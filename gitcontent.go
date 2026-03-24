package main

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"strconv"
	"strings"
)

type repoCommitResult struct {
	DidCommit bool
	CommitSHA string
}

func commitAndPushAllChanges(ctx context.Context, message string) (repoCommitResult, error) {
	res, err := commitAllChanges(ctx, message)
	if err != nil {
		return repoCommitResult{}, err
	}
	if !res.DidCommit {
		return res, nil
	}
	if _, err := runGit(ctx, "push"); err != nil {
		return repoCommitResult{}, fmt.Errorf("git push: %w", err)
	}
	return res, nil
}

func commitAllChanges(ctx context.Context, message string) (repoCommitResult, error) {
	if strings.TrimSpace(message) == "" {
		return repoCommitResult{}, fmt.Errorf("empty commit message")
	}

	changed, err := hasRepoChanges(ctx)
	if err != nil {
		return repoCommitResult{}, err
	}
	if !changed {
		log.Printf("No changes to commit (%s)", message)
		return repoCommitResult{DidCommit: false}, nil
	}

	if _, err := runGit(ctx, "add", "-A", "."); err != nil {
		return repoCommitResult{}, fmt.Errorf("git add: %w", err)
	}

	staged, err := hasStagedChanges(ctx)
	if err != nil {
		return repoCommitResult{}, err
	}
	if !staged {
		log.Printf("No changes staged to commit (%s)", message)
		return repoCommitResult{DidCommit: false}, nil
	}

	if _, err := runGit(ctx, "commit", "-m", message); err != nil {
		return repoCommitResult{}, fmt.Errorf("git commit: %w", err)
	}
	sha, _ := runGit(ctx, "rev-parse", "HEAD")
	return repoCommitResult{DidCommit: true, CommitSHA: strings.TrimSpace(sha)}, nil
}

func bestEffortCommitAndPushAllChanges(ctx context.Context, message string) {
	if _, err := commitAndPushAllChanges(ctx, message); err != nil {
		log.Printf("Commit/push failed (%s): %v", message, err)
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

func hasRepoChanges(ctx context.Context) (bool, error) {
	out, err := runGit(ctx, "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("git status --porcelain: %w", err)
	}
	return strings.TrimSpace(out) != "", nil
}

func hasStagedChanges(ctx context.Context) (bool, error) {
	out, err := runGit(ctx, "diff", "--cached", "--name-only")
	if err != nil {
		return false, fmt.Errorf("git diff --cached --name-only: %w", err)
	}
	return strings.TrimSpace(out) != "", nil
}
