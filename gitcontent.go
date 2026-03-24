package main

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type contentCommitResult struct {
	DidCommit bool
	CommitSHA string
}

func commitAndPushContentMarkdown(ctx context.Context, message string) (contentCommitResult, error) {
	res, err := commitContentMarkdown(ctx, message)
	if err != nil {
		return contentCommitResult{}, err
	}
	if !res.DidCommit {
		return res, nil
	}
	if _, err := runGit(ctx, "push"); err != nil {
		return contentCommitResult{}, fmt.Errorf("git push: %w", err)
	}
	return res, nil
}

func commitContentMarkdown(ctx context.Context, message string) (contentCommitResult, error) {
	if strings.TrimSpace(message) == "" {
		return contentCommitResult{}, fmt.Errorf("empty commit message")
	}

	paths, err := listChangedContentMarkdownPaths(ctx)
	if err != nil {
		return contentCommitResult{}, err
	}
	if len(paths) == 0 {
		log.Printf("No content markdown changes to commit (%s)", message)
		return contentCommitResult{DidCommit: false}, nil
	}

	// Stage content markdown changes (including deletions) while respecting gitignore.
	for _, chunk := range chunkStrings(paths, 200) {
		if _, err := runGit(ctx, append([]string{"add", "-A", "--"}, chunk...)...); err != nil {
			return contentCommitResult{}, fmt.Errorf("git add: %w", err)
		}
	}

	stagedAny, err := hasStagedContentMarkdownChanges(ctx)
	if err != nil {
		return contentCommitResult{}, err
	}
	if !stagedAny {
		log.Printf("No content markdown changes staged to commit (%s)", message)
		return contentCommitResult{DidCommit: false}, nil
	}

	// Commit only those paths so staged automation changes (if any) aren't swept in.
	if err := gitCommitPaths(ctx, message, paths); err != nil {
		return contentCommitResult{}, err
	}
	sha, _ := runGit(ctx, "rev-parse", "HEAD")
	return contentCommitResult{DidCommit: true, CommitSHA: strings.TrimSpace(sha)}, nil
}

func bestEffortCommitAndPushContentMarkdown(ctx context.Context, message string) {
	if _, err := commitAndPushContentMarkdown(ctx, message); err != nil {
		log.Printf("Content commit/push failed (%s): %v", message, err)
	}
}

func runGit(ctx context.Context, args ...string) (string, error) {
	if strings.TrimSpace(rootDir) == "" {
		return "", fmt.Errorf("rootDir is not set")
	}

	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		if strings.ContainsAny(arg, " \t\n\"'()[]<>*?!$") {
			arg = strconv.Quote(arg)
		}
		quoted = append(quoted, arg)
	}
	log.Printf("$ git %s", strings.Join(quoted, " "))

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = rootDir
	out, err := cmd.CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		if s == "" {
			return "", err
		}
		return "", fmt.Errorf("%w: %s", err, s)
	}
	return s, nil
}

func runGitWithStdin(ctx context.Context, stdin string, args ...string) (string, error) {
	if strings.TrimSpace(rootDir) == "" {
		return "", fmt.Errorf("rootDir is not set")
	}

	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		if strings.ContainsAny(arg, " \t\n\"'()[]<>*?!$") {
			arg = strconv.Quote(arg)
		}
		quoted = append(quoted, arg)
	}
	log.Printf("$ git %s", strings.Join(quoted, " "))

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = rootDir
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		if s == "" {
			return "", err
		}
		return "", fmt.Errorf("%w: %s", err, s)
	}
	return s, nil
}

func hasStagedContentMarkdownChanges(ctx context.Context) (bool, error) {
	if contentIgnoreSet == nil {
		return false, fmt.Errorf("content ignore set is not initialized")
	}

	out, err := runGit(ctx, "diff", "--cached", "--name-only", "-z")
	if err != nil {
		return false, fmt.Errorf("git diff --cached: %w", err)
	}
	for _, p := range splitNul(out) {
		p = filepath.ToSlash(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if isContentFilePath(p, contentIgnoreSet) {
			return true, nil
		}
	}
	return false, nil
}

func listChangedContentMarkdownPaths(ctx context.Context) ([]string, error) {
	if contentIgnoreSet == nil {
		return nil, fmt.Errorf("content ignore set is not initialized")
	}

	paths := make(map[string]struct{})

	// Unstaged (worktree) changes (includes deletions).
	if out, err := runGit(ctx, "diff", "--name-only", "-z"); err != nil {
		return nil, fmt.Errorf("git diff: %w", err)
	} else {
		for _, p := range splitNul(out) {
			paths[p] = struct{}{}
		}
	}

	// Staged (index) changes (includes deletions).
	if out, err := runGit(ctx, "diff", "--cached", "--name-only", "-z"); err != nil {
		return nil, fmt.Errorf("git diff --cached: %w", err)
	} else {
		for _, p := range splitNul(out) {
			paths[p] = struct{}{}
		}
	}

	// Untracked files (respects gitignore).
	if out, err := runGit(ctx, "ls-files", "-o", "--exclude-standard", "-z"); err != nil {
		return nil, fmt.Errorf("git ls-files -o: %w", err)
	} else {
		for _, p := range splitNul(out) {
			paths[p] = struct{}{}
		}
	}

	var res []string
	for p := range paths {
		p = filepath.ToSlash(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if isContentFilePath(p, contentIgnoreSet) {
			res = append(res, p)
		}
	}
	return res, nil
}

func gitCommitPaths(ctx context.Context, message string, paths []string) error {
	if strings.TrimSpace(message) == "" {
		return fmt.Errorf("empty commit message")
	}
	if len(paths) == 0 {
		return fmt.Errorf("no paths to commit")
	}

	// Prefer stdin-based pathspec to avoid argument length limits.
	var stdin strings.Builder
	for _, p := range paths {
		stdin.WriteString(p)
		stdin.WriteByte(0)
	}
	if _, err := runGitWithStdin(ctx, stdin.String(), "commit", "-m", message, "--pathspec-from-file=-", "--pathspec-file-nul"); err == nil {
		return nil
	} else if strings.Contains(err.Error(), "pathspec-from-file") || strings.Contains(err.Error(), "unknown option") {
		// Older git versions may not support --pathspec-from-file; fall back.
		if _, err := runGit(ctx, append([]string{"commit", "-m", message, "--"}, paths...)...); err != nil {
			return fmt.Errorf("git commit: %w", err)
		}
		return nil
	} else {
		return fmt.Errorf("git commit: %w", err)
	}
}

func splitNul(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, "\x00")
	out := parts[:0]
	for _, p := range parts {
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

func chunkStrings(in []string, size int) [][]string {
	if size <= 0 {
		size = 200
	}
	var out [][]string
	for len(in) > 0 {
		n := size
		if n > len(in) {
			n = len(in)
		}
		out = append(out, in[:n])
		in = in[n:]
	}
	return out
}
