package main

import (
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type FileSet map[string]int

var (
	contentIgnoreSet FileSet
)

func initContentPathSets() error {
	ignores := slices.Clone(builtinIgnores)

	// Add parent directories of auto-generated files to ignores.
	for _, f := range []string{config.HealthFile, config.ProactiveHistoryFile} {
		if f = strings.TrimSpace(f); f != "" {
			dir := path.Dir(filepath.ToSlash(f))
			if dir != "" && dir != "." {
				ignores = append(ignores, dir)
			}
		}
	}

	var err error
	contentIgnoreSet, err = buildPathSet(ignores)
	if err != nil {
		return fmt.Errorf("init ignore paths: %w", err)
	}
	return nil
}

func buildPathSet(paths []string) (FileSet, error) {
	set := make(FileSet, len(paths))
	for i, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		p = strings.TrimPrefix(p, "./")
		p = strings.TrimPrefix(p, "/")

		p, err := resolveSpecialDir(p)
		if err != nil {
			return nil, err
		}
		if p == "" {
			continue
		}

		p = filepath.ToSlash(filepath.Clean(filepath.FromSlash(p)))
		if p == "" || p == "." {
			continue
		}
		if existing, ok := set[p]; ok && existing <= i {
			continue
		}
		set[p] = i
	}
	return set, nil
}

func (f FileSet) Match(relPath string) int {
	rank, ok := f[relPath]
	if !ok {
		return -1
	}
	return rank
}

func (f FileSet) Matches(relPath string) bool {
	return f.Match(relPath) >= 0
}

func (f FileSet) MatchAnyParent(relPath string) int {
	best := -1
	for prefix, rank := range f {
		if rem, ok := strings.CutPrefix(relPath, prefix); ok && (rem == "" || rem[0] == '/') {
			if best < 0 || rank < best {
				best = rank
			}
		}
	}
	return best
}

func (f FileSet) MatchesAnyParent(relPath string) bool {
	return f.MatchAnyParent(relPath) >= 0
}

func resolveSpecialDir(name string) (string, error) {
	if name == "" || name[0] != '{' || name[len(name)-1] != '}' {
		return name, nil
	}
	var fullPath string
	switch name {
	case "{RawInputs}":
		fullPath = rawInputsDir
	case "{Prompts}":
		fullPath = promptsDir
	default:
		return "", fmt.Errorf("%s: unknown special directory", name)
	}
	if rootDir == "" {
		return "", fmt.Errorf("%s: rootDir is not set", name)
	}
	if fullPath == "" {
		return "", nil
	}
	rel, err := relSlashPath(rootDir, fullPath)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return rel, nil
}

// walkContentFiles walks all content markdown files under rootDir and calls f
// with the file name (without extension), its slash-separated relPath, and the
// file's last modification time.
func walkContentFiles(f func(name, relPath string, lastMod time.Time)) error {
	if strings.TrimSpace(rootDir) == "" {
		return fmt.Errorf("rootDir is not set")
	}
	if contentIgnoreSet == nil {
		return fmt.Errorf("content ignore set is not initialized")
	}

	return filepath.WalkDir(rootDir, func(fullPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if fullPath == rootDir {
			return nil
		}
		name := entry.Name()
		if isHiddenFileName(name) {
			return skip(entry)
		}
		if !entry.IsDir() && !hasContentFileSuffix(name) {
			return nil
		}
		rel, err := relSlashPath(rootDir, fullPath)
		if err != nil {
			return err
		}
		if contentIgnoreSet.Matches(rel) {
			return skip(entry)
		}
		if !entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			base := path.Base(rel)
			f(strings.TrimSuffix(base, path.Ext(base)), rel, info.ModTime())
		}
		return nil
	})
}

func relSlashPath(basepath, targpath string) (string, error) {
	rel, err := filepath.Rel(basepath, targpath)
	if err != nil {
		return "", err
	}
	rel = filepath.ToSlash(rel)
	rel = strings.TrimPrefix(rel, "./")
	return rel, nil
}

func hasContentFileSuffix(name string) bool {
	return strings.EqualFold(path.Ext(name), ".md")
}

func isHiddenFileName(name string) bool {
	return strings.HasPrefix(name, ".")
}

func isHiddenFilePath(relPath string) bool {
	return strings.HasPrefix(relPath, ".") || strings.Contains(relPath, "/.")
}

func isContentFilePath(relPath string, ignore FileSet) bool {
	if isHiddenFilePath(relPath) {
		return false
	}
	if !hasContentFileSuffix(relPath) {
		return false
	}
	if ignore.MatchesAnyParent(relPath) {
		return false
	}
	return true
}

func skip(entry fs.DirEntry) error {
	if entry.IsDir() {
		return filepath.SkipDir
	}
	return nil
}
