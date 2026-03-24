package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var historyMu sync.Mutex
var historyCurrentPath string

func historyEnabled() bool {
	return config.WriteHistory
}

func historyDirPath() (string, error) {
	if strings.TrimSpace(rootDir) == "" {
		return "", fmt.Errorf("rootDir is not set")
	}
	return filepath.Join(rootDir, ".history"), nil
}

var historySuffixRe = regexp.MustCompile(`[^a-z0-9_-]+`)

func normalizeHistorySuffix(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, " ", "-")
	s = historySuffixRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-_")
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return s
}

func historyAppend(s string) {
	if !historyEnabled() {
		return
	}
	if strings.TrimSpace(s) == "" {
		return
	}
	historyMu.Lock()
	defer historyMu.Unlock()

	if strings.TrimSpace(historyCurrentPath) == "" {
		return
	}
	f, err := os.OpenFile(historyCurrentPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o666)
	if err != nil {
		return
	}
	defer f.Close()

	_, _ = f.WriteString(s)
	if !strings.HasSuffix(s, "\n") {
		_, _ = f.WriteString("\n")
	}
}

func historyStartInvocation(now time.Time, suffix, mode, sessionID string) {
	if !historyEnabled() {
		return
	}
	now = now.Local()
	suffix = normalizeHistorySuffix(suffix)
	if suffix == "" {
		suffix = normalizeHistorySuffix(mode)
	}
	if suffix == "" {
		suffix = "run"
	}

	historyMu.Lock()
	defer historyMu.Unlock()

	dir, err := historyDirPath()
	if err != nil {
		return
	}
	_ = os.MkdirAll(dir, 0o777)

	stamp := now.Format("20060102-150405")
	candidate := filepath.Join(dir, fmt.Sprintf("%s-%s.txt", stamp, suffix))
	fn := candidate
	for i := 2; ; i++ {
		if _, err := os.Stat(fn); os.IsNotExist(err) {
			break
		}
		fn = filepath.Join(dir, fmt.Sprintf("%s-%s-%d.txt", stamp, suffix, i))
	}

	historyCurrentPath = fn
	header := fmt.Sprintf("===== %s | %s | session=%s =====\n", now.Format("2006-01-02 15:04:05 Mon"), mode, sessionID)
	_ = os.WriteFile(fn, []byte(header), 0o666)
}

func historyLogPrompt(prompt string) {
	if !historyEnabled() {
		return
	}
	historyAppend(renderHistoryPrompt(prompt))
}

func historyLogAssistant(text string) {
	if !historyEnabled() {
		return
	}
	var out strings.Builder
	out.WriteString("---- assistant ----\n")
	out.WriteString(text)
	if !strings.HasSuffix(text, "\n") {
		out.WriteString("\n")
	}
	out.WriteString("---- end assistant ----\n")
	historyAppend(out.String())
}

func renderHistoryPrompt(prompt string) string {
	var out strings.Builder
	out.WriteString("---- prompt ----\n")
	out.WriteString(prompt)
	if !strings.HasSuffix(prompt, "\n") {
		out.WriteString("\n")
	}
	out.WriteString("---- end prompt ----\n")
	return out.String()
}
