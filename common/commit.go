package common

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/ztrue/tracerr"
	git "gopkg.in/src-d/go-git.v4"
)

func commit(repoConfig RepoConfig) error {
	repoPath := repoConfig.RepoPath
	repo, err := git.PlainOpenWithOptions(repoPath, &git.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		return tracerr.Wrap(err)
	}

	w, err := repo.Worktree()
	if err != nil {
		return tracerr.Wrap(err)
	}

	status, err := w.Status()
	if err != nil {
		return tracerr.Wrap(err)
	}

	hasChanges := false
	commitMsg := []string{}
	for filePath, fileStatus := range status {
		if fileStatus.Worktree == git.Unmodified && fileStatus.Staging == git.Unmodified {
			continue
		}

		ignore, err := ShouldIgnoreFile(repoPath, filePath)
		if err != nil {
			return tracerr.Wrap(err)
		}

		if ignore {
			continue
		}

		hasChanges = true
		_, err = w.Add(filePath)
		if err != nil {
			return tracerr.Wrap(err)
		}

		msg := ""
		if fileStatus.Worktree == git.Untracked && fileStatus.Staging == git.Untracked {
			msg += "?? "
		} else {
			msg += " " + string(fileStatus.Worktree) + " "
		}
		msg += filePath
		commitMsg = append(commitMsg, msg)
	}

	sort.Strings(commitMsg)
	msg := strings.Join(commitMsg, "\n")

	if !hasChanges {
		return nil
	}

	_, err = GitCommand(repoConfig, []string{"commit", "-m", msg})
	if err != nil {
		return tracerr.Wrap(err)
	}

	return nil
}

func GitCommand(repoConfig RepoConfig, args []string) (bytes.Buffer, error) {
	var outb bytes.Buffer
	gitExec := "git"
	if repoConfig.GitExec != "" {
		gitExec = repoConfig.GitExec
	}
	timeout := repoConfig.CommandTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	parent := repoConfig.Context
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	statusCmd := exec.CommandContext(ctx, gitExec, args...)
	configureGitProcess(statusCmd)
	statusCmd.WaitDelay = 2 * time.Second
	statusCmd.Dir = repoConfig.RepoPath
	statusCmd.Stdout = &outb
	// Do not echo Git output on failure: remote URLs, hooks and credential
	// helpers can print secrets. The operation and exit status are sufficient.
	statusCmd.Env = toEnvString(repoConfig)
	err := statusCmd.Run()
	if err != nil {
		operation := "command"
		if len(args) > 0 {
			operation = args[0]
		}
		if ctx.Err() != nil {
			return outb, fmt.Errorf("git %s interrupted: %w", operation, ctx.Err())
		}
		return outb, fmt.Errorf("git %s failed: %w", operation, err)
	}
	return outb, nil
}

func toEnvString(repoConfig RepoConfig) []string {
	vals := append([]string{}, os.Environ()...)
	vals = append(vals, repoConfig.Env...)
	// A process launched by a Git hook can inherit repository selectors. The
	// explicit checkout path must remain authoritative; preserve authentication
	// and other environment while dropping selectors for another worktree/index.
	filtered := make([]string, 0, len(vals))
	for _, entry := range vals {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_PREFIX", "GIT_NAMESPACE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES":
			continue
		}
		filtered = append(filtered, entry)
	}
	vals = filtered
	vals = append(vals, "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "GIT_EDITOR=true", "GIT_SEQUENCE_EDITOR=true", "GIT_MERGE_AUTOEDIT=no")
	return vals
}
