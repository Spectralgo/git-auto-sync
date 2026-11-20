package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Spectralgo/git-auto-sync/common"
	"github.com/gofrs/flock"
)

var testCLI string

func TestMain(m *testing.M) {
	if path := os.Getenv("GIT_AUTO_SYNC_LOCK_HELPER"); path != "" {
		lock := flock.New(path)
		if ok, err := lock.TryLock(); !ok || err != nil {
			os.Exit(2)
		}
		fmt.Println("locked")
		for {
			time.Sleep(time.Hour)
		}
	}
	dir, err := os.MkdirTemp("", "git-auto-sync-tests-")
	if err != nil {
		panic(err)
	}
	testCLI = filepath.Join(dir, "git-auto-sync")
	build := exec.Command("go", "build", "-o", testCLI, ".")
	if out, err := build.CombinedOutput(); err != nil {
		panic(fmt.Sprintf("build: %v: %s", err, out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestSyncConflictRetainsLocalDocuments(t *testing.T) {
	_, a, b := fixture(t)
	writeNote(t, a, "shared.md", "published from a\n")
	syncRepo(t, a, true)
	writeNote(t, b, "shared.md", "local from b\n")
	got := syncRepo(t, b, false)
	if got.State != "conflict" || got.Ahead != 1 || got.Behind != 1 {
		t.Fatalf("not a retained divergence: %+v", got)
	}
	content, err := os.ReadFile(filepath.Join(b, "shared.md"))
	if err != nil || string(content) != "local from b\n" {
		t.Fatalf("local document lost: %q %v", content, err)
	}
	if runGit(t, b, "status", "--porcelain") != "" {
		t.Fatal("failed merge left unresolved files")
	}
	if _, err := os.Stat(filepath.Join(b, ".git", "MERGE_HEAD")); !os.IsNotExist(err) {
		t.Fatal("merge was not aborted")
	}
	if runGit(t, a, "rev-parse", "HEAD") != runGit(t, b, "rev-parse", "refs/remotes/origin/main") {
		t.Fatal("remote history changed")
	}
	if got := invoke(t, false, "status", "--repo", b, "--json"); got.State != "conflict" {
		t.Fatalf("conflict lost on next process: %+v", got)
	}
}

func TestSyncRejectsUnexpectedCheckoutBeforeCommitting(t *testing.T) {
	for _, kind := range []string{"wrong branch", "detached", "missing upstream", "wrong remote", "wrong upstream branch", "missing remote"} {
		t.Run(kind, func(t *testing.T) {
			_, a, _ := fixture(t)
			original := runGit(t, a, "rev-parse", "HEAD")
			switch kind {
			case "wrong branch":
				runGit(t, a, "switch", "-c", "other")
			case "detached":
				runGit(t, a, "switch", "--detach")
			case "missing upstream":
				runGit(t, a, "branch", "--unset-upstream")
			case "wrong remote":
				runGit(t, a, "config", "branch.main.remote", "other")
			case "wrong upstream branch":
				runGit(t, a, "config", "branch.main.merge", "refs/heads/other")
			case "missing remote":
				runGit(t, a, "remote", "remove", "origin")
			}
			writeNote(t, a, "must remain uncommitted.md", "retained\n")
			if got := syncRepo(t, a, false); got.State != "error" || got.Error == "" {
				t.Fatalf("unexpected acceptance: %+v", got)
			}
			if runGit(t, a, "rev-parse", "HEAD") != original {
				t.Fatal("committed before validating checkout")
			}
			if got := invoke(t, false, "status", "--repo", a, "--json"); got.State != "error" {
				t.Fatalf("invalid checkout marked ready: %+v", got)
			}
		})
	}
}

func TestSyncOfflineRecoveryAndPersistedStatus(t *testing.T) {
	remote, a, _ := fixture(t)
	first := syncRepo(t, a, true)
	if err := os.Rename(remote, remote+".offline"); err != nil {
		t.Fatal(err)
	}
	writeNote(t, a, "offline.md", "written offline\n")
	failed := syncRepo(t, a, false)
	if failed.State != "error" || failed.Ahead != 1 || failed.LastSuccess != first.LastSuccess {
		t.Fatalf("offline failure not recorded: %+v", failed)
	}
	if got := invoke(t, false, "status", "--repo", a, "--json"); got.State != "error" || got.LastSuccess != first.LastSuccess {
		t.Fatalf("lost durable failure: %+v", got)
	}
	if err := os.Rename(remote+".offline", remote); err != nil {
		t.Fatal(err)
	}
	syncRepo(t, a, true)
	if _, err := os.Stat(filepath.Join(a, ".git", "git-auto-sync-status.json")); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(runGit(t, a, "ls-files"), "git-auto-sync") {
		t.Fatal("sync metadata was tracked")
	}
}

func TestSyncCheckoutLockExcludesProcessesAndSurvivesDeath(t *testing.T) {
	_, a, _ := fixture(t)
	syncRepo(t, a, true)
	helper := exec.Command(os.Args[0], "-test.run=^$")
	helper.Env = append(os.Environ(), "GIT_AUTO_SYNC_LOCK_HELPER="+filepath.Join(a, ".git", "git-auto-sync.lock"))
	pipe, err := helper.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = helper.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = helper.Process.Kill(); _ = helper.Wait() })
	line, err := bufio.NewReader(pipe).ReadString('\n')
	if err != nil || line != "locked\n" {
		t.Fatalf("helper did not lock: %q %v", line, err)
	}
	writeNote(t, a, "pending.md", "retained while locked\n")
	for _, command := range []string{"sync", "status"} {
		if got := invoke(t, false, command, "--repo", a, "--json"); got.State != "busy" {
			t.Fatalf("lock did not exclude %s: %+v", command, got)
		}
	}
	if err = helper.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = helper.Wait()
	syncRepo(t, a, true)
}

func TestSyncRetriesConcurrentPush(t *testing.T) {
	_, a, b := fixture(t)
	writeNote(t, a, "a.md", "from a\n")
	writeNote(t, b, "b.md", "from b during push\n")
	runGit(t, b, "add", "--all")
	runGit(t, b, "commit", "-m", "race winner")
	// A pre-push hook advances the remote after a fetched its previous head.
	hook := "#!/bin/sh\nif mkdir \"$GIT_DIR/race-once\" 2>/dev/null; then\n git -C '" + strings.ReplaceAll(b, "'", "'\\''") + "' push origin HEAD:refs/heads/main || exit 1\nfi\n"
	// GIT_DIR is not guaranteed in hooks, so use a fully qualified marker.
	hook = strings.ReplaceAll(hook, "$GIT_DIR/race-once", filepath.Join(a, ".git", "race-once"))
	if err := os.WriteFile(filepath.Join(a, ".git", "hooks", "pre-push"), []byte(hook), 0700); err != nil {
		t.Fatal(err)
	}
	syncRepo(t, a, true)
	if _, err := os.Stat(filepath.Join(a, "b.md")); err != nil {
		t.Fatal("concurrent note was not merged", err)
	}
	syncRepo(t, b, true)
	if _, err := os.Stat(filepath.Join(b, "a.md")); err != nil {
		t.Fatal("local note was not published", err)
	}
}

func TestStatusRequiresSuccessfulSyncAndDetectsFetchedDivergence(t *testing.T) {
	_, a, b := fixture(t)
	if got := invoke(t, false, "status", "--repo", a, "--json"); got.State != "pending" {
		t.Fatalf("unverified checkout ready: %+v", got)
	}
	syncRepo(t, a, true)
	writeNote(t, b, "new.md", "new remote commit\n")
	syncRepo(t, b, true)
	runGit(t, a, "fetch", "origin")
	if got := invoke(t, false, "status", "--repo", a, "--json"); got.State != "pending" || got.Behind != 1 {
		t.Fatalf("fetched divergence missed: %+v", got)
	}
}

func TestSyncWithoutFlagsRetainsPreviouslyExpectedBranch(t *testing.T) {
	_, a, _ := fixture(t)
	syncRepo(t, a, true)
	runGit(t, a, "switch", "-c", "other")
	runGit(t, a, "push", "-u", "origin", "other")
	before := runGit(t, a, "rev-parse", "HEAD")
	writeNote(t, a, "unpublished.md", "do not commit on other branch\n")
	if got := invoke(t, false, "sync", "--repo", a, "--json"); got.State != "error" {
		t.Fatalf("expected branch was forgotten: %+v", got)
	}
	if runGit(t, a, "rev-parse", "HEAD") != before {
		t.Fatal("wrong-branch edit was committed")
	}
}

func TestLinkedWorktreeUsesActualGitDirectory(t *testing.T) {
	_, a, _ := fixture(t)
	linked := filepath.Join(filepath.Dir(a), "linked checkout")
	runGit(t, a, "worktree", "add", "-b", "linked", linked)
	runGit(t, linked, "push", "-u", "origin", "linked")
	if _, err := isValidGitRepo(linked); err != nil {
		t.Fatalf("linked worktree rejected: %v", err)
	}
	if _, err := common.NewRepoConfig(linked); err != nil {
		t.Fatalf("watch configuration rejects linked worktree: %v", err)
	}
	writeNote(t, linked, "linked.md", "linked document\n")
	invoke(t, true, "sync", "--repo", linked, "--remote", "origin", "--branch", "linked", "--json")
	gitDir := runGit(t, linked, "rev-parse", "--absolute-git-dir")
	for _, name := range []string{"git-auto-sync.lock", "git-auto-sync-status.json"} {
		if _, err := os.Stat(filepath.Join(gitDir, name)); err != nil {
			t.Fatal("metadata missing from actual Git directory", err)
		}
	}
	invoke(t, true, "status", "--repo", linked, "--json")
}

func TestSyncPreservesConfiguredGitExecutable(t *testing.T) {
	_, a, _ := fixture(t)
	gitExec, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(a, ".git", "git-wrapper")
	marker := filepath.Join(a, ".git", "wrapper-used")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	content := "#!/bin/sh\ntouch " + quote(marker) + "\nexec " + quote(gitExec) + " \"$@\"\n"
	if err := os.WriteFile(script, []byte(content), 0700); err != nil {
		t.Fatal(err)
	}
	runGit(t, a, "config", "auto-sync.exec", script)
	syncRepo(t, a, true)
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("auto-sync.exec was ignored", err)
	}
}

func TestSyncTracksExplicitBranchWithCustomFetchConfiguration(t *testing.T) {
	_, a, _ := fixture(t)
	runGit(t, a, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/custom/*")
	writeNote(t, a, "explicit.md", "publish the requested branch\n")
	syncRepo(t, a, true)
	invoke(t, true, "status", "--repo", a, "--json")
}

func TestSyncPreservesRepositoryScopedSSHTransport(t *testing.T) {
	remote, a, _ := fixture(t)
	for _, key := range []string{"GIT_SSH_COMMAND", "GIT_SSH"} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	// Any accidental fallback to system ssh must fail locally, without network.
	stubDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stubDir, "ssh"), []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	gitExec, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	transport := filepath.Join(a, ".git", "scoped-ssh")
	marker := filepath.Join(a, ".git", "scoped-ssh-used")
	content := "#!/bin/sh\ntouch " + quote(marker) + "\ncase \"$*\" in\n*git-upload-pack*) exec " + quote(gitExec) + " upload-pack " + quote(remote) + ";;\n*git-receive-pack*) exec " + quote(gitExec) + " receive-pack " + quote(remote) + ";;\n*) exit 1;;\nesac\n"
	if err := os.WriteFile(transport, []byte(content), 0700); err != nil {
		t.Fatal(err)
	}
	runGit(t, a, "config", "core.sshCommand", quote(transport))
	runGit(t, a, "remote", "set-url", "origin", "ssh://git-auto-sync-test.invalid/notes.git")
	writeNote(t, a, "scoped.md", "publish through repo SSH configuration\n")
	syncRepo(t, a, true)
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("repository-scoped SSH transport was not used", err)
	}
}

func TestTerminatedSyncStopsRealGitHookBeforeReleasingLock(t *testing.T) {
	_, a, _ := fixture(t)
	original := runGit(t, a, "rev-parse", "HEAD")
	started := filepath.Join(a, ".git", "hook-started")
	lateWrite := filepath.Join(a, ".git", "hook-finished")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	hook := "#!/bin/sh\ntouch " + quote(started) + "\nsleep 2\ntouch " + quote(lateWrite) + "\n"
	if err := os.WriteFile(filepath.Join(a, ".git", "hooks", "pre-commit"), []byte(hook), 0700); err != nil {
		t.Fatal(err)
	}
	writeNote(t, a, "interrupted.md", "retain complete local document\n")
	cmd := exec.Command(testCLI, "sync", "--repo", a, "--remote", "origin", "--branch", "main", "--timeout", "20s", "--json")
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	deadline := time.Now().Add(8 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("real Git hook did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("interrupted sync reported success")
	}
	lock := flock.New(filepath.Join(a, ".git", "git-auto-sync.lock"))
	if ok, err := lock.TryLock(); !ok || err != nil {
		t.Fatalf("lock not released after cleanup: %v", err)
	}
	defer lock.Close()
	time.Sleep(2500 * time.Millisecond)
	if _, err := os.Stat(lateWrite); !os.IsNotExist(err) {
		t.Fatal("Git hook kept mutating after engine exited and lock became available")
	}
	if head := runGit(t, a, "rev-parse", "HEAD"); head != original {
		t.Fatal("Git committed after engine exited")
	}
	content, err := os.ReadFile(filepath.Join(a, "interrupted.md"))
	if err != nil || string(content) != "retain complete local document\n" {
		t.Fatalf("interrupted document lost: %q %v", content, err)
	}
	var result syncResult
	if err := json.Unmarshal([]byte(stdout.String()), &result); err != nil || result.State != "error" {
		t.Fatalf("interrupted CLI did not report structured error: %q %s %v", stdout.String(), stderr.String(), err)
	}
}

func TestTerminatedSyncAbortsMergeUsingFreshCleanupContext(t *testing.T) {
	_, a, b := fixture(t)
	writeNote(t, a, "from-a.md", "published a\n")
	syncRepo(t, a, true)
	writeNote(t, b, "from-b.md", "local b\n")
	started := filepath.Join(b, ".git", "merge-hook-started")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	// Only pause the integration commit, after merge --no-commit has persisted
	// abort metadata. The initial local-note commit must finish normally.
	hook := "#!/bin/sh\nif test -f " + quote(filepath.Join(b, ".git", "MERGE_HEAD")) + "; then\ntouch " + quote(started) + "\nsleep 2\nfi\n"
	if err := os.WriteFile(filepath.Join(b, ".git", "hooks", "pre-commit"), []byte(hook), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(testCLI, "sync", "--repo", b, "--remote", "origin", "--branch", "main", "--timeout", "20s", "--json")
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	deadline := time.Now().Add(8 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("real merge hook did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("interrupted merge reported success")
	}
	if _, err := os.Stat(filepath.Join(b, ".git", "MERGE_HEAD")); !os.IsNotExist(err) {
		t.Fatal("interrupted merge was not aborted")
	}
	if _, err := os.Stat(filepath.Join(b, "from-a.md")); !os.IsNotExist(err) {
		t.Fatal("aborted merge did not restore the local tree")
	}
	content, err := os.ReadFile(filepath.Join(b, "from-b.md"))
	if err != nil || string(content) != "local b\n" {
		t.Fatalf("local document lost: %q %v", content, err)
	}
	var result syncResult
	if err := json.Unmarshal([]byte(stdout.String()), &result); err != nil || (result.State != "conflict" && result.State != "error") {
		t.Fatalf("invalid canceled merge result: %q %s %v", stdout.String(), stderr.String(), err)
	}
}

func TestExplicitRepoCannotBeRedirectedByInheritedGitEnvironment(t *testing.T) {
	_, a, b := fixture(t)
	originalB := runGit(t, b, "rev-parse", "HEAD")
	writeNote(t, a, "expected.md", "commit only expected checkout\n")
	writeNote(t, b, "other.md", "must remain uncommitted\n")
	t.Setenv("GIT_DIR", filepath.Join(b, ".git"))
	t.Setenv("GIT_WORK_TREE", b)
	syncRepo(t, a, true)
	if _, err := os.Stat(filepath.Join(a, ".git", "git-auto-sync-status.json")); err != nil {
		t.Fatal("explicit checkout was redirected by inherited Git environment", err)
	}
	if err := os.Unsetenv("GIT_DIR"); err != nil {
		t.Fatal(err)
	}
	if err := os.Unsetenv("GIT_WORK_TREE"); err != nil {
		t.Fatal(err)
	}
	if runGit(t, b, "rev-parse", "HEAD") != originalB {
		t.Fatal("committed changes in the wrong checkout")
	}
	if runGit(t, a, "status", "--porcelain") != "" {
		t.Fatal("expected checkout was not synced")
	}
}

func TestStatusDoesNotReuseSuccessFromAnotherRemote(t *testing.T) {
	remote, a, _ := fixture(t)
	syncRepo(t, a, true)
	runGit(t, a, "remote", "add", "another", remote)
	runGit(t, a, "fetch", "another")
	runGit(t, a, "config", "branch.main.remote", "another")
	if got := invoke(t, false, "status", "--repo", a, "--remote", "another", "--branch", "main", "--json"); got.State != "pending" {
		t.Fatalf("success from old target reused: %+v", got)
	}
}

type syncResult struct {
	State       string `json:"state"`
	Head        string `json:"head"`
	RemoteHead  string `json:"remote_head"`
	Ahead       int    `json:"ahead"`
	Behind      int    `json:"behind"`
	LastSuccess string `json:"last_success"`
	Error       string `json:"error"`
}

func runGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func fixture(t *testing.T) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	a, b := filepath.Join(dir, "clone a"), filepath.Join(dir, "clone b")
	runGit(t, dir, "init", "--bare", "--initial-branch=main", remote)
	runGit(t, dir, "clone", remote, a)
	configure := func(repo string) {
		runGit(t, repo, "config", "user.name", "Test Writer")
		runGit(t, repo, "config", "user.email", "test@example.invalid")
		runGit(t, repo, "config", "commit.gpgsign", "false")
	}
	configure(a)
	writeNote(t, a, "shared.md", "base\n")
	runGit(t, a, "add", "--all")
	runGit(t, a, "commit", "-m", "initial")
	runGit(t, a, "push", "-u", "origin", "main")
	runGit(t, dir, "clone", remote, b)
	configure(b)
	return remote, a, b
}

func writeNote(t *testing.T, repo, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func invoke(t *testing.T, ready bool, args ...string) syncResult {
	t.Helper()
	cmd := exec.Command(testCLI, args...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if (err == nil) != ready {
		t.Fatalf("CLI %v readiness: err=%v stdout=%s stderr=%s", args, err, out, stderr.String())
	}
	var result syncResult
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("CLI stdout must be one JSON value: %v, got %q; stderr=%s", err, out, stderr.String())
	}
	if ready && (result.State != "synced" || result.Head == "" || result.Head != result.RemoteHead || result.LastSuccess == "" || result.Ahead != 0 || result.Behind != 0 || result.Error != "") {
		t.Fatalf("invalid synced result: %+v", result)
	}
	return result
}

func syncRepo(t *testing.T, repo string, ready bool) syncResult {
	t.Helper()
	return invoke(t, ready, "sync", "--repo", repo, "--remote", "origin", "--branch", "main", "--strategy", "merge", "--json")
}

func TestSyncIndependentNotesAndFreshStatus(t *testing.T) {
	_, a, b := fixture(t)
	writeNote(t, a, "a note.md", "from a\n")
	writeNote(t, b, "b note.md", "from b\n")
	syncRepo(t, a, true)
	syncRepo(t, b, true)
	syncRepo(t, a, true)
	for _, repo := range []string{a, b} {
		for _, name := range []string{"a note.md", "b note.md"} {
			if _, err := os.Stat(filepath.Join(repo, name)); err != nil {
				t.Fatal(err)
			}
		}
		invoke(t, true, "status", "--repo", repo, "--json")
	}
	writeNote(t, a, "fresh.md", "not yet committed\n")
	if got := invoke(t, false, "status", "--repo", a, "--json"); got.State != "pending" {
		t.Fatalf("fresh edits invisible: %+v", got)
	}
	syncRepo(t, a, true)
	writeNote(t, a, "manual.md", "manual commit\n")
	runGit(t, a, "add", "--all")
	runGit(t, a, "commit", "-m", "manual")
	if got := invoke(t, false, "status", "--repo", a, "--json"); got.State != "pending" || got.Ahead != 1 {
		t.Fatalf("fresh divergence invisible: %+v", got)
	}
}
