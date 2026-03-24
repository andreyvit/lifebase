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
)

type ingestTask struct {
	typ         ingestTaskType
	path        string
	displayName string // used for notifications like "Processing <name>"
	deleteAfter bool   // delete file after successful ingestion (for Telegram temp audio)
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
				if t.typ == taskAudioFile && t.displayName != "" {
					_ = sendTelegramText(ctx, "Processing "+t.displayName)
				}
				if err := add(ctx, t.path); err != nil {
					// Report error via Telegram, do not crash
					_ = sendTelegramText(ctx, "Ingest failed for "+filepath.Base(t.path)+": "+err.Error())
					log.Printf("Ingest failed: %v", err)
					continue
				}
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
