package main

import (
	"context"
	"fmt"
	"os"
	"strings"
)

const (
	lifebaseRestartDirective = "<<<LIFEBASE:RESTART>>>"
	lifebaseRestartDone      = "<lifebase:restart-done />"
)

var (
	restartProcess = func() {
		os.Exit(0)
	}
	restartDoneModelRunner    = runIngestModel
	restartDoneTelegramSender = sendTelegramText
)

func requestSelfRestart() {
	UpdateState(func(st *State) {
		st.IsRestartingMyself = true
	})
	restartProcess()
}

func completePendingSelfRestart(ctx context.Context) error {
	var pending bool
	ReadState(func(st *State) {
		pending = st.IsRestartingMyself
	})
	if !pending {
		return nil
	}

	UpdateState(func(st *State) {
		st.IsRestartingMyself = false
	})

	out, err := restartDoneModelRunner(ctx, lifebaseRestartDone)
	if err != nil {
		return fmt.Errorf("restart completion agent message: %w", err)
	}
	out = normalizeAgentUserMessage(out)
	if out == "" {
		return nil
	}
	if err := restartDoneTelegramSender(ctx, out); err != nil {
		return fmt.Errorf("restart completion telegram send: %w", err)
	}
	return nil
}

func normalizeAgentUserMessage(out string) string {
	out = strings.TrimSpace(out)
	out = strings.TrimPrefix(out, "---")
	return strings.TrimSpace(out)
}
