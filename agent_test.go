package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseAgent(t *testing.T) {
	cases := []struct {
		in      string
		want    agentKind
		wantErr bool
	}{
		{in: "", want: agentClaude},
		{in: "claude", want: agentClaude},
		{in: "Claude", want: agentClaude},
		{in: "grok", want: agentGrok},
		{in: "GROK", want: agentGrok},
		{in: "codex", want: agentCodex},
		{in: "cursor", wantErr: true},
	}
	for _, tc := range cases {
		got, err := parseAgent(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("parseAgent(%q) err = nil, want error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("parseAgent(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("parseAgent(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAgentPromptArgs(t *testing.T) {
	prompt := "hello world"
	sid := "11111111-2222-4333-8444-555555555555"
	claudeSel := ModelSelection{Model: "claude-fable", Effort: effortMedium}
	grokSel := ModelSelection{Model: "grok", Effort: effortMax}
	codexSel := ModelSelection{Model: "codex-sol", Effort: effortHigh}

	claudeNew := agentPromptArgs(claudeSel, sid, true, prompt)
	if !containsAll(claudeNew, "--dangerously-skip-permissions", "--model", "fable", "--effort", "medium", "--session-id", sid, "-p", prompt) {
		t.Fatalf("claude new args = %#v", claudeNew)
	}
	claudeResume := agentPromptArgs(claudeSel, sid, false, prompt)
	if !containsAll(claudeResume, "--resume", sid, "-p", prompt) {
		t.Fatalf("claude resume args = %#v", claudeResume)
	}

	grokNew := agentPromptArgs(grokSel, sid, true, prompt)
	if !containsAll(grokNew, "--always-approve", "-m", "grok-4.6", "--effort", "xhigh", "--session-id", sid, "-p", prompt) {
		t.Fatalf("grok new args = %#v", grokNew)
	}
	if containsAll(grokNew, "--resume") {
		t.Fatalf("grok new args unexpectedly resume: %#v", grokNew)
	}
	grokResume := agentPromptArgs(grokSel, sid, false, prompt)
	if !containsAll(grokResume, "--always-approve", "--resume", sid, "-p", prompt) {
		t.Fatalf("grok resume args = %#v", grokResume)
	}

	codexNew := agentPromptArgs(codexSel, sid, true, prompt)
	if !containsAll(codexNew, "exec", "--dangerously-bypass-approvals-and-sandbox", "--json", "-m", "gpt-5.6-sol", "-c", "model_reasoning_effort=high", prompt) {
		t.Fatalf("codex new args = %#v", codexNew)
	}
	for _, arg := range codexNew {
		if arg == "resume" || arg == sid {
			t.Fatalf("codex new args should not resume: %#v", codexNew)
		}
	}
	codexResume := agentPromptArgs(codexSel, sid, false, prompt)
	if !containsAll(codexResume, "exec", "resume", sid, prompt) {
		t.Fatalf("codex resume args = %#v", codexResume)
	}
}

func TestParseCodexExecJSON(t *testing.T) {
	stdout := strings.Join([]string{
		`{"type":"thread.started","thread_id":"019ce76e-c1c9-76b2-bd23-cb3c6b3724e2"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"PING"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`,
	}, "\n")
	sid, msg, err := parseCodexExecJSON(stdout)
	if err != nil {
		t.Fatalf("parseCodexExecJSON: %v", err)
	}
	if sid != "019ce76e-c1c9-76b2-bd23-cb3c6b3724e2" {
		t.Fatalf("session id = %q", sid)
	}
	if msg != "PING" {
		t.Fatalf("message = %q, want PING", msg)
	}

	if _, _, err := parseCodexExecJSON("not json at all"); err == nil {
		t.Fatal("expected error for non-JSON stdout")
	}
}

func TestShouldStartNewAgentSession(t *testing.T) {
	old := config
	config = DefaultConfig()
	t.Cleanup(func() { config = old })

	now := time.Date(2026, 8, 19, 10, 0, 0, 0, time.Local)
	if !shouldStartNewAgentSession(now, &SessionState{}) {
		t.Fatal("empty session should start new")
	}

	recent := SessionState{
		SessionID:      "abc",
		FirstMessageAt: now.Add(-2 * time.Hour),
		LastMessageAt:  now.Add(-10 * time.Minute),
	}
	if shouldStartNewAgentSession(now, &recent) {
		t.Fatal("recent interaction should keep session")
	}

	stale := SessionState{
		SessionID:      "abc",
		FirstMessageAt: time.Date(2026, 8, 18, 10, 0, 0, 0, time.Local),
		LastMessageAt:  time.Date(2026, 8, 18, 22, 0, 0, 0, time.Local),
	}
	if !shouldStartNewAgentSession(now, &stale) {
		t.Fatal("session from previous day should rotate")
	}
}

func TestBuildAgentCLIInvocationReusesPerAgentSessions(t *testing.T) {
	setupTempStateForTest(t)
	initState()

	oldConfig := config
	oldRoot := rootDir
	oldPrompts := promptsDir
	config = DefaultConfig()
	config.HealthFile = ""
	rootDir = t.TempDir()
	promptsDir = t.TempDir()
	t.Cleanup(func() {
		config = oldConfig
		rootDir = oldRoot
		promptsDir = oldPrompts
	})
	if err := os.WriteFile(filepath.Join(promptsDir, "init.md"), []byte("init {now}"), 0o666); err != nil {
		t.Fatal(err)
	}

	now := time.Now().Local()
	UpdateState(func(s *State) {
		s.GrokSession = SessionState{
			SessionID:      "grok-session",
			FirstMessageAt: now,
			LastMessageAt:  now,
		}
		s.ClaudeSession = SessionState{
			SessionID:      "claude-session",
			FirstMessageAt: now,
			LastMessageAt:  now,
		}
		s.Model = "claude-fable"
	})

	inv, err := buildAgentCLIInvocation(now, "hello", "")
	if err != nil {
		t.Fatalf("buildAgentCLIInvocation claude: %v", err)
	}
	if inv.StartNew {
		t.Fatal("claude should reuse its existing session when switching from grok")
	}
	if inv.Session.SessionID != "claude-session" {
		t.Fatalf("claude session id = %q, want claude-session", inv.Session.SessionID)
	}

	UpdateState(func(s *State) { s.Model = "grok" })
	inv, err = buildAgentCLIInvocation(now, "hello", "")
	if err != nil {
		t.Fatalf("buildAgentCLIInvocation grok: %v", err)
	}
	if inv.StartNew {
		t.Fatal("grok should reuse its existing session")
	}
	if inv.Session.SessionID != "grok-session" {
		t.Fatalf("grok session id = %q, want grok-session", inv.Session.SessionID)
	}

	UpdateState(func(s *State) { s.Model = "codex-sol" })
	inv, err = buildAgentCLIInvocation(now, "hello", "")
	if err != nil {
		t.Fatalf("buildAgentCLIInvocation codex: %v", err)
	}
	if !inv.StartNew {
		t.Fatal("codex with no saved session should start new")
	}
	ReadState(func(s *State) {
		if s.GrokSession.SessionID != "grok-session" {
			t.Fatalf("grok session was overwritten: %#v", s.GrokSession)
		}
		if s.ClaudeSession.SessionID != "claude-session" {
			t.Fatalf("claude session was overwritten: %#v", s.ClaudeSession)
		}
	})
}

func TestResetSessionClearsAllAgents(t *testing.T) {
	setupTempStateForTest(t)
	initState()

	oldConfig := config
	config = DefaultConfig()
	config.Agent = "grok"
	t.Cleanup(func() { config = oldConfig })

	now := time.Now().Local()
	UpdateState(func(s *State) {
		s.ClaudeSession = SessionState{SessionID: "claude-session", FirstMessageAt: now, LastMessageAt: now}
		s.GrokSession = SessionState{SessionID: "grok-session", FirstMessageAt: now, LastMessageAt: now}
		s.CodexSession = SessionState{SessionID: "codex-session", FirstMessageAt: now, LastMessageAt: now}
		s.ResetSession()
	})
	ReadState(func(s *State) {
		if s.ClaudeSession.SessionID != "" || s.GrokSession.SessionID != "" || s.CodexSession.SessionID != "" {
			t.Fatalf("sessions = claude=%#v grok=%#v codex=%#v, want all cleared", s.ClaudeSession, s.GrokSession, s.CodexSession)
		}
	})
}

func TestExpireSessionsForNewDayClearsStaleAgents(t *testing.T) {
	setupTempStateForTest(t)
	initState()

	oldConfig := config
	oldRoot := rootDir
	oldPrompts := promptsDir
	config = DefaultConfig()
	config.HealthFile = ""
	rootDir = t.TempDir()
	promptsDir = t.TempDir()
	t.Cleanup(func() {
		config = oldConfig
		rootDir = oldRoot
		promptsDir = oldPrompts
	})
	if err := os.WriteFile(filepath.Join(promptsDir, "init.md"), []byte("init {now}"), 0o666); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 8, 19, 10, 0, 0, 0, time.Local)
	yesterday := time.Date(2026, 8, 18, 22, 0, 0, 0, time.Local)
	UpdateState(func(s *State) {
		s.ClaudeSession = SessionState{SessionID: "claude-today", FirstMessageAt: now.Add(-time.Hour), LastMessageAt: now.Add(-time.Minute)}
		s.GrokSession = SessionState{SessionID: "grok-yesterday", FirstMessageAt: yesterday, LastMessageAt: yesterday}
		s.CodexSession = SessionState{SessionID: "codex-yesterday", FirstMessageAt: yesterday, LastMessageAt: yesterday}
		s.Model = "claude-fable"
	})

	inv, err := buildAgentCLIInvocation(now, "hello", "")
	if err != nil {
		t.Fatalf("buildAgentCLIInvocation: %v", err)
	}
	if inv.StartNew {
		t.Fatal("today's claude session should be reused")
	}
	if inv.Session.SessionID != "claude-today" {
		t.Fatalf("claude session id = %q, want claude-today", inv.Session.SessionID)
	}
	ReadState(func(s *State) {
		if s.GrokSession.SessionID != "" {
			t.Fatalf("stale grok session survived: %#v", s.GrokSession)
		}
		if s.CodexSession.SessionID != "" {
			t.Fatalf("stale codex session survived: %#v", s.CodexSession)
		}
		if s.ClaudeSession.SessionID != "claude-today" {
			t.Fatalf("claude session = %#v, want kept", s.ClaudeSession)
		}
	})
}

func containsAll(args []string, want ...string) bool {
	set := make(map[string]int, len(args))
	for _, a := range args {
		set[a]++
	}
	for _, w := range want {
		if set[w] == 0 {
			return false
		}
	}
	return true
}
