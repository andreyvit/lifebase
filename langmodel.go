package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
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

func runModel(ctx context.Context, prompt string, historySuffix string, extraContext string) (result string, err error) {
	// Serialize all model invocations globally.
	modelMu.Lock()
	defer modelMu.Unlock()

	maybeRunHealthDayChangeProcessing(ctx, time.Now().Local())

	bestEffortCommitAndPushAllChanges(ctx, "changes before running model")
	restartAfterReturn := false
	defer func() {
		bestEffortCommitAndPushAllChanges(ctx, "changes")
		if restartAfterReturn {
			requestSelfRestart()
		}
	}()

	out, err := runAgentCLI(ctx, prompt, historySuffix, extraContext)
	if err != nil {
		return "", err
	}
	out, action, err := handleAgentControlOutput(func(correctionPrompt string) (string, error) {
		return runAgentCLI(ctx, correctionPrompt, historySuffix, "")
	}, out)
	if err != nil {
		return "", err
	}
	if action == agentControlRestart {
		restartAfterReturn = true
		return "", nil
	}
	return out, nil
}

type agentControlAction int

const (
	agentControlNone agentControlAction = iota
	agentControlRestart
)

func handleAgentControlOutput(rerun func(prompt string) (string, error), out string) (string, agentControlAction, error) {
	for attempt := 0; attempt < 3; attempt++ {
		if strings.TrimSpace(out) == lifebaseRestartDirective {
			return "", agentControlRestart, nil
		}
		if !strings.Contains(out, lifebaseRestartDirective) {
			return out, agentControlNone, nil
		}
		if rerun == nil {
			return "", agentControlNone, fmt.Errorf("agent response contains %s but cannot be corrected", lifebaseRestartDirective)
		}

		next, err := rerun(restartDirectiveCorrectionPrompt())
		if err != nil {
			return "", agentControlNone, fmt.Errorf("agent restart directive correction: %w", err)
		}
		out = next
	}
	return "", agentControlNone, fmt.Errorf("agent response still contains %s after correction attempts", lifebaseRestartDirective)
}

func restartDirectiveCorrectionPrompt() string {
	return fmt.Sprintf(`Your previous response was rejected because it contained %s but was not exactly that directive after trimming whitespace.

If you want LifeBase to restart, reply with exactly:
%s

If you do not want LifeBase to restart, reply with a normal user-facing message that does not contain %s.`, lifebaseRestartDirective, lifebaseRestartDirective, lifebaseRestartDirective)
}

type agentCLIInvocation struct {
	InitPrompt string // non-empty only for new sessions
	Prompt     string
	Session    SessionState
	StartNew   bool
	Spec       agentSpec
}

func loadPersistedAgentSession(kind agentKind) SessionState {
	var sess SessionState
	ReadState(func(s *State) {
		sess = *s.sessionPtr(kind)
	})
	return sess
}

func persistAgentSession(kind agentKind, sess SessionState) {
	UpdateState(func(s *State) {
		*s.sessionPtr(kind) = sess
	})
}

func buildAgentCLIInvocation(now time.Time, prompt string, extraContext string) (agentCLIInvocation, error) {
	now = now.Local()
	spec := configuredAgent()

	UpdateState(func(s *State) {
		s.expireSessionsForNewDay(now)
	})

	sess := loadPersistedAgentSession(spec.Kind)
	startNew := shouldStartNewAgentSession(now, &sess)
	if startNew {
		sess = SessionState{
			FirstMessageAt: now,
			LastMessageAt:  now,
		}
		if spec.Kind != agentCodex {
			id, err := newUUIDv4()
			if err != nil {
				return agentCLIInvocation{}, err
			}
			sess.SessionID = id
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

	return agentCLIInvocation{
		InitPrompt: initPrompt,
		Prompt:     prompt,
		Session:    sess,
		StartNew:   startNew,
		Spec:       spec,
	}, nil
}

func runAgentCLI(ctx context.Context, prompt string, historySuffix string, extraContext string) (string, error) {
	now := time.Now().Local()
	inv, err := buildAgentCLIInvocation(now, prompt, extraContext)
	if err != nil {
		return "", err
	}
	log.Printf("Running %s CLI...", inv.Spec.DisplayName)

	historyStartInvocation(now, historySuffix, inv.Spec.HistoryMode, inv.Session.SessionID)

	if inv.StartNew && inv.InitPrompt != "" {
		initArgs := agentPromptArgs(inv.Spec.Kind, inv.Session.SessionID, true, inv.InitPrompt)
		historyLogPrompt(inv.InitPrompt)

		sid, initOut, err := runAgentCommand(ctx, inv.Spec, initArgs)
		historyLogAssistant(initOut)
		if err != nil {
			return "", fmt.Errorf("%s init: %w", inv.Spec.Bin, err)
		}
		if inv.Spec.Kind == agentCodex {
			if sid == "" {
				return "", fmt.Errorf("codex init: missing session id")
			}
			inv.Session.SessionID = sid
		}
		persistAgentSession(inv.Spec.Kind, inv.Session)
	}

	newSession := inv.StartNew && inv.InitPrompt == ""
	args := agentPromptArgs(inv.Spec.Kind, inv.Session.SessionID, newSession, inv.Prompt)
	historyLogPrompt(inv.Prompt)

	sid, out, err := runAgentCommand(ctx, inv.Spec, args)
	if err != nil {
		historyLogAssistant(out)
		return "", err
	}
	if inv.Spec.Kind == agentCodex && sid != "" {
		inv.Session.SessionID = sid
	}

	persistAgentSession(inv.Spec.Kind, inv.Session)
	historyLogAssistant(out)
	return out, nil
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
