package main

import (
	"bytes"
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type ingestTaskType int

const (
	taskAudioFile ingestTaskType = iota
	taskRawInput
	taskTelegramAudio
	taskTelegramText
)

type ingestTask struct {
	typ         ingestTaskType
	path        string
	displayName string
	deleteAfter bool // delete file after successful ingestion (for Telegram temp audio)
	imagePaths  []string
}

// daemon starts watchers and a single worker that serializes ingestion.
func daemon(ctx context.Context) error {
	// Fatal early if essential tools are missing
	checkDaemonDependencies()

	tasks := make(chan ingestTask, 64)

	// Worker: single-threaded ingestion
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case t := <-tasks:
				stopActivity := startTelegramActivityIndicator(ctx, t)
				if err := processIngestTask(ctx, t); err != nil {
					stopActivity()
					// Report error via Telegram, do not crash
					_ = sendTelegramText(ctx, "Ingest failed for "+filepath.Base(t.path)+": "+err.Error())
					log.Printf("Ingest failed: %v", err)
					continue
				}
				stopActivity()
				if t.deleteAfter {
					if err := os.Remove(t.path); err != nil {
						log.Printf("Cleanup: failed to remove temp file %s: %v", t.path, err)
					} else {
						log.Printf("Cleanup: removed temp file %s", t.path)
					}
				}
			}
		}
	}()

	// Start watchers
	if strings.TrimSpace(audioRecorderDir) != "" {
		go watchAudioFolder(ctx, audioRecorderDir, tasks)
	} else {
		log.Printf("Audio recorder dir not configured; skipping audio folder watch")
	}
	go pollTelegram(ctx, tasks)
	// Start proactive check-ins runner
	go proactiveRunner(ctx)

	// Block forever until context is cancelled
	<-ctx.Done()
	return ctx.Err()
}

const telegramActivityInterval = 4 * time.Second

func startTelegramActivityIndicator(parent context.Context, task ingestTask) func() {
	if !shouldSendTelegramActivity(task) {
		return func() {}
	}

	ctx, cancel := context.WithCancel(parent)
	go runTelegramActivityIndicator(ctx, telegramActivityInterval, func(ctx context.Context) error {
		return sendTelegramChatAction(ctx, "typing")
	})
	return cancel
}

func shouldSendTelegramActivity(task ingestTask) bool {
	switch task.typ {
	case taskTelegramAudio, taskTelegramText:
		return true
	case taskAudioFile:
		return task.deleteAfter
	default:
		return false
	}
}

func runTelegramActivityIndicator(ctx context.Context, interval time.Duration, send func(context.Context) error) {
	if err := send(ctx); err != nil {
		log.Printf("Telegram chat action failed: %v", err)
	}
	if interval <= 0 {
		<-ctx.Done()
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := send(ctx); err != nil {
				log.Printf("Telegram chat action failed: %v", err)
			}
		}
	}
}

func processIngestTask(ctx context.Context, task ingestTask) error {
	switch task.typ {
	case taskAudioFile, taskRawInput:
		return add(ctx, task.path)
	case taskTelegramAudio:
		return addTelegramAudio(ctx, task.path, task.imagePaths)
	case taskTelegramText:
		return addTelegramText(ctx, task.path, task.imagePaths)
	default:
		return nil
	}
}

func checkDaemonDependencies() {
	if _, err := exec.LookPath("claude"); err != nil {
		log.Fatal("claude not found in PATH. Please install Claude Code CLI (https://docs.anthropic.com/en/docs/claude-code).")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		log.Fatal("ffmpeg not found in PATH. Please install it, e.g. 'brew install ffmpeg'.")
	}
	// Verify libmp3lame encoder is present
	cmd := exec.Command("ffmpeg", "-hide_banner", "-encoders")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		log.Fatalf("failed to run ffmpeg to check encoders: %v", err)
	}
	if !strings.Contains(buf.String(), "libmp3lame") {
		log.Fatal("ffmpeg built without libmp3lame encoder. Please reinstall ffmpeg with MP3 support (Homebrew ffmpeg includes lame by default). Try: brew reinstall ffmpeg")
	}
}

func watchAudioFolder(ctx context.Context, dir string, tasks chan<- ingestTask) {
	log.Printf("Watching for new audio files in: %s", dir)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			entries, err := os.ReadDir(dir)
			if err != nil {
				log.Printf("Audio watch: read dir error: %v", err)
				continue
			}
			for _, de := range entries {
				if de.IsDir() {
					continue
				}
				name := de.Name()
				if !strings.HasSuffix(strings.ToLower(name), ".m4a") {
					continue
				}
				if stateHasSeen(name) {
					continue
				}
				// If paused, ignore and don't mark as seen
				var paused bool
				ReadState(func(s *State) { paused = s.Paused })
				if paused {
					log.Printf("Paused: ignoring new audio %s", name)
					continue
				}
				// Mark as seen BEFORE processing to avoid duplicate work
				stateMarkSeen(name)

				full := filepath.Join(dir, name)
				log.Printf("New audio: %s", name)
				// Update last incoming time using file modtime if available
				if fi, err := os.Stat(full); err == nil {
					updateLastIncoming(fi.ModTime())
				} else {
					updateLastIncoming(time.Now())
				}
				tasks <- ingestTask{typ: taskAudioFile, path: full, displayName: name}
			}
		}
	}
}
