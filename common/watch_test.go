package common

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWatchLoopDebouncesUntilWritesSettle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan bool, 20)
	calls := make(chan time.Time, 20)
	done := make(chan struct{})
	cfg := RepoConfig{FSLag: 100 * time.Millisecond, PollInterval: time.Hour}
	go func() {
		runWatchLoop(ctx, cfg, events, func(RepoConfig) error { calls <- time.Now(); return nil })
		close(done)
	}()
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("no initial sync")
	}
	for i := 0; i < 3; i++ {
		events <- true
		select {
		case <-calls:
			t.Fatal("sync ran before debounce elapsed")
		case <-time.After(30 * time.Millisecond):
		}
	}
	select {
	case <-calls:
		t.Fatal("debounce did not reset after last write")
	case <-time.After(30 * time.Millisecond):
	}
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("settled writes were not synced")
	}
	select {
	case <-calls:
		t.Fatal("burst triggered duplicate sync")
	case <-time.After(130 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watch loop did not stop")
	}
}

func TestWatchLoopRetriesTransientFailureAndKeepsWatching(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan bool, 10)
	calls := make(chan int, 10)
	attempts := 0
	go runWatchLoop(ctx, RepoConfig{FSLag: 20 * time.Millisecond, PollInterval: time.Hour}, events, func(RepoConfig) error {
		attempts++
		calls <- attempts
		if attempts == 1 {
			return errors.New("offline")
		}
		return nil
	})
	for want := 1; want <= 2; want++ {
		select {
		case got := <-calls:
			if got != want {
				t.Fatalf("got attempt %d", got)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("transient failure killed watch loop")
		}
	}
	events <- true
	select {
	case got := <-calls:
		if got != 3 {
			t.Fatalf("got attempt %d", got)
		}
	case <-time.After(time.Second):
		t.Fatal("watching did not resume")
	}
}

func TestWatchPollingHonorsPendingWriteDebounce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan bool, 1)
	calls := make(chan struct{}, 20)
	go runWatchLoop(ctx, RepoConfig{FSLag: 250 * time.Millisecond, PollInterval: 50 * time.Millisecond}, events, func(RepoConfig) error {
		calls <- struct{}{}
		return nil
	})
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("initial sync missing")
	}
	events <- true
	select {
	case <-calls:
		t.Fatal("remote poll bypassed pending write debounce")
	case <-time.After(180 * time.Millisecond):
	}
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("settled write never synchronized")
	}
}

func TestWatchPollingHonorsWritesQueuedDuringSync(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan bool, 1)
	started, release := make(chan struct{}), make(chan struct{})
	calls := make(chan struct{}, 20)
	first := true
	go runWatchLoop(ctx, RepoConfig{FSLag: 250 * time.Millisecond, PollInterval: 20 * time.Millisecond}, events, func(RepoConfig) error {
		if first {
			first = false
			close(started)
			<-release
		} else {
			calls <- struct{}{}
		}
		return nil
	})
	<-started
	events <- true
	time.Sleep(50 * time.Millisecond)
	close(release)
	select {
	case <-calls:
		t.Fatal("poll bypassed write queued during previous sync")
	case <-time.After(180 * time.Millisecond):
	}
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("queued write did not synchronize after settling")
	}
}
