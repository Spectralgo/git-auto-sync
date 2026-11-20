//go:build !windows

package common

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// Git can start transports, hooks and merge drivers. Cancel the complete
// process group so no descendant keeps editing after our lock is released.
func configureGitProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
