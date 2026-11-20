package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/Spectralgo/git-auto-sync/common"
	cfg "github.com/Spectralgo/git-auto-sync/common/config"
	"github.com/kardianos/service"
)

type Daemon struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

func (d *Daemon) Start(s service.Service) error {
	d.ctx, d.cancel = context.WithCancel(context.Background())
	d.done = make(chan struct{})
	go func() { d.run(); close(d.done) }()
	return nil
}

func (d *Daemon) run() {
	config, err := cfg.Read()
	if err != nil {
		panic(err)
	}

	var wg sync.WaitGroup

	for _, repoPath := range config.Repos {
		wg.Add(1)

		fmt.Println("Monitoring", repoPath)
		go watchForChangesContext(d.ctx, &wg, repoPath)
	}

	wg.Wait()
}

func (d *Daemon) Stop(s service.Service) error {
	if d.cancel == nil {
		return nil
	}
	d.cancel()
	select {
	case <-d.done:
		return nil
	case <-time.After(8 * time.Second):
		return fmt.Errorf("timed out waiting for active sync cleanup")
	}
}

func main() {
	daemon := Daemon{}
	autoSyncService, err := common.NewServiceWithDaemon(&daemon)
	if err != nil {
		log.Fatal("BuildService", err)
	}

	s := autoSyncService.Service
	logger, err := s.Logger(nil)
	if err != nil {
		log.Fatal("BuildLogger", err)
	}

	err = s.Run()
	if err != nil {
		_ = logger.Error("RunService", err)
	}
}

func watchForChanges(wg *sync.WaitGroup, repoPath string) {
	watchForChangesContext(context.Background(), wg, repoPath)
}

func watchForChangesContext(ctx context.Context, wg *sync.WaitGroup, repoPath string) {
	defer wg.Done()

	cfg, err := common.NewRepoConfig(repoPath)
	if err != nil {
		log.Println(err)
		return
	}

	err = common.WatchForChangesContext(ctx, cfg)
	if err != nil {
		log.Println(err)
	}
}

// FIXME: Handle operating system signal which tells it to reload the config
