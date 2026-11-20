package common

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGitCommandInheritsEnvironmentWithoutDisclosingIt(t *testing.T) {
	t.Setenv("GIT_AUTO_SYNC_TEST_SECRET", "test-secret-not-for-logs")
	script := filepath.Join(t.TempDir(), "fake-git")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nif [ \"$1\" = ok ]; then printf '%s:%s' \"$GIT_AUTO_SYNC_TEST_SECRET\" \"$GIT_TERMINAL_PROMPT\"; else printf '%s' \"$GIT_AUTO_SYNC_TEST_SECRET\" >&2; exit 1; fi\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := RepoConfig{RepoPath: t.TempDir(), GitExec: script}
	out, err := GitCommand(cfg, []string{"ok"})
	if err != nil || out.String() != "test-secret-not-for-logs:0" {
		t.Fatalf("environment not inherited/noninteractive: %q %v", out.String(), err)
	}
	_, err = GitCommand(cfg, []string{"fail"})
	if err == nil || strings.Contains(err.Error(), "test-secret") {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestGitCommandTimeout(t *testing.T) {
	script := filepath.Join(t.TempDir(), "fake-git")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec sleep 20\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := RepoConfig{RepoPath: t.TempDir(), GitExec: script}
	// The zero-value timeout remains bounded; test the short configurable value.
	cfg.CommandTimeout = 50 * time.Millisecond
	start := time.Now()
	_, err := GitCommand(cfg, []string{"fetch"})
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("timeout not enforced: %v (%s)", err, time.Since(start))
	}
}

func TestPushDoesNotSilentlyAcceptMissingUpstream(t *testing.T) {
	cfg := PrepareFixture(t, "no_changes")
	if err := push(cfg); err == nil {
		t.Fatal("push accepted a checkout with no upstream")
	}
}

func TestGitCommandTimeoutStopsDescendantProcesses(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "unexpected-write")
	ready := filepath.Join(dir, "child-started")
	script := filepath.Join(dir, "fake-git")
	content := "#!/bin/sh\n(trap '' HUP; touch '" + strings.ReplaceAll(ready, "'", "'\\''") + "'; sleep 1.5; touch '" + strings.ReplaceAll(marker, "'", "'\\''") + "') &\nwait\n"
	if err := os.WriteFile(script, []byte(content), 0700); err != nil {
		t.Fatal(err)
	}
	_, err := GitCommand(RepoConfig{RepoPath: dir, GitExec: script, CommandTimeout: time.Second}, []string{"fetch"})
	if err == nil {
		t.Fatal("expected timeout")
	}
	if _, err := os.Stat(ready); err != nil {
		t.Fatal("descendant never started; test cannot verify cancellation", err)
	}
	time.Sleep(2 * time.Second)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("timed-out descendant kept writing after command returned")
	}
}
