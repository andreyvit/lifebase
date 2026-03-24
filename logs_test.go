package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverLogSpecs(t *testing.T) {
	oldRootDir := rootDir
	t.Cleanup(func() {
		rootDir = oldRootDir
	})

	rootDir = t.TempDir()

	files := []string{
		"Logs/StoolLog.md",
		"Logs/MealLog-2026-01.md",
		"Other/FooLog.md",
		"Other/FooLog-2026-03.md",
		"Other/FooLog-2026-03-brief.md",
		".hidden/HiddenLog.md",
		"Deep/1/2/3/4/5/TooDeepLog.md",
	}
	for _, rel := range files {
		full := filepath.Join(rootDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o777); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte("test\n"), 0o666); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	specs, err := discoverLogSpecs()
	if err != nil {
		t.Fatalf("discoverLogSpecs: %v", err)
	}
	if len(specs) != 3 {
		t.Fatalf("discoverLogSpecs len=%d, want 3 (%+v)", len(specs), specs)
	}

	got := make(map[string]LogSpec, len(specs))
	for _, spec := range specs {
		got[spec.FileBasename] = spec
	}

	if spec := got["FooLog"]; spec.DirRelPath != "Other" || spec.PrefaceRelPath != "Other/FooLog.md" {
		t.Fatalf("FooLog = %+v, want dir=Other preface=Other/FooLog.md", spec)
	}
	if spec := got["MealLog"]; spec.DirRelPath != "Logs" || spec.PrefaceRelPath != "" {
		t.Fatalf("MealLog = %+v, want dir=Logs no preface", spec)
	}
	if spec := got["StoolLog"]; spec.DirRelPath != "Logs" || spec.PrefaceRelPath != "Logs/StoolLog.md" {
		t.Fatalf("StoolLog = %+v, want dir=Logs preface=Logs/StoolLog.md", spec)
	}
	if _, ok := got["TooDeepLog"]; ok {
		t.Fatalf("TooDeepLog should not be discovered beyond depth %d", maxDiscoveryDepth)
	}
}
