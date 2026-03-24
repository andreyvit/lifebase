package main

import (
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

var (
	rootDir              string
	rawInputsDir         string
	promptsDir           string
	audioRecorderDir     string
	appleHealthExportDir string

	config  Config = DefaultConfig()
	secrets Secrets
)

const (
	rawFileTimeFormat = "2006-01-02-150405"
)

func main() {
	log.SetFlags(0)
	log.SetOutput(os.Stdout)

	var configFile string
	var addFn string
	var proactiveName string
	var commitMode bool
	var health48 bool
	flag.Usage = func() {
		log.Printf("usage: lifebase [-f <config-file>] -add <input-file>")
		log.Printf("   or: lifebase [-f <config-file>] -proactive <name>")
		log.Printf("   or: lifebase [-f <config-file>] -commit")
		log.Printf("   or: lifebase [-f <config-file>] -health48")
		log.Printf("   or: lifebase [-f <config-file>]   (to run daemon mode)")
		flag.PrintDefaults()
	}
	flag.StringVar(&configFile, "f", "./lifebase.yaml", "path to lifebase config file")
	flag.StringVar(&addFn, "add", "", "add a new file")
	flag.StringVar(&proactiveName, "proactive", "", "run a proactive check-in now (e.g. 'evening')")
	flag.BoolVar(&commitMode, "commit", false, "commit and push content markdown changes")
	flag.BoolVar(&health48, "health48", false, "print Apple Health hourly buckets for the last 48 hours")
	flag.Parse()

	ensure(decodeUserFile(configFile, &config))
	if config.DayBoundaryHour < 0 || config.DayBoundaryHour > 23 {
		log.Fatalf("lifebase configuration error: day_boundary_hour must be between 0 and 23")
	}
	if config.AgentSessionExtendIfInteractedWithin < 0 {
		log.Fatalf("lifebase configuration error: agent_session_extend_if_interacted_within must be >= 0")
	}
	configBaseDir := filepath.Dir(must(filepath.Abs(configFile)))
	rootDir = mustResolvePath(configBaseDir, config.ContentDir, "content_dir")
	rawInputsDir = mustResolvePath(rootDir, config.RawInputsDir, "raw_inputs_dir")
	promptsDir = mustResolvePath(rootDir, config.PromptsDir, "prompts_dir")
	statePath = mustResolvePath(rootDir, config.StateFile, "state_file")
	audioRecorderDir = resolvePath(rootDir, config.AudioRecorderDir)
	appleHealthExportDir = resolvePath(configBaseDir, config.AppleHealthExportDir)
	secretsPath := mustResolvePath(rootDir, config.SecretsFile, "secrets_file")
	ensure(decodeUserFile(secretsPath, &secrets))
	ensure(os.MkdirAll(rawInputsDir, 0o777))
	ensure(initContentPathSets())

	// Ensure auto-generated file parent directories exist
	for _, f := range []string{config.HealthFile, config.ProactiveHistoryFile} {
		if f = strings.TrimSpace(f); f != "" {
			ensure(os.MkdirAll(filepath.Dir(filepath.Join(rootDir, filepath.FromSlash(f))), 0o777))
		}
	}

	initState()
	maybeRunHealthDayChangeProcessing(context.Background(), time.Now().Local())

	if addFn != "" {
		err := add(context.Background(), addFn)
		if err != nil {
			log.Fatalf("** %v", err)
		}
	} else if proactiveName != "" {
		if err := runProactiveByName(context.Background(), proactiveName); err != nil {
			log.Fatalf("** %v", err)
		}
	} else if commitMode {
		if _, err := commitAndPushContentMarkdown(context.Background(), "content changes"); err != nil {
			log.Fatalf("** %v", err)
		}
	} else if health48 {
		out, err := renderAppleHealthLast48Hours(time.Now().Local())
		if err != nil {
			log.Fatalf("** %v", err)
		}
		fmt.Println(out)
	} else {
		if err := daemon(context.Background()); err != nil {
			log.Fatalf("** %v", err)
		}
	}
}

func add(ctx context.Context, fn string) error {
	fn = must(filepath.Abs(fn))
	tm := must(os.Stat(fn)).ModTime().Local().Format(rawFileTimeFormat)
	transcriptionFn := filepath.Join(rawInputsDir, fmt.Sprintf("%s.md", tm))

	var textExts = []string{".txt", ".md"}

	if strings.HasPrefix(fn, rawInputsDir) || slices.Contains(textExts, filepath.Ext(fn)) {
		transcriptionFn = fn
		tm = strings.TrimSuffix(filepath.Base(fn), filepath.Ext(fn))
		log.Printf("Reusing transcription: %s", filepath.Base(transcriptionFn))
	} else {
		log.Printf("Transcribing %s...", filepath.Base(fn))
		text, err := transcribe(ctx, fn)
		if err != nil {
			return fmt.Errorf("transcribe: %v", err)
		}
		text = strings.TrimSpace(text) + "\n"

		ensure(os.WriteFile(transcriptionFn, []byte(text), 0666))
		log.Printf("Transcribed: %s", filepath.Base(transcriptionFn))
	}

	raw, err := os.ReadFile(transcriptionFn)
	if err != nil {
		return fmt.Errorf("read transcription: %v", err)
	}
	transcription := strings.TrimSpace(string(raw))

	claudeOut, err := runIngestModel(ctx, fmt.Sprintf("<voice-memo-transcription>\n%s\n</voice-memo-transcription>\n\n"+readPrompt("system-ingest.md"), transcription))
	if err != nil {
		return fmt.Errorf("ingestion: %v", err)
	}
	claudeOut = strings.TrimSpace(claudeOut)
	claudeOut = strings.TrimPrefix(claudeOut, "---")
	claudeOut = strings.TrimSpace(claudeOut)

	if claudeOut == "" {
		log.Printf("Claude produced empty output, nothing to send")
		return nil
	}

	log.Printf("Sending Telegram message:\n%s\n\n", claudeOut)
	if err := sendTelegramText(ctx, claudeOut); err != nil {
		return fmt.Errorf("telegram send: %v", err)
	}
	log.Printf("Sent Claude output to Telegram (%d chars)", len(claudeOut))

	return nil
}

func shouldStartNewClaudeSession(now time.Time, sess *SessionState) bool {
	if sess.SessionID == "" {
		return true
	}
	if !sess.LastMessageAt.IsZero() && now.Sub(sess.LastMessageAt) < config.AgentSessionExtendIfInteractedWithin {
		return false
	}
	boundary := time.Date(now.Year(), now.Month(), now.Day(), config.DayBoundaryHour, 0, 0, 0, now.Location())
	if now.Before(boundary) {
		return false
	}
	if sess.FirstMessageAt.IsZero() {
		return true
	}
	return sess.FirstMessageAt.Before(boundary)
}

func readPrompt(name string) string {
	fn := promptPath(name)
	if fn == "" {
		log.Fatalf("** invalid prompt path: %q", name)
	}
	b, err := os.ReadFile(fn)
	if err != nil {
		log.Fatalf("** cannot read prompt %q at %s: %v", name, fn, err)
	}
	return strings.TrimSpace(string(b))
}

func newUUIDv4() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
