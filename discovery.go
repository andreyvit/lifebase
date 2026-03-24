package main

import (
	"io/fs"
	"path/filepath"
	"strings"
)

const maxDiscoveryDepth = 5

func walkVisibleFiles(maxDepth int, f func(relPath string, entry fs.DirEntry) error) error {
	if strings.TrimSpace(rootDir) == "" {
		return nil
	}

	return filepath.WalkDir(rootDir, func(fullPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if fullPath == rootDir {
			return nil
		}

		relPath, err := relSlashPath(rootDir, fullPath)
		if err != nil {
			return err
		}
		if isHiddenRelPath(relPath) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if pathDepth(relPath) > maxDepth {
				return filepath.SkipDir
			}
			return nil
		}
		return f(relPath, entry)
	})
}

func relSlashPath(basePath, targetPath string) (string, error) {
	rel, err := filepath.Rel(basePath, targetPath)
	if err != nil {
		return "", err
	}
	rel = filepath.ToSlash(rel)
	rel = strings.TrimPrefix(rel, "./")
	return rel, nil
}

func isHiddenRelPath(relPath string) bool {
	for _, part := range strings.Split(relPath, "/") {
		if strings.HasPrefix(part, ".") {
			return true
		}
	}
	return false
}

func pathDepth(relPath string) int {
	if relPath == "" || relPath == "." {
		return 0
	}
	return strings.Count(relPath, "/") + 1
}
