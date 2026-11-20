# Git Auto Sync

This is the [Spectralgo fork](https://github.com/Spectralgo/git-auto-sync) of
[GitJournal/git-auto-sync](https://github.com/GitJournal/git-auto-sync), under
the original Apache 2.0 [license](LICENSE). This fork adds a locked, structured,
merge-based sync engine and fixes watcher debounce and transient-error recovery.

## Install this fork

Requires Go 1.23 or later and a native Git executable. Install a reviewed commit:

```sh
go install github.com/Spectralgo/git-auto-sync@<commit>
```

To build from this checkout (including on an Apple Silicon Mac):

```sh
go build -trimpath -ldflags "-X main.version=$(git rev-parse HEAD)" -o bin/git-auto-sync .
go build -trimpath -o bin/git-auto-sync-daemon ./daemon
```

The package-manager instructions below refer to upstream releases and do not
install this fork.

## Reliable one-shot synchronization

Configure a Git identity, authentication and an upstream for an existing local
branch first. Both the branch name and its configured upstream must match the
expected remote and branch before any files are staged or committed.

```sh
git-auto-sync sync --repo '/path/to/notes' --remote origin --branch main --strategy merge --json
git-auto-sync status --repo '/path/to/notes' --json
```

Both commands return exactly one JSON value on stdout in JSON mode. Exit status
is zero only for `synced`; all other states return nonzero. Fields include:

| Field | Meaning |
| --- | --- |
| `state` | `synced`, `pending`, `conflict`, `error` or `busy` |
| `head`, `remote_head` | Local HEAD and last observed upstream commit |
| `ahead`, `behind` | Commit counts relative to that upstream |
| `last_success` | UTC timestamp of the last verified clean sync, or an empty string |
| `error` | Failure description, or an empty string |
| `remote`, `branch` | Expected publication target |

`sync` stages complete local changes with `git add --all`, including deletions,
empty files and hidden files, subject to normal Git ignore rules. It commits,
fetches the expected branch, merges without rewriting history, and pushes the
explicit refspec `HEAD:refs/heads/<branch>`. Push failure triggers at most three
fetch/merge/push attempts. It never force-pushes or chooses a side in a conflict.
Only `merge` is supported. A failed merge is aborted, preserving the local
commit and document content for manual reconciliation. An already unfinished
Git operation is reported without attempting to complete it.

`status` is a local readiness check. It reads fresh worktree dirt, HEAD and
tracking references instead of trusting a cached success. It does not contact
the server; run `sync` to establish remote freshness. A clean checkout without
a previous successful sync is `pending`. Failures and conflicts remain visible
across process restarts until synchronization succeeds. Explicit `--remote` and
`--branch` are recommended; otherwise the previous target is retained or the
initial configured upstream is used.

Native Git inherits the process environment, including credential helpers and
`SSH_AUTH_SOCK`; `--env KEY=VALUE` adds explicit overrides. Terminal credential
prompts are disabled. Repository `core.sshCommand`, `GIT_SSH` and
`GIT_SSH_COMMAND` are preserved so repository-scoped keys and host checks keep
working. Configure SSH with `-oBatchMode=yes`; transports must be noninteractive.
Repository-selector variables such as `GIT_DIR`, `GIT_WORK_TREE` and
`GIT_INDEX_FILE` are removed so inherited Git-hook environment cannot redirect
the explicit checkout path to another repository or index.
Each subprocess has a timeout (`--timeout 20s`, default `1m`). On Unix, timing
out kills its process group, including Git transports and hooks. Commands never
print environment variables or raw Git failure output, which can contain secrets.
`SIGTERM` and `SIGINT` cancel active Git process groups and wait for cleanup
before releasing the checkout lock. Divergent merges persist abort metadata
before the separate integration commit runs its hooks. Abort and final status
use a fresh cleanup context capped at five seconds; supervising wrappers should
allow at least ten seconds after `SIGTERM` before escalating termination.

## Cooperative checkout lock and durable status

Resolve the actual Git directory, including for linked worktrees:

```sh
git -C '/path/to/notes' rev-parse --absolute-git-dir
```

The lock is `<git-directory>/git-auto-sync.lock`. On macOS and Linux it uses an
exclusive, nonblocking OS `flock`, compatible with Python
`fcntl.flock(fd, LOCK_EX | LOCK_NB)`. Open or create it with mode `0600`, keep
the descriptor open for the complete publication, then unlock and close it.
**Never unlink the lock file.** The OS releases the lock on process death.
Manual sync, watcher sync and status all use this same lock; contention returns
`busy` and does not overwrite the previous status.

Cooperative writers should acquire this lock, atomically publish complete files,
release it, then invoke `sync`. An editor that does not take the lock can still
race Git; use separate files for independent writers. This lock is local to one
checkout, not distributed between machines.

`<git-directory>/git-auto-sync-status.json` is atomically replaced after each
sync attempt, outside tracked document content. If a process is forcibly killed
during native Git activity, inspect any unfinished Git operation or stale native
`index.lock` manually before retrying. Do not delete active Git locks. The
cooperative lock itself never requires stale-lock deletion.

GitAutoSync is a simple command line program to automatically commit changes
to your git repo, and always keep that repo up to date. This way you can use any editor with your text files, and never need to worry about
comitting and remembering to push and pull changes.

## Installation

* OSX - `brew install GitJournal/tap/git-auto-sync`
* Linux -
  - Ubuntu/Debian -

    ```bash
    sudo echo "deb [trusted=yes] https://apt.fury.io/vhanda/ /" | sudo tee /etc/apt/sources.list.d/git-auto-sync.list
    sudo apt-get update
    sudo apt-get install -y git-auto-sync
    ```
  - Fedora/RPM -

    ```bash
    sudo echo -e "[git-auto-sync]\nname=Git-Auto-Sync\nbaseurl=https://yum.fury.io/vhanda/\nenabled=1\ngpgcheck=0" | sudo tee /etc/yum.repos.d/git-auto-sync.repo
    sudo yum update
    sudo yum install -y git-auto-sync
    ```
  - Arch Linux - Please write an AUR package and open a PR for this. You're an Arch User. You got this.
  - Other - Download the [latest release](https://github.com/GitJournal/git-auto-sync/releases/latest)
* Windows - Download the [latest release](https://github.com/GitJournal/git-auto-sync/releases/latest)

## How to use?

GitAutoSync comes with a manual and daemon mode. It's recommended to start with the manual
mode to ensure authentication is working correctly. It internally just calls the `git` executable
so, if that works, `git-auto-sync` should just work.

You can test it out by running `git-auto-sync sync` to commit, fetch, merge and push any changes.
If there are no changes, it will still fetch, merge and verify publication.

Once you're satisfied that `git-auto-sync` is working for you. You can run `git-auto-sync daemon add <repoPath>` to start a background daemon which will continously monitor that repo for any changes
in the file system and accordingly sync the changes.

This daemon will be automatically started as a system process.

You can check if it is running `git-auto-sync daemon status`

### Background Daemon

The background daemon will be started / stopped automatically if there are any repos to watch in `git-auto-sync daemon ls`.
This process debounces filesystem changes for one second, polls every 10 minutes,
and additionally tries to sync on resuming from a suspend. Transient sync failures
retry with exponential backoff capped at 60 seconds without terminating the
watcher. Awake notifications are optional; polling and structured status work
without desktop notifications.

### Merge Conflicts

This fork uses merges. A content conflict aborts the merge and records `conflict`
in the durable status file. Local and remote commits are preserved. Resolve the
divergence manually, then rerun sync; no desktop notification is required.

### Ignored Files

The sync engine uses Git's ignore rules. Add editor swap files and other local
artifacts to `.gitignore` or `.git/info/exclude`. Hidden and empty documents are
included when they are not ignored by Git.

## Similar Projects

- [Obsidian Git](https://github.com/denolehov/obsidian-git)
- [VS Code GitDoc](https://marketplace.visualstudio.com/items?itemName=vsls-contrib.gitdoc)
- [Git Annex](https://git-annex.branchable.com/)
- [Git Sync](https://github.com/simonthum/git-sync)
