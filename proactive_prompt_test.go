package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRunProactivePromptEmptyOutputMarksLastRun(t *testing.T) {
	setupTempStateForTest(t)
	initState()

	restore := stubProactiveForTest(t)
	defer restore()

	ranAt := time.Date(2026, time.April, 16, 7, 6, 0, 0, time.Local)
	proactiveNow = func() time.Time { return ranAt }

	var sendCount int
	var historyCount int
	proactiveModelRunner = func(context.Context, string, string, string) (string, error) {
		return " \n\t ", nil
	}
	proactiveTelegramSender = func(context.Context, string) error {
		sendCount++
		return nil
	}
	proactiveHistoryWriter = func(string, time.Time, string) {
		historyCount++
	}

	p := &Prompt{Name: "morning", FileName: "morning.md", Key: "morning", Body: "test"}
	if err := runProactivePrompt(context.Background(), p, proactiveRunKey(p)); err != nil {
		t.Fatalf("runProactivePrompt: %v", err)
	}

	if sendCount != 0 {
		t.Fatalf("sendCount = %d, want 0", sendCount)
	}
	if historyCount != 0 {
		t.Fatalf("historyCount = %d, want 0", historyCount)
	}

	ReadState(func(s *State) {
		if got := s.ProactiveLastRun["morning"]; !got.Equal(ranAt) {
			t.Fatalf("ProactiveLastRun[morning] = %v, want %v", got, ranAt)
		}
	})
}

func TestRunProactivePromptSendFailureDoesNotMarkLastRun(t *testing.T) {
	setupTempStateForTest(t)
	initState()

	restore := stubProactiveForTest(t)
	defer restore()

	ranAt := time.Date(2026, time.April, 16, 7, 6, 0, 0, time.Local)
	proactiveNow = func() time.Time { return ranAt }

	proactiveModelRunner = func(context.Context, string, string, string) (string, error) {
		return "hello", nil
	}
	proactiveTelegramSender = func(context.Context, string) error {
		return errors.New("boom")
	}
	proactiveHistoryWriter = func(string, time.Time, string) {
		t.Fatal("history should not be written when send fails")
	}

	p := &Prompt{Name: "morning", FileName: "morning.md", Key: "morning", Body: "test"}
	if err := runProactivePrompt(context.Background(), p, proactiveRunKey(p)); err == nil {
		t.Fatal("runProactivePrompt error = nil, want error")
	}

	ReadState(func(s *State) {
		if got := s.ProactiveLastRun["morning"]; !got.IsZero() {
			t.Fatalf("ProactiveLastRun[morning] = %v, want zero", got)
		}
	})
}

func stubProactiveForTest(t *testing.T) func() {
	t.Helper()

	oldModelRunner := proactiveModelRunner
	oldTelegramSender := proactiveTelegramSender
	oldHistoryWriter := proactiveHistoryWriter
	oldNow := proactiveNow

	return func() {
		proactiveModelRunner = oldModelRunner
		proactiveTelegramSender = oldTelegramSender
		proactiveHistoryWriter = oldHistoryWriter
		proactiveNow = oldNow
	}
}
