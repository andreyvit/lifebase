package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPendingTelegramImagesPersistAcrossRestart(t *testing.T) {
	setupTempStateForTest(t)
	initState()

	now := time.Date(2026, time.April, 10, 11, 12, 13, 0, time.Local)
	img1 := filepath.Join(t.TempDir(), "one.jpg")
	img2 := filepath.Join(t.TempDir(), "two.png")
	appendPendingTelegramImages([]PendingTelegramImage{
		{Path: img1, ReceivedAt: now},
		{Path: img2, ReceivedAt: now.Add(time.Minute)},
	})

	state = State{}
	initState()

	got := takePendingTelegramImagePaths()
	if len(got) != 2 || got[0] != img1 || got[1] != img2 {
		t.Fatalf("takePendingTelegramImagePaths() = %#v, want [%q %q]", got, img1, img2)
	}
	if remaining := pendingTelegramImagePaths(); len(remaining) != 0 {
		t.Fatalf("pendingTelegramImagePaths() after take = %#v, want empty", remaining)
	}
}

func TestBuildIngestInputBlockTelegramMessage(t *testing.T) {
	img1 := filepath.Join(t.TempDir(), "one.jpg")
	img2 := filepath.Join(t.TempDir(), "two.png")

	block, err := buildIngestInputBlock([]string{img1, img2}, "telegram-message", "hello from telegram")
	if err != nil {
		t.Fatalf("buildIngestInputBlock: %v", err)
	}

	for _, img := range []string{img1, img2} {
		if !strings.Contains(block, `<attached-image path="`+img+`"/>`) {
			t.Fatalf("block missing attached-image tag for %q:\n%s", img, block)
		}
	}
	if !strings.Contains(block, "<telegram-message>hello from telegram</telegram-message>") {
		t.Fatalf("block missing telegram-message tag:\n%s", block)
	}
}

func TestBuildIngestInputBlockVoiceMemo(t *testing.T) {
	img := filepath.Join(t.TempDir(), "voice-context.jpg")

	block, err := buildIngestInputBlock([]string{img}, "voice-memo-transcription", "transcribed text")
	if err != nil {
		t.Fatalf("buildIngestInputBlock: %v", err)
	}

	if !strings.Contains(block, `<attached-image path="`+img+`"/>`) {
		t.Fatalf("block missing attached-image tag:\n%s", block)
	}
	if !strings.Contains(block, "<voice-memo-transcription>\ntranscribed text\n</voice-memo-transcription>") {
		t.Fatalf("block missing voice transcription tag:\n%s", block)
	}
}

func TestTelegramMediaGroupFlushesCaptionedAlbumOnce(t *testing.T) {
	now := time.Date(2026, time.April, 10, 12, 0, 0, 0, time.Local)
	groups := map[string]*pendingTelegramMediaGroup{}

	addTelegramMediaGroupBatch(groups, telegramImageBatch{
		ReceivedAt:   now,
		MediaGroupID: "album-1",
		Caption:      "caption",
		ImagePaths:   []string{"a.jpg"},
	})
	addTelegramMediaGroupBatch(groups, telegramImageBatch{
		ReceivedAt:   now.Add(500 * time.Millisecond),
		MediaGroupID: "album-1",
		ImagePaths:   []string{"b.jpg"},
	})

	batches := takeReadyTelegramMediaGroups(groups, now.Add(500*time.Millisecond+telegramMediaGroupFlushAfter+time.Millisecond), false)
	if len(batches) != 1 {
		t.Fatalf("takeReadyTelegramMediaGroups len = %d, want 1", len(batches))
	}
	if batches[0].Caption != "caption" {
		t.Fatalf("caption = %q, want %q", batches[0].Caption, "caption")
	}
	if len(batches[0].ImagePaths) != 2 || batches[0].ImagePaths[0] != "a.jpg" || batches[0].ImagePaths[1] != "b.jpg" {
		t.Fatalf("image paths = %#v, want [a.jpg b.jpg]", batches[0].ImagePaths)
	}
}

func TestHandleTelegramCommandCancelClearsPendingTelegramImages(t *testing.T) {
	setupTempStateForTest(t)
	initState()

	now := time.Date(2026, time.April, 10, 12, 30, 0, 0, time.Local)
	UpdateState(func(st *State) {
		st.PendingLog = &PendingLogInput{
			FileBasename: "MealLog",
			Title:        "MealLog",
			ChatID:       123,
			ExpiresAt:    now.Add(15 * time.Minute),
		}
		st.PendingTelegramImages = []PendingTelegramImage{
			{Path: filepath.Join(t.TempDir(), "queued.jpg"), ReceivedAt: now},
		}
	})

	if !handleTelegramCommand(context.Background(), "/cancel", now, 123) {
		t.Fatalf("handleTelegramCommand did not handle /cancel")
	}

	ReadState(func(st *State) {
		if st.PendingLog != nil {
			t.Fatalf("PendingLog = %#v, want nil", st.PendingLog)
		}
		if len(st.PendingTelegramImages) != 0 {
			t.Fatalf("PendingTelegramImages = %#v, want empty", st.PendingTelegramImages)
		}
	})
}

func TestRunTelegramActivityIndicatorRepeatsUntilCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var count atomic.Int32
	done := make(chan struct{})
	go func() {
		runTelegramActivityIndicator(ctx, 10*time.Millisecond, func(context.Context) error {
			count.Add(1)
			return nil
		})
		close(done)
	}()

	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) {
		if count.Load() >= 3 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := count.Load(); got < 3 {
		t.Fatalf("chat action count = %d, want at least 3", got)
	}

	cancel()

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("runTelegramActivityIndicator did not stop after cancel")
	}
}

func setupTempStateForTest(t *testing.T) {
	t.Helper()

	oldState := state
	oldStatePath := statePath
	oldAudioRecorderDir := audioRecorderDir
	oldSecrets := secrets

	tmp := t.TempDir()
	state = State{}
	statePath = filepath.Join(tmp, "lifebase-state.json")
	audioRecorderDir = ""
	secrets = Secrets{}

	t.Cleanup(func() {
		state = oldState
		statePath = oldStatePath
		audioRecorderDir = oldAudioRecorderDir
		secrets = oldSecrets
	})

	if err := os.WriteFile(statePath, []byte("{}\n"), 0o666); err != nil {
		t.Fatalf("write state file: %v", err)
	}
}
