package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommitAndPushPullRebaseInvokesResolverForConflicts(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	local := filepath.Join(root, "local")
	peer := filepath.Join(root, "peer")

	runTestCommand(t, "", "git", "init", "--bare", remote)
	runTestCommand(t, "", "git", "clone", remote, local)
	configureTestGitUser(t, local)
	runTestCommand(t, local, "git", "checkout", "-b", "main")
	writeTestFile(t, filepath.Join(local, "note.md"), "base\n")
	runTestCommand(t, local, "git", "add", ".")
	runTestCommand(t, local, "git", "commit", "-m", "initial")
	runTestCommand(t, local, "git", "push", "-u", "origin", "main")

	runTestCommand(t, "", "git", "clone", remote, peer)
	configureTestGitUser(t, peer)
	writeTestFile(t, filepath.Join(peer, "note.md"), "remote\n")
	runTestCommand(t, peer, "git", "commit", "-am", "remote")
	runTestCommand(t, peer, "git", "push")

	oldRootDir := rootDir
	oldResolver := gitRebaseConflictResolver
	rootDir = local
	resolverCalled := false
	gitRebaseConflictResolver = func(ctx context.Context, pullErr error) error {
		resolverCalled = true
		conflict, err := rebaseConflictInProgress(ctx)
		if err != nil {
			return err
		}
		if !conflict {
			t.Fatal("resolver called without a rebase conflict in progress")
		}
		paths, err := unmergedRebasePaths(ctx)
		if err != nil {
			return err
		}
		if len(paths) != 1 || paths[0] != "note.md" {
			t.Fatalf("unmerged paths = %#v, want [note.md]", paths)
		}
		writeTestFile(t, filepath.Join(local, "note.md"), "remote\nlocal\n")
		if _, err := runGit(ctx, "add", "note.md"); err != nil {
			return err
		}
		runTestCommand(t, local, "git", "rebase", "--continue")
		return nil
	}
	t.Cleanup(func() {
		rootDir = oldRootDir
		gitRebaseConflictResolver = oldResolver
	})

	writeTestFile(t, filepath.Join(local, "note.md"), "local\n")
	res, err := commitAndPushAllChanges(ctx, "local")
	if err != nil {
		t.Fatalf("commitAndPushAllChanges: %v", err)
	}
	if !res.DidCommit {
		t.Fatal("DidCommit = false, want true")
	}
	if !resolverCalled {
		t.Fatal("resolver was not called")
	}
	if inProgress, err := rebaseInProgress(ctx); err != nil {
		t.Fatalf("rebaseInProgress: %v", err)
	} else if inProgress {
		t.Fatal("rebase still in progress")
	}

	runTestCommand(t, peer, "git", "pull", "--ff-only")
	if got := readTestFile(t, filepath.Join(peer, "note.md")); got != "remote\nlocal\n" {
		t.Fatalf("pushed note.md = %q, want resolved contents", got)
	}
	remoteHead := runTestCommand(t, peer, "git", "rev-parse", "HEAD")
	if res.CommitSHA != remoteHead {
		t.Fatalf("CommitSHA = %s, want rebased HEAD %s", res.CommitSHA, remoteHead)
	}
}

func configureTestGitUser(t *testing.T, dir string) {
	t.Helper()
	runTestCommand(t, dir, "git", "config", "user.name", "LifeBase Test")
	runTestCommand(t, dir, "git", "config", "user.email", "lifebase-test@example.com")
}

func runTestCommand(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_EDITOR=true", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, s)
	}
	return s
}

func writeTestFile(t *testing.T, path string, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o666); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
