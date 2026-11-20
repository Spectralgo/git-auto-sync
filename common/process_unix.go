//go:build !windows

package common

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Git can start transports, hooks and merge drivers. Cancel the complete
// process group so no descendant keeps editing after our lock is released.
func configureGitProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// Give native Git a chance to remove index/ref locks before forcing
		// termination. All descendants share this process group.
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		if err != nil {
			return err
		}
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if errors.Is(syscall.Kill(-cmd.Process.Pid, 0), syscall.ESRCH) {
				return nil
			}
			time.Sleep(10 * time.Millisecond)
		}
		// Reap stubborn transports/filters before returning to the lock owner.
		err = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
}
