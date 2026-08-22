package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

type agentKind string

const (
	agentClaude agentKind = "claude"
	agentCodex  agentKind = "codex"
	agentGrok   agentKind = "grok"
)

type agentSpec struct {
	Kind        agentKind
	Bin         string
	DisplayName string
	HistoryMode string
	InstallHint string
}

func parseAgent(s string) (agentKind, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "claude":
		return agentClaude, nil
	case "codex":
		return agentCodex, nil
	case "grok":
		return agentGrok, nil
	default:
		return "", fmt.Errorf("lifebase configuration error: agent must be claude, grok, or codex (got %q)", s)
	}
}

func specFor(kind agentKind) agentSpec {
	switch kind {
	case agentCodex:
		return agentSpec{
			Kind:        agentCodex,
			Bin:         "codex",
			DisplayName: "Codex",
			HistoryMode: "codex-cli",
			InstallHint: "codex not found in PATH. Please install Codex CLI (https://github.com/openai/codex).",
		}
	case agentGrok:
		return agentSpec{
			Kind:        agentGrok,
			Bin:         "grok",
			DisplayName: "Grok",
			HistoryMode: "grok-cli",
			InstallHint: "grok not found in PATH. Please install Grok CLI (https://x.ai/cli/install.sh).",
		}
	default:
		return agentSpec{
			Kind:        agentClaude,
			Bin:         "claude",
			DisplayName: "Claude Code",
			HistoryMode: "claude-cli",
			InstallHint: "claude not found in PATH. Please install Claude Code CLI (https://docs.anthropic.com/en/docs/claude-code).",
		}
	}
}

func configuredAgent() agentSpec {
	kind, err := parseAgent(config.Agent)
	if err != nil {
		log.Fatal(err)
	}
	return specFor(kind)
}

func agentPromptArgs(kind agentKind, sessionID string, newSession bool, prompt string) []string {
	switch kind {
	case agentCodex:
		args := []string{"exec", "--dangerously-bypass-approvals-and-sandbox", "--json"}
		if !newSession {
			args = append(args, "resume", sessionID)
		}
		return append(args, prompt)
	case agentGrok:
		args := []string{"--always-approve", "--no-auto-update", "--output-format", "plain"}
		if newSession {
			args = append(args, "--session-id", sessionID)
		} else {
			args = append(args, "--resume", sessionID)
		}
		return append(args, "-p", prompt)
	default:
		args := []string{"--dangerously-skip-permissions"}
		if newSession {
			args = append(args, "--session-id", sessionID)
		} else {
			args = append(args, "--resume", sessionID)
		}
		return append(args, "-p", prompt)
	}
}

func runAgentCommand(ctx context.Context, spec agentSpec, args []string) (sessionID, out string, err error) {
	logAgentCLICommand(spec.Bin, args)
	cmd := exec.CommandContext(ctx, spec.Bin, args...)
	cmd.Dir = rootDir
	if spec.Kind == agentGrok {
		cmd.Env = append(os.Environ(), "GROK_DISABLE_AUTOUPDATER=1")
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	stdoutStr := stdout.String()
	stderrStr := strings.TrimSpace(stderr.String())
	if stderrStr != "" {
		log.Printf("%s stderr: %s", spec.Bin, stderrStr)
	}

	result := strings.TrimSpace(stdoutStr)
	if spec.Kind == agentCodex {
		id, msg, parseErr := parseCodexExecJSON(stdoutStr)
		sessionID = id
		if parseErr != nil {
			if runErr != nil {
				return id, "", fmt.Errorf("%w: %s", runErr, combineAgentOutput(stdoutStr, stderrStr))
			}
			return id, "", parseErr
		}
		result = msg
	}
	if runErr != nil {
		return sessionID, "", fmt.Errorf("%w: %s", runErr, combineAgentOutput(stdoutStr, stderrStr))
	}
	return sessionID, result, nil
}

func combineAgentOutput(stdout, stderr string) string {
	stdout = strings.TrimSpace(stdout)
	stderr = strings.TrimSpace(stderr)
	switch {
	case stdout == "":
		return stderr
	case stderr == "":
		return stdout
	default:
		return stdout + "\n" + stderr
	}
}

func parseCodexExecJSON(stdout string) (sessionID, message string, err error) {
	sawJSON := false
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
			Item     struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		sawJSON = true
		switch ev.Type {
		case "thread.started":
			if ev.ThreadID != "" {
				sessionID = ev.ThreadID
			}
		case "item.completed":
			if ev.Item.Type == "agent_message" && ev.Item.Text != "" {
				message = ev.Item.Text
			}
		}
	}
	if !sawJSON && strings.TrimSpace(stdout) != "" {
		return "", "", fmt.Errorf("codex exec: expected JSON event stream")
	}
	return sessionID, message, nil
}

func logAgentCLICommand(bin string, args []string) {
	quotedArgs := make([]string, len(args))
	for i, arg := range args {
		if strings.ContainsAny(arg, ` "'()[]<>*?!$`) {
			arg = strconv.Quote(arg)
		}
		quotedArgs[i] = arg
	}
	log.Printf("$ %s %s", bin, strings.Join(quotedArgs, " "))
}
