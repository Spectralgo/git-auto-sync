package common

import "os/exec"

// CommandContext still bounds the Git process on Windows. Process-tree
// cancellation currently requires the Unix process-group implementation.
func configureGitProcess(cmd *exec.Cmd) {}
