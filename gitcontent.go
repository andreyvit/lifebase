package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
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
	return pullRebaseAndPush(ctx, res)
}

func syncAllChanges(ctx context.Context, message string) (repoCommitResult, error) {
	res, err := commitAllChanges(ctx, message)
	if err != nil {
		return repoCommitResult{}, err
	}
	return pullRebaseAndPush(ctx, res)
}

func pullRebaseAndPush(ctx context.Context, res repoCommitResult) (repoCommitResult, error) {
	if err := pullRebaseBeforePush(ctx); err != nil {
		return repoCommitResult{}, err
	}
	if sha, err := runGit(ctx, "rev-parse", "HEAD"); err != nil {
		return repoCommitResult{}, fmt.Errorf("git rev-parse HEAD after pull --rebase: %w", err)
	} else {
		res.CommitSHA = strings.TrimSpace(sha)
	}
	if _, err := runGit(ctx, "push"); err != nil {
		return repoCommitResult{}, fmt.Errorf("git push: %w", err)
	}
	return res, nil
}

func pullRebaseBeforePush(ctx context.Context) error {
	if _, err := runGit(ctx, "pull", "--rebase"); err != nil {
		conflict, inspectErr := rebaseConflictInProgress(ctx)
		if inspectErr != nil {
			return fmt.Errorf("git pull --rebase: %w; inspecting rebase state: %v", err, inspectErr)
		}
		if !conflict {
			return fmt.Errorf("git pull --rebase: %w", err)
		}

		if resolveErr := gitRebaseConflictResolver(ctx, err); resolveErr != nil {
			return fmt.Errorf("git pull --rebase conflicts: %w", resolveErr)
		}
		agentName := configuredAgent().DisplayName
		if conflict, err := rebaseConflictInProgress(ctx); err != nil {
			return fmt.Errorf("inspect rebase conflicts after %s: %w", agentName, err)
		} else if conflict {
			return fmt.Errorf("git pull --rebase conflicts remain after %s", agentName)
		}
		if inProgress, err := rebaseInProgress(ctx); err != nil {
			return fmt.Errorf("inspect rebase state after %s: %w", agentName, err)
		} else if inProgress {
			return fmt.Errorf("git pull --rebase still in progress after %s", agentName)
		}
	}
	return nil
}

var gitRebaseConflictResolver = resolveRebaseConflictsWithAgent

func resolveRebaseConflictsWithAgent(ctx context.Context, pullErr error) error {
	unmerged, err := unmergedRebasePaths(ctx)
	if err != nil {
		return fmt.Errorf("list unmerged paths: %w", err)
	}

	prompt := fmt.Sprintf(`Git pull --rebase stopped with merge conflicts while LifeBase was preparing to push an automatic commit.

Please resolve the repository's rebase conflicts and complete the rebase.

Rules:
- Inspect the conflicted files and preserve both the local LifeBase auto-commit changes and incoming upstream changes whenever possible.
- Stage resolved files.
- Run git rebase --continue after resolving the conflicts.
- If Git needs an editor during rebase continuation, run GIT_EDITOR=true git rebase --continue.
- Do not run git push.
- If you cannot resolve safely, explain why and leave the repo in a clear state.

Original git pull --rebase error:
%s

Current unmerged files:
%s`, pullErr, formatPathList(unmerged))

	spec := configuredAgent()
	if _, err := runAgentConflictResolver(ctx, prompt); err != nil {
		return fmt.Errorf("%s conflict resolution: %w", spec.Bin, err)
	}
	return nil
}

func runAgentConflictResolver(ctx context.Context, prompt string) (string, error) {
	spec := configuredAgent()
	log.Printf("Running %s CLI for Git rebase conflict...", spec.DisplayName)
	args := agentPromptArgs(spec.Kind, "", true, prompt)
	_, out, err := runAgentCommand(ctx, spec, args)
	return out, err
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

func rebaseConflictInProgress(ctx context.Context) (bool, error) {
	inProgress, err := rebaseInProgress(ctx)
	if err != nil {
		return false, err
	}
	if !inProgress {
		return false, nil
	}
	unmerged, err := unmergedRebasePaths(ctx)
	if err != nil {
		return false, err
	}
	return len(unmerged) > 0, nil
}

func rebaseInProgress(ctx context.Context) (bool, error) {
	for _, name := range []string{"rebase-merge", "rebase-apply"} {
		p, err := gitPath(ctx, name)
		if err != nil {
			return false, err
		}
		if _, err := os.Stat(p); err == nil {
			return true, nil
		} else if !os.IsNotExist(err) {
			return false, err
		}
	}
	return false, nil
}

func gitPath(ctx context.Context, name string) (string, error) {
	out, err := runGit(ctx, "rev-parse", "--git-path", name)
	if err != nil {
		return "", fmt.Errorf("git rev-parse --git-path %s: %w", name, err)
	}
	p := strings.TrimSpace(out)
	if p == "" {
		return "", fmt.Errorf("git path for %s is empty", name)
	}
	if filepath.IsAbs(p) {
		return p, nil
	}
	return filepath.Join(rootDir, p), nil
}

func unmergedRebasePaths(ctx context.Context) ([]string, error) {
	out, err := runGit(ctx, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, fmt.Errorf("git diff --name-only --diff-filter=U: %w", err)
	}
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			paths = append(paths, line)
		}
	}
	return paths, nil
}

func formatPathList(paths []string) string {
	if len(paths) == 0 {
		return "(none)"
	}
	var b strings.Builder
	for _, p := range paths {
		fmt.Fprintf(&b, "- %s\n", p)
	}
	return strings.TrimRight(b.String(), "\n")
}
