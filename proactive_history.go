package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const proactiveHistoryMaxEntries = 30

// appendProactiveRecent appends the proactive output to the history file
// so Claude can read it and avoid repetition, trimming old entries.
func appendProactiveRecent(name string, at time.Time, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}

	histFile := strings.TrimSpace(config.ProactiveHistoryFile)
	if histFile == "" {
		return
	}

	fullPath := filepath.Join(rootDir, filepath.FromSlash(histFile))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o777); err != nil {
		return
	}

	entry := fmt.Sprintf("## %s — %s\n\n%s", at.Local().Format("2006-01-02 Mon 15:04"), strings.TrimSpace(name), text)

	// Read existing entries, append new one, trim to limit.
	var entries []string
	if b, err := os.ReadFile(fullPath); err == nil {
		entries = splitProactiveEntries(string(b))
	}
	entries = append(entries, entry)
	if len(entries) > proactiveHistoryMaxEntries {
		entries = entries[len(entries)-proactiveHistoryMaxEntries:]
	}

	_ = os.WriteFile(fullPath, []byte(strings.Join(entries, "\n\n")+"\n"), 0o666)
}

// splitProactiveEntries splits the history file on "## " headers.
func splitProactiveEntries(content string) []string {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}

	var entries []string
	parts := strings.Split("\n"+content, "\n## ")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		entries = append(entries, "## "+p)
	}
	return entries
}
