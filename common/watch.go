package common

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rjeczalik/notify"
	"github.com/ztrue/tracerr"
)

type RepoConfig struct {
	Context        context.Context
	RepoPath       string
	PollInterval   time.Duration
	FSLag          time.Duration
	GitExec        string
	Env            []string
	Remote         string
	Branch         string
	Strategy       string
	CommandTimeout time.Duration
}

type AwakeNotifier interface {
	Start(chan bool) error
}

func NewRepoConfig(repoPath string) (RepoConfig, error) {
	r, err := openSyncRepo(RepoConfig{RepoPath: repoPath})
	if err != nil {
		return RepoConfig{}, err
	}
	cfg := r.cfg
	cfg.PollInterval = 10 * time.Minute
	cfg.FSLag = time.Second
	cfg.Strategy = "merge"
	secondsStr, err := r.git("config", "--get", "auto-sync.syncInterval")
	if err == nil && secondsStr != "" {
		seconds, err := strconv.Atoi(secondsStr)
		if err != nil || seconds <= 0 {
			return RepoConfig{}, fmt.Errorf("auto-sync.syncInterval must be positive seconds")
		}
		cfg.PollInterval = time.Duration(seconds) * time.Second
	}
	// Pin the branch and remote at watcher startup. A later branch switch must
	// fail validation rather than publish an unrelated checkout.
	cfg.Branch, _ = r.git("symbolic-ref", "--quiet", "--short", "HEAD")
	if cfg.Branch != "" {
		cfg.Remote, _ = r.git("config", "--get", "branch."+cfg.Branch+".remote")
	}
	return cfg, nil
}

// WatchForChanges uses the same lock and status as manual sync. Transient
// failures are recorded by Sync and retried without stopping the watcher.
func WatchForChanges(cfg RepoConfig) error {
	ctx := cfg.Context
	if ctx == nil {
		ctx = context.Background()
	}
	return WatchForChangesContext(ctx, cfg)
}

func WatchForChangesContext(ctx context.Context, cfg RepoConfig) error {
	cfg.Context = ctx
	notifyChannel := make(chan notify.EventInfo, 100)
	if err := notify.Watch(filepath.Join(cfg.RepoPath, "..."), notifyChannel, notify.Write, notify.Rename, notify.Remove, notify.Create); err != nil {
		return tracerr.Wrap(err)
	}
	defer notify.Stop(notifyChannel)
	events := make(chan bool, 100)
	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	notifier, err := NewAwakeNotifier()
	if err != nil {
		log.Printf("awake notifier unavailable: %v", err)
	} else if err = notifier.Start(events); err != nil {
		log.Printf("awake notifier unavailable: %v", err)
	}
	done := make(chan struct{})
	go func() { runWatchLoop(watchCtx, cfg, events, AutoSync); close(done) }()
	defer func() { cancel(); <-done }()
	for {
		select {
		case <-ctx.Done():
			return nil
		case event := <-notifyChannel:
			if event == nil {
				continue
			}
			relative, err := filepath.Rel(cfg.RepoPath, event.Path())
			// Ignore Git metadata, including our permanent lock/status files.
			// Git itself handles ignored files; all other events debounce.
			if err != nil || relative == ".git" || strings.HasPrefix(relative, ".git"+string(filepath.Separator)) {
				continue
			}
			select {
			case events <- true:
			default:
			}
		}
	}
}

// runWatchLoop serializes sync attempts and resets its timer for each write.
// Failed attempts retry at 1, 2, 4 ... 60 seconds; a successful attempt resets
// the backoff. Polling still checks for remote-only changes.
func runWatchLoop(ctx context.Context, cfg RepoConfig, events <-chan bool, syncFn func(RepoConfig) error) {
	poll := cfg.PollInterval
	if poll <= 0 {
		poll = 10 * time.Minute
	}
	lag := cfg.FSLag
	if lag <= 0 {
		lag = time.Second
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()
	var next <-chan time.Time
	backoff := time.Second
	schedule := func(delay time.Duration) {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(delay)
		next = timer.C
	}
	perform := func() {
		next = nil
		if err := syncFn(cfg); err != nil {
			log.Printf("sync failed for %s: %v; retrying in %s", cfg.RepoPath, err, backoff)
			schedule(backoff)
			backoff *= 2
			if backoff > time.Minute {
				backoff = time.Minute
			}
		} else {
			backoff = time.Second
		}
	}
	perform()
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			schedule(lag)
		case <-ticker.C:
			perform()
		case <-next:
			perform()
		}
	}
}
