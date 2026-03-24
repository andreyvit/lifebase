package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInitRepoCreatesReadyToRunExample(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, time.March, 25, 9, 5, 0, 0, time.Local)

	if err := initRepo(filepath.Join(root, "lifebase.yaml"), now); err != nil {
		t.Fatalf("initRepo: %v", err)
	}

	for _, rel := range []string{
		"lifebase.yaml",
		"lifebase-secrets.yaml",
		"lifebase-state.json",
		".gitignore",
		"AGENTS.md",
		"Core/AboutMe.md",
		"Core/Dreams.md",
		"Core/TopOfMind.md",
		"Core/CurrentProjects.md",
		"Core/Schedule.md",
		"Core/Routines.md",
		"Core/AINotes.md",
		"Diary/2026.md",
		"Daily/2026-03.md",
		"Logs/MealLog-2026-03.md",
		"Therapy/Therapy-2026.md",
		"Prompts/init.md",
		"Prompts/system-ingest.md",
		"Raw",
	} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("%s missing: %v", rel, err)
		}
	}

	daily := string(must(os.ReadFile(filepath.Join(root, "Daily/2026-03.md"))))
	if !strings.Contains(daily, "## 2026-03-25 Wed") {
		t.Fatalf("daily file did not get date substitution:\n%s", daily)
	}

	logFile := string(must(os.ReadFile(filepath.Join(root, "Logs/MealLog-2026-03.md"))))
	if !strings.Contains(logFile, "2026-03-25 09:05 Wed") {
		t.Fatalf("log file did not get date substitution:\n%s", logFile)
	}

	agents := string(must(os.ReadFile(filepath.Join(root, "AGENTS.md"))))
	if strings.Contains(agents, "YYYY") {
		t.Fatalf("AGENTS.md still contains placeholders:\n%s", agents)
	}
}

func TestInitRepoMergesGitignoreAndSkipsExistingFiles(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, time.March, 25, 9, 5, 0, 0, time.Local)

	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("README.md\n/lifebase-state.json\n"), 0o666); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("custom agents\n"), 0o666); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	if err := initRepo(filepath.Join(root, "lifebase.yaml"), now); err != nil {
		t.Fatalf("initRepo: %v", err)
	}

	gitignore := string(must(os.ReadFile(filepath.Join(root, ".gitignore"))))
	for _, line := range []string{"README.md", "/lifebase-state.json", "/lifebase-secrets.yaml", "/Raw/"} {
		if !strings.Contains(gitignore, line) {
			t.Fatalf(".gitignore missing %q:\n%s", line, gitignore)
		}
	}

	agents := string(must(os.ReadFile(filepath.Join(root, "AGENTS.md"))))
	if agents != "custom agents\n" {
		t.Fatalf("AGENTS.md was overwritten:\n%s", agents)
	}
}
