package builtin

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

const darwinZombie = 5 // SZOMB, <sys/proc.h>

// processStartMarker is the pid's kernel start time. It is stable for one
// process and different for a later process that reuses the pid.
func processStartMarker(pid int) (string, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if errors.Is(err, unix.EIO) || errors.Is(err, unix.ESRCH) {
		return "", errProcessGone // a missing pid yields a short (EIO) sysctl result
	}
	if err != nil {
		return "", fmt.Errorf("builtin supervisor: sysctl kern.proc.pid.%d: %w", pid, err)
	}
	if int(kp.Proc.P_pid) != pid || kp.Proc.P_stat == darwinZombie {
		return "", errProcessGone
	}
	st := kp.Proc.P_starttime
	return fmt.Sprintf("darwin:%d.%06d", st.Sec, st.Usec), nil
}
