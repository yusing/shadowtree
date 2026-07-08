//go:build !(aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris)

package runner

import "os/exec"

func configureCommandCancellation(cmd *exec.Cmd) {
	cmd.WaitDelay = canceledCommandWaitDelay
}
