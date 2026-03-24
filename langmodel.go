package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

func runIngestModel(ctx context.Context, prompt string) (string, error) {
	return runModel(ctx, prompt, "ingest", "")
}

func runProactiveModel(ctx context.Context, prompt string, historySuffix string, extraContext string) (string, error) {
	return runModel(ctx, prompt, historySuffix, extraContext)
}

func runModel(ctx context.Context, prompt string, historySuffix string, extraContext string) (string, error) {
	// Serialize all model invocations globally.
	modelMu.Lock()
	defer modelMu.Unlock()

	maybeRunHealthDayChangeProcessing(ctx, time.Now().Local())

	bestEffortCommitAndPushContentMarkdown(ctx, "content changes before running model")
	defer bestEffortCommitAndPushContentMarkdown(ctx, "content changes")

	return runClaudeCLI(ctx, prompt, historySuffix, extraContext)
}

type claudeCLIInvocation struct {
	InitPrompt string // non-empty only for new sessions
	Prompt     string
	Session    SessionState
	StartNew   bool
}

func buildClaudeCLIInvocation(now time.Time, prompt string, extraContext string) (claudeCLIInvocation, error) {
	now = now.Local()

	var sess SessionState
	ReadState(func(s *State) {
		sess = s.ClaudeSession
	})
	startNew := shouldStartNewClaudeSession(now, &sess)
	if startNew {
		id, err := newUUIDv4()
		if err != nil {
			return claudeCLIInvocation{}, err
		}
		sess = SessionState{
			SessionID:      id,
			FirstMessageAt: now,
			LastMessageAt:  now,
		}
	} else {
		sess.LastMessageAt = now
	}

	prompt = strings.TrimSpace(prompt)
	if strings.TrimSpace(extraContext) != "" {
		prompt = strings.TrimSpace(extraContext) + "\n\n" + strings.TrimSpace(prompt)
	}

	// Suffix prompt with current local time for better context.
	nowStr := now.Format("2006-01-02 Mon 15:04")
	prompt = fmt.Sprintf("%s\n\nNow is %s.", strings.TrimSpace(prompt), nowStr)

	var initPrompt string
	if startNew {
		// Write health file before init
		if err := writeHealthMetricsFile(now); err != nil {
			log.Printf("Health metrics file skipped: %v", err)
		}

		// Read and substitute init.md template
		initRaw := readPrompt("init.md")
		values := map[string]string{
			"{now}": now.Format("2006-01-02 15:04 Mon"),
		}
		initPrompt = strings.TrimSpace(Subst(initRaw, values))
	}

	return claudeCLIInvocation{
		InitPrompt: initPrompt,
		Prompt:     prompt,
		Session:    sess,
		StartNew:   startNew,
	}, nil
}

func runClaudeCLI(ctx context.Context, prompt string, historySuffix string, extraContext string) (string, error) {
	log.Printf("Running Claude Code CLI...")
	now := time.Now().Local()
	inv, err := buildClaudeCLIInvocation(now, prompt, extraContext)
	if err != nil {
		return "", err
	}

	historyStartInvocation(now, historySuffix, "claude-cli", inv.Session.SessionID)

	if inv.StartNew && inv.InitPrompt != "" {
		// Phase 1: init invocation
		initArgs := []string{"--dangerously-skip-permissions", "--session-id", inv.Session.SessionID, "-p", inv.InitPrompt}
		logClaudeCLICommand(initArgs)
		historyLogPrompt(inv.InitPrompt)

		initCmd := exec.CommandContext(ctx, "claude", initArgs...)
		initCmd.Dir = rootDir
		initOut, err := initCmd.CombinedOutput()
		historyLogAssistant(strings.TrimSpace(string(initOut)))
		if err != nil {
			return "", fmt.Errorf("claude init: %w: %s", err, strings.TrimSpace(string(initOut)))
		}

		// Update session state after init
		UpdateState(func(s *State) {
			s.ClaudeSession = inv.Session
		})
	}

	// Phase 2 (or only phase for continuing sessions): actual prompt
	var args []string
	if inv.StartNew {
		args = []string{"--dangerously-skip-permissions", "--resume", inv.Session.SessionID, "-p", inv.Prompt}
	} else {
		args = []string{"--dangerously-skip-permissions", "--resume", inv.Session.SessionID, "-p", inv.Prompt}
	}
	logClaudeCLICommand(args)
	historyLogPrompt(inv.Prompt)

	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = rootDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		historyLogAssistant(strings.TrimSpace(string(out)))
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}

	UpdateState(func(s *State) {
		s.ClaudeSession = inv.Session
	})

	res := string(out)
	historyLogAssistant(strings.TrimSpace(res))
	return res, nil
}

func logClaudeCLICommand(args []string) {
	quotedArgs := make([]string, len(args))
	for i, arg := range args {
		if strings.ContainsAny(arg, ` "'()[]<>*?!$`) {
			arg = strconv.Quote(arg)
		}
		quotedArgs[i] = arg
	}
	log.Printf("$ claude %s", strings.Join(quotedArgs, " "))
}

// writeHealthMetricsFile writes health data to the configured health file path.
func writeHealthMetricsFile(now time.Time) error {
	healthFile := strings.TrimSpace(config.HealthFile)
	if healthFile == "" {
		return nil
	}

	healthCtx, err := builtinAppleHealthTodayContext(now)
	if err != nil {
		return err
	}
	if strings.TrimSpace(healthCtx) == "" {
		return nil
	}

	fullPath := filepath.Join(rootDir, filepath.FromSlash(healthFile))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o777); err != nil {
		return err
	}
	return os.WriteFile(fullPath, []byte(healthCtx+"\n"), 0o666)
}

var modelMu sync.Mutex
