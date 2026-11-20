package common

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

// SyncResult describes the checkout and the last observed upstream. Status is
// local/read-only; only a successful Sync verifies publication to the server.
type SyncResult struct {
	State       string `json:"state"`
	Head        string `json:"head"`
	RemoteHead  string `json:"remote_head"`
	Ahead       int    `json:"ahead"`
	Behind      int    `json:"behind"`
	LastSuccess string `json:"last_success"`
	Error       string `json:"error"`
	Remote      string `json:"remote"`
	Branch      string `json:"branch"`
	UpdatedAt   string `json:"updated_at"`
}

type syncRepo struct {
	cfg    RepoConfig
	gitDir string
}

func openSyncRepo(cfg RepoConfig) (syncRepo, error) {
	if cfg.RepoPath == "" {
		cfg.RepoPath = "."
	}
	out, err := GitCommand(cfg, []string{"rev-parse", "--show-toplevel"})
	if err != nil {
		return syncRepo{}, fmt.Errorf("not a working checkout: %w", err)
	}
	cfg.RepoPath = strings.TrimSpace(out.String())
	if cfg.GitExec == "" {
		configured, readErr := GitCommand(cfg, []string{"config", "--get", "auto-sync.exec"})
		if readErr == nil && strings.TrimSpace(configured.String()) != "" {
			cfg.GitExec = strings.TrimSpace(configured.String())
			if !filepath.IsAbs(cfg.GitExec) {
				cfg.GitExec = filepath.Join(cfg.RepoPath, cfg.GitExec)
			}
			if _, err := os.Stat(cfg.GitExec); err != nil {
				return syncRepo{}, fmt.Errorf("configured Git executable is unavailable: %w", err)
			}
		}
	}
	out, err = GitCommand(cfg, []string{"rev-parse", "--absolute-git-dir"})
	if err != nil {
		return syncRepo{}, err
	}
	return syncRepo{cfg: cfg, gitDir: strings.TrimSpace(out.String())}, nil
}

func (r syncRepo) git(args ...string) (string, error) {
	out, err := GitCommand(r.cfg, args)
	return strings.TrimSpace(out.String()), err
}

func (r syncRepo) lock() (*flock.Flock, error) {
	// The file is permanent; unlinking it would let two processes lock different
	// inodes. flock is released by the OS on close, exit or process death.
	lock := flock.New(filepath.Join(r.gitDir, "git-auto-sync.lock"), flock.SetPermissions(0600))
	ok, err := lock.TryLock()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errSyncBusy
	}
	return lock, nil
}

var errSyncBusy = errors.New("checkout is locked by another publisher or sync")
var errSyncConflict = errors.New("integration conflict; local commit retained, resolve manually before syncing again")

func (r syncRepo) previous() SyncResult {
	var result SyncResult
	data, err := os.ReadFile(filepath.Join(r.gitDir, "git-auto-sync-status.json"))
	if err == nil {
		_ = json.Unmarshal(data, &result)
	}
	return result
}

func (r syncRepo) save(result SyncResult) error {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(r.gitDir, "git-auto-sync-status-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(r.gitDir, "git-auto-sync-status.json"))
}

func (r *syncRepo) validate() error {
	branch, err := r.git("symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return errors.New("detached HEAD or missing branch")
	}
	if r.cfg.Branch == "" {
		r.cfg.Branch = branch
	}
	if branch != r.cfg.Branch {
		return fmt.Errorf("expected branch %q; checkout is on %q", r.cfg.Branch, branch)
	}
	if _, err = r.git("check-ref-format", "refs/heads/"+r.cfg.Branch); err != nil {
		return errors.New("invalid expected branch")
	}
	remote, err := r.git("config", "--get", "branch."+branch+".remote")
	if err != nil || remote == "" || remote == "." {
		return errors.New("branch has no remote upstream; configure it explicitly before syncing")
	}
	if r.cfg.Remote == "" {
		r.cfg.Remote = remote
	}
	if remote != r.cfg.Remote {
		return fmt.Errorf("upstream remote %q does not match expected %q", remote, r.cfg.Remote)
	}
	if strings.HasPrefix(remote, "-") {
		return errors.New("invalid remote name")
	}
	merge, err := r.git("config", "--get", "branch."+branch+".merge")
	if err != nil || merge != "refs/heads/"+r.cfg.Branch {
		return errors.New("upstream branch does not match expected branch")
	}
	if _, err = r.git("remote", "get-url", remote); err != nil {
		return errors.New("expected remote is not configured")
	}
	if r.cfg.Strategy == "" {
		r.cfg.Strategy = "merge"
	}
	if r.cfg.Strategy != "merge" {
		return errors.New("only the merge strategy is supported")
	}
	return nil
}

func (r syncRepo) inProgress() bool {
	for _, name := range []string{"MERGE_HEAD", "rebase-merge", "rebase-apply", "CHERRY_PICK_HEAD", "REVERT_HEAD"} {
		if _, err := os.Stat(filepath.Join(r.gitDir, name)); err == nil {
			return true
		}
	}
	u, err := r.git("ls-files", "--unmerged")
	return err != nil || u != ""
}

func (r syncRepo) inspect(previous SyncResult) SyncResult {
	result := SyncResult{State: "pending", LastSuccess: previous.LastSuccess, Remote: r.cfg.Remote, Branch: r.cfg.Branch, UpdatedAt: previous.UpdatedAt}
	result.Head, _ = r.git("rev-parse", "--verify", "HEAD")
	result.RemoteHead, _ = r.git("rev-parse", "--verify", "refs/remotes/"+r.cfg.Remote+"/"+r.cfg.Branch)
	if result.Head != "" && result.RemoteHead != "" {
		counts, err := r.git("rev-list", "--left-right", "--count", "HEAD..."+result.RemoteHead)
		parts := strings.Fields(counts)
		if err == nil && len(parts) == 2 {
			result.Ahead, _ = strconv.Atoi(parts[0])
			result.Behind, _ = strconv.Atoi(parts[1])
		}
	}
	dirt, err := r.git("status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		result.State = "error"
		result.Error = err.Error()
		return result
	}
	if r.inProgress() {
		result.State = "conflict"
		result.Error = "checkout has an unfinished Git operation or unmerged files"
		return result
	}
	if previous.State == "error" || previous.State == "conflict" {
		result.State = previous.State
		result.Error = previous.Error
	}
	if dirt != "" || result.Ahead != 0 || result.Behind != 0 || result.Head != previous.Head || result.RemoteHead != previous.RemoteHead || result.Remote != previous.Remote || result.Branch != previous.Branch {
		if result.State != "conflict" && result.State != "error" {
			result.State = "pending"
		}
		return result
	}
	if previous.State == "synced" && previous.LastSuccess != "" && result.Head != "" && result.Head == result.RemoteHead {
		result.State = "synced"
	}
	return result
}

// Status acquires the same lock as Sync and cooperative publishers, then reads
// fresh HEAD, tracking refs and worktree dirt. It performs no network access.
func Status(cfg RepoConfig) SyncResult {
	r, err := openSyncRepo(cfg)
	if err != nil {
		return SyncResult{State: "error", Error: err.Error()}
	}
	lock, err := r.lock()
	if err != nil {
		return lockFailure(err)
	}
	defer lock.Close()
	previous := r.previous()
	if r.cfg.Branch == "" {
		r.cfg.Branch = previous.Branch
	}
	if r.cfg.Remote == "" {
		r.cfg.Remote = previous.Remote
	}
	if err = r.validate(); err != nil {
		return SyncResult{State: "error", Error: err.Error(), LastSuccess: previous.LastSuccess}
	}
	return r.inspect(previous)
}

func lockFailure(err error) SyncResult {
	state := "error"
	if errors.Is(err, errSyncBusy) {
		state = "busy"
	}
	return SyncResult{State: state, Error: err.Error()}
}

// Sync commits local changes, fetches and merges the configured upstream and
// explicitly pushes HEAD to that branch. No histories are rewritten.
func Sync(cfg RepoConfig) SyncResult {
	r, err := openSyncRepo(cfg)
	if err != nil {
		return SyncResult{State: "error", Error: err.Error()}
	}
	lock, err := r.lock()
	if err != nil {
		return lockFailure(err)
	}
	defer lock.Close()
	previous := r.previous()
	if r.cfg.Branch == "" {
		r.cfg.Branch = previous.Branch
	}
	if r.cfg.Remote == "" {
		r.cfg.Remote = previous.Remote
	}
	// Signal cancellation must kill and reap the active Git process group before
	// this deferred lock close runs. Cleanup then gets its own short context so
	// cancellation cannot prevent merge abort or durable error reporting.
	var cleanupCancel context.CancelFunc
	defer func() {
		if cleanupCancel != nil {
			cleanupCancel()
		}
	}()
	prepareCleanup := func(force bool) {
		if cleanupCancel == nil && (force || (r.cfg.Context != nil && r.cfg.Context.Err() != nil)) {
			r.cfg.Context, cleanupCancel = context.WithTimeout(context.Background(), 5*time.Second)
		}
	}
	finish := func(syncErr error) SyncResult {
		prepareCleanup(false)
		result := r.inspect(previous)
		if syncErr != nil {
			result.State = "error"
			if errors.Is(syncErr, errSyncConflict) {
				result.State = "conflict"
			}
			result.Error = syncErr.Error()
		} else if result.Head == result.RemoteHead && result.Head != "" && result.Ahead == 0 && result.Behind == 0 {
			dirt, e := r.git("status", "--porcelain=v1", "--untracked-files=all")
			if e != nil {
				result.State = "error"
				result.Error = e.Error()
			} else if dirt != "" {
				result.State = "pending"
				result.Error = "checkout changed during sync; another sync is needed"
			} else {
				result.State = "synced"
				result.Error = ""
				result.LastSuccess = time.Now().UTC().Format(time.RFC3339Nano)
			}
		}
		result.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if err := r.save(result); err != nil {
			result.State = "error"
			result.Error = fmt.Sprintf("cannot persist sync status: %v", err)
		}
		return result
	}
	if err = r.validate(); err != nil {
		return finish(err)
	}
	if r.inProgress() {
		return finish(errSyncConflict)
	}
	if err = ensureGitAuthor(r.cfg); err != nil {
		return finish(err)
	}
	if _, err = r.git("add", "--all", "--", "."); err != nil {
		return finish(err)
	}
	staged, err := r.git("diff", "--cached", "--name-only")
	if err != nil {
		return finish(err)
	}
	if staged != "" {
		if _, err = r.git("commit", "-m", "Auto-sync local changes"); err != nil {
			return finish(err)
		}
	}
	remoteRef := "refs/remotes/" + r.cfg.Remote + "/" + r.cfg.Branch
	for attempt := 0; attempt < 3; attempt++ {
		// Force-updating a local tracking ref is intentional; the push itself is
		// always a normal fast-forward push, even if the server was rewritten.
		refspec := "+refs/heads/" + r.cfg.Branch + ":" + remoteRef
		if _, err = r.git("fetch", "--no-tags", r.cfg.Remote, refspec); err != nil {
			return finish(err)
		}
		// Persist MERGE_HEAD before running potentially slow commit hooks. Native
		// automatic merge commits invoke hooks before writing that metadata,
		// which would leave no safe abort path if cancellation interrupts a hook.
		_, err = r.git("merge", "--no-commit", "--no-edit", "--no-stat", "--no-autostash", remoteRef)
		if err == nil {
			if _, statErr := os.Stat(filepath.Join(r.gitDir, "MERGE_HEAD")); statErr == nil {
				_, err = r.git("commit", "--no-edit")
			}
		}
		if err != nil {
			if _, statErr := os.Stat(filepath.Join(r.gitDir, "MERGE_HEAD")); statErr == nil {
				prepareCleanup(true)
				if _, abortErr := r.git("merge", "--abort"); abortErr != nil {
					return finish(fmt.Errorf("%w; merge abort failed, manual recovery needed: %v", errSyncConflict, abortErr))
				}
				return finish(errSyncConflict)
			}
			return finish(err)
		}
		if _, err = r.git("push", r.cfg.Remote, "HEAD:refs/heads/"+r.cfg.Branch); err == nil {
			// A custom remote.fetch map may update a different tracking ref on
			// push. Record this successful explicit publication consistently.
			if _, err = r.git("update-ref", remoteRef, "HEAD"); err != nil {
				return finish(err)
			}
			return finish(nil)
		}
	}
	return finish(fmt.Errorf("push failed after 3 fetch/merge attempts: %w", err))
}
