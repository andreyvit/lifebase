package main

import (
	"context"
	"strings"
	"testing"
)

func TestHandleAgentControlOutputRestartDirective(t *testing.T) {
	out, action, err := handleAgentControlOutput(nil, "\n\t"+lifebaseRestartDirective+"\n")
	if err != nil {
		t.Fatalf("handleAgentControlOutput: %v", err)
	}
	if out != "" {
		t.Fatalf("out = %q, want empty", out)
	}
	if action != agentControlRestart {
		t.Fatalf("action = %v, want agentControlRestart", action)
	}
}

func TestHandleAgentControlOutputRejectsMixedRestartDirective(t *testing.T) {
	var correctionPrompt string
	out, action, err := handleAgentControlOutput(func(prompt string) (string, error) {
		correctionPrompt = prompt
		return "corrected response", nil
	}, "Please restart "+lifebaseRestartDirective)
	if err != nil {
		t.Fatalf("handleAgentControlOutput: %v", err)
	}
	if action != agentControlNone {
		t.Fatalf("action = %v, want agentControlNone", action)
	}
	if out != "corrected response" {
		t.Fatalf("out = %q, want corrected response", out)
	}
	if !strings.Contains(correctionPrompt, lifebaseRestartDirective) {
		t.Fatalf("correction prompt does not mention directive:\n%s", correctionPrompt)
	}
}

func TestCompletePendingSelfRestartNotifiesAgentAndClearsFlag(t *testing.T) {
	setupTempStateForTest(t)
	initState()
	UpdateState(func(st *State) {
		st.IsRestartingMyself = true
	})

	oldRunner := restartDoneModelRunner
	oldSender := restartDoneTelegramSender
	var gotPrompt string
	var gotTelegram string
	restartDoneModelRunner = func(ctx context.Context, prompt string) (string, error) {
		gotPrompt = prompt
		return "done", nil
	}
	restartDoneTelegramSender = func(ctx context.Context, text string) error {
		gotTelegram = text
		return nil
	}
	t.Cleanup(func() {
		restartDoneModelRunner = oldRunner
		restartDoneTelegramSender = oldSender
	})

	if err := completePendingSelfRestart(context.Background()); err != nil {
		t.Fatalf("completePendingSelfRestart: %v", err)
	}
	if gotPrompt != lifebaseRestartDone {
		t.Fatalf("prompt = %q, want %q", gotPrompt, lifebaseRestartDone)
	}
	if gotTelegram != "done" {
		t.Fatalf("telegram = %q, want done", gotTelegram)
	}
	ReadState(func(st *State) {
		if st.IsRestartingMyself {
			t.Fatal("IsRestartingMyself = true, want false")
		}
	})
}
