package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	cli "github.com/urfave/cli/v2"
	"github.com/ztrue/tracerr"

	"github.com/Spectralgo/git-auto-sync/common"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app := &cli.App{
		Name:                 "git-auto-sync",
		Version:              version,
		Usage:                "Automatically Sync any Git Repo",
		EnableBashCompletion: true,
		Commands: []*cli.Command{
			{
				Name:    "watch",
				Aliases: []string{"monitor", "w", "m"},
				Usage:   "Monitor a folder for changes",
				Action: func(ctx *cli.Context) error {
					repoPath, err := os.Getwd()
					if err != nil {
						return tracerr.Wrap(err)
					}

					repoPath, err = isValidGitRepo(repoPath)
					if err != nil {
						return tracerr.Wrap(err)
					}

					cfg, err := common.NewRepoConfig(repoPath)
					if err != nil {
						return tracerr.Wrap(err)
					}

					return common.WatchForChangesContext(ctx.Context, cfg)
				},
			},
			{
				Name:    "sync",
				Aliases: []string{"s"},
				Usage:   "Commit, fetch, merge and publish a checkout",
				Flags:   syncFlags(),
				Action:  syncAction,
			},
			{
				Name:   "status",
				Usage:  "Inspect local readiness and the last observed upstream",
				Flags:  syncFlags(),
				Action: statusAction,
			},
			{
				Name:  "check",
				Usage: "Check if a file will be ignored",
				Action: func(ctx *cli.Context) error {
					repoPath, err := os.Getwd()
					if err != nil {
						return tracerr.Wrap(err)
					}

					repoPath, err = isValidGitRepo(repoPath)
					if err != nil {
						return tracerr.Wrap(err)
					}

					path := ctx.Args().First()
					path, err = filepath.Abs(path)
					if err != nil {
						return tracerr.Wrap(err)
					}

					ignored, err := common.ShouldIgnoreFile(repoPath, path)
					if err != nil {
						return tracerr.Wrap(err)
					}
					fmt.Println("Ignored:", ignored)

					return nil
				},
			},
			{
				Name:  "version",
				Usage: "Print the version",
				Action: func(ctx *cli.Context) error {
					cli.VersionPrinter(ctx)
					return nil
				},
			},
			{
				Name:    "daemon",
				Aliases: []string{"d"},
				Usage:   "Interact with the background daemon",
				Subcommands: []*cli.Command{
					{
						Name:   "status",
						Usage:  "Show the Daemon's status",
						Action: daemonStatus,
					},
					{
						Name:    "list",
						Aliases: []string{"ls"},
						Usage:   "List of repos being auto-synced",
						Action:  daemonList,
					},
					{
						Name:   "add",
						Usage:  "Add a repo for auto-sync",
						Action: daemonAdd,
					},
					{
						Name:    "remove",
						Aliases: []string{"rm"},
						Usage:   "Remove a repo from auto-sync",
						Action:  daemonRm,
					},
					{
						Name:   "env",
						Usage:  "Set an environment variable",
						Action: daemonEnv,
					},
				},
			},
		},
	}

	err := app.RunContext(ctx, os.Args)
	if err != nil {
		log.Fatal(err)
	}
}

func syncFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "repo", Value: ".", Usage: "Checkout path"},
		&cli.StringFlag{Name: "remote", Usage: "Expected remote (defaults to configured upstream)"},
		&cli.StringFlag{Name: "branch", Usage: "Expected branch (defaults to configured upstream)"},
		&cli.StringFlag{Name: "strategy", Value: "merge", Usage: "Integration strategy (merge)"},
		&cli.BoolFlag{Name: "json", Usage: "Write exactly one structured JSON result"},
		&cli.DurationFlag{Name: "timeout", Usage: "Per-command timeout (default 1 minute)"},
		&cli.StringSliceFlag{Name: "env", Aliases: []string{"e"}, Usage: "Additional Git environment variables"},
	}
}

func syncConfig(ctx *cli.Context) common.RepoConfig {
	return common.RepoConfig{Context: ctx.Context, RepoPath: ctx.String("repo"), Remote: ctx.String("remote"), Branch: ctx.String("branch"), Strategy: ctx.String("strategy"), Env: ctx.StringSlice("env"), CommandTimeout: ctx.Duration("timeout")}
}

func printResult(ctx *cli.Context, result common.SyncResult) error {
	if ctx.Bool("json") {
		if err := json.NewEncoder(ctx.App.Writer).Encode(result); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(ctx.App.Writer, result.State)
		if result.Error != "" {
			fmt.Fprintln(ctx.App.ErrWriter, result.Error)
		}
	}
	if result.State != "synced" {
		return cli.Exit("checkout is not synced", 1)
	}
	return nil
}

func syncAction(ctx *cli.Context) error   { return printResult(ctx, common.Sync(syncConfig(ctx))) }
func statusAction(ctx *cli.Context) error { return printResult(ctx, common.Status(syncConfig(ctx))) }
