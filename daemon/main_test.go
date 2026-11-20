package main

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestInvalidRepoConfigurationDoesNotStartWatcher(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	done := make(chan struct{})
	go func() { watchForChanges(&wg, filepath.Join(t.TempDir(), "missing")); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("invalid configuration started a watcher with the zero-value checkout path")
	}
}
