package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestModelAndEffortKeyboardsIncludeCancel(t *testing.T) {
	models := modelMenuKeyboard()
	if len(models) != len(modelCatalog)+1 {
		t.Fatalf("model keyboard rows = %d, want %d", len(models), len(modelCatalog)+1)
	}
	if models[len(models)-1][0].Text != cancelButtonText {
		t.Fatalf("model keyboard last = %q, want %q", models[len(models)-1][0].Text, cancelButtonText)
	}
	for i, spec := range modelCatalog {
		if models[i][0].Text != spec.Label {
			t.Fatalf("model keyboard[%d] = %q, want %q", i, models[i][0].Text, spec.Label)
		}
	}

	efforts := effortMenuKeyboard()
	if len(efforts) != len(effortCatalog)+1 {
		t.Fatalf("effort keyboard rows = %d, want %d", len(efforts), len(effortCatalog)+1)
	}
	if efforts[len(efforts)-1][0].Text != cancelButtonText {
		t.Fatalf("effort keyboard last = %q, want %q", efforts[len(efforts)-1][0].Text, cancelButtonText)
	}
}

func TestModelCommandWithoutArgsOpensPendingMenu(t *testing.T) {
	setupTempStateForTest(t)
	initState()

	now := time.Date(2026, time.April, 10, 12, 30, 0, 0, time.Local)
	if !handleTelegramCommand(context.Background(), "/model", now, 123) {
		t.Fatal("handleTelegramCommand did not handle /model")
	}
	ReadState(func(st *State) {
		if st.PendingMenu == nil {
			t.Fatal("PendingMenu is nil")
		}
		if st.PendingMenu.Kind != pendingMenuModel {
			t.Fatalf("PendingMenu.Kind = %q, want %q", st.PendingMenu.Kind, pendingMenuModel)
		}
		if st.PendingMenu.ChatID != 123 {
			t.Fatalf("PendingMenu.ChatID = %d, want 123", st.PendingMenu.ChatID)
		}
		if st.PendingLog != nil {
			t.Fatalf("PendingLog = %#v, want nil", st.PendingLog)
		}
	})
}

func TestEffortCommandWithoutArgsOpensPendingMenu(t *testing.T) {
	setupTempStateForTest(t)
	initState()

	now := time.Date(2026, time.April, 10, 12, 30, 0, 0, time.Local)
	if !handleTelegramCommand(context.Background(), "/effort", now, 99) {
		t.Fatal("handleTelegramCommand did not handle /effort")
	}
	ReadState(func(st *State) {
		if st.PendingMenu == nil || st.PendingMenu.Kind != pendingMenuEffort {
			t.Fatalf("PendingMenu = %#v", st.PendingMenu)
		}
	})
}

func TestPendingModelChoiceAppliesAndClearsMenu(t *testing.T) {
	setupTempStateForTest(t)
	initState()
	oldLookPath := lookPath
	lookPath = func(file string) (string, error) { return file, nil }
	t.Cleanup(func() { lookPath = oldLookPath })

	now := time.Date(2026, time.April, 10, 12, 30, 0, 0, time.Local)
	if !handleTelegramCommand(context.Background(), "/model", now, 123) {
		t.Fatal("handleTelegramCommand did not handle /model")
	}
	if !handleTelegramPendingInput(context.Background(), "Grok 4.6", now, 123) {
		t.Fatal("pending model choice was not consumed")
	}
	ReadState(func(st *State) {
		if st.PendingMenu != nil {
			t.Fatalf("PendingMenu = %#v, want nil", st.PendingMenu)
		}
		if st.Model != "grok" {
			t.Fatalf("model = %q, want grok", st.Model)
		}
	})
}

func TestPendingEffortChoiceAppliesAndClearsMenu(t *testing.T) {
	setupTempStateForTest(t)
	initState()

	now := time.Date(2026, time.April, 10, 12, 30, 0, 0, time.Local)
	if !handleTelegramCommand(context.Background(), "/effort", now, 123) {
		t.Fatal("handleTelegramCommand did not handle /effort")
	}
	if !handleTelegramPendingInput(context.Background(), "Extra High", now, 123) {
		t.Fatal("pending effort choice was not consumed")
	}
	ReadState(func(st *State) {
		if st.PendingMenu != nil {
			t.Fatalf("PendingMenu = %#v, want nil", st.PendingMenu)
		}
		if st.Effort != "xhigh" {
			t.Fatalf("effort = %q, want xhigh", st.Effort)
		}
	})
}

func TestPendingMenuUnknownChoiceStaysOpen(t *testing.T) {
	setupTempStateForTest(t)
	initState()

	now := time.Date(2026, time.April, 10, 12, 30, 0, 0, time.Local)
	if !handleTelegramCommand(context.Background(), "/model", now, 123) {
		t.Fatal("handleTelegramCommand did not handle /model")
	}
	if !handleTelegramPendingInput(context.Background(), "not a model", now, 123) {
		t.Fatal("unknown model choice should stay in the menu")
	}
	ReadState(func(st *State) {
		if st.PendingMenu == nil || st.PendingMenu.Kind != pendingMenuModel {
			t.Fatalf("PendingMenu = %#v, want model menu", st.PendingMenu)
		}
	})
}

func TestCancelButtonClearsPendingMenu(t *testing.T) {
	setupTempStateForTest(t)
	initState()

	now := time.Date(2026, time.April, 10, 12, 30, 0, 0, time.Local)
	if !handleTelegramCommand(context.Background(), "/model", now, 123) {
		t.Fatal("handleTelegramCommand did not handle /model")
	}
	if !handleTelegramPendingInput(context.Background(), "Cancel", now, 123) {
		t.Fatal("Cancel button was not consumed")
	}
	ReadState(func(st *State) {
		if st.PendingMenu != nil {
			t.Fatalf("PendingMenu = %#v, want nil", st.PendingMenu)
		}
		if st.PendingLog != nil {
			t.Fatalf("PendingLog = %#v, want nil", st.PendingLog)
		}
	})
}

func TestCancelButtonClearsPendingLog(t *testing.T) {
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
	})
	if !handleTelegramPendingInput(context.Background(), "Cancel", now, 123) {
		t.Fatal("Cancel button was not consumed")
	}
	ReadState(func(st *State) {
		if st.PendingLog != nil {
			t.Fatalf("PendingLog = %#v, want nil", st.PendingLog)
		}
	})
}

func TestModelMenuReplacesPendingLog(t *testing.T) {
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
	})
	if !handleTelegramCommand(context.Background(), "/model", now, 123) {
		t.Fatal("handleTelegramCommand did not handle /model")
	}
	ReadState(func(st *State) {
		if st.PendingLog != nil {
			t.Fatalf("PendingLog = %#v, want nil", st.PendingLog)
		}
		if st.PendingMenu == nil || st.PendingMenu.Kind != pendingMenuModel {
			t.Fatalf("PendingMenu = %#v, want model menu", st.PendingMenu)
		}
	})
}

func TestTopLevelCommandClearsPendingMenu(t *testing.T) {
	setupTempStateForTest(t)
	initState()

	now := time.Date(2026, time.April, 10, 12, 30, 0, 0, time.Local)
	if !handleTelegramCommand(context.Background(), "/model", now, 123) {
		t.Fatal("handleTelegramCommand did not handle /model")
	}
	if !handleTelegramCommand(context.Background(), "/pause", now, 123) {
		t.Fatal("handleTelegramCommand did not handle /pause")
	}
	ReadState(func(st *State) {
		if st.PendingMenu != nil {
			t.Fatalf("PendingMenu = %#v, want nil after /pause", st.PendingMenu)
		}
		if !st.Paused {
			t.Fatal("Paused = false, want true")
		}
	})
}

func TestModelCommandWithArgsClearsPendingMenu(t *testing.T) {
	setupTempStateForTest(t)
	initState()
	oldLookPath := lookPath
	lookPath = func(file string) (string, error) { return file, nil }
	t.Cleanup(func() { lookPath = oldLookPath })

	now := time.Date(2026, time.April, 10, 12, 30, 0, 0, time.Local)
	if !handleTelegramCommand(context.Background(), "/model", now, 123) {
		t.Fatal("handleTelegramCommand did not handle /model")
	}
	if !handleTelegramCommand(context.Background(), "/model Claude Opus 5", now, 123) {
		t.Fatal("handleTelegramCommand did not handle /model with args")
	}
	ReadState(func(st *State) {
		if st.PendingMenu != nil {
			t.Fatalf("PendingMenu = %#v, want nil", st.PendingMenu)
		}
		if st.Model != "claude-opus" {
			t.Fatalf("model = %q, want claude-opus", st.Model)
		}
	})
}

func TestIsCancelText(t *testing.T) {
	if !isCancelText("Cancel") || !isCancelText(" cancel ") || !isCancelText("CANCEL") {
		t.Fatal("expected Cancel variants to match")
	}
	if isCancelText("/cancel") || isCancelText("Canceled") || isCancelText("") {
		t.Fatal("expected non-button text to be ignored")
	}
}

func TestReplyKeyboardMarkupJSON(t *testing.T) {
	markup := tgReplyKeyboardMarkup{
		Keyboard:        modelMenuKeyboard(),
		ResizeKeyboard:  true,
		OneTimeKeyboard: true,
	}
	if len(markup.Keyboard) == 0 || markup.Keyboard[len(markup.Keyboard)-1][0].Text != "Cancel" {
		t.Fatalf("markup keyboard = %#v", markup.Keyboard)
	}
	remove := tgReplyKeyboardRemove{RemoveKeyboard: true}
	if !remove.RemoveKeyboard {
		t.Fatal("remove_keyboard not set")
	}
	if !strings.Contains(renderModelMenu(defaultModelSelection()), "Choose a model:") {
		t.Fatal("model prompt missing")
	}
}
