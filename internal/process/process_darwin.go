package process

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func bootIdentity() (string, error) {
	return unix.Sysctl("kern.bootsessionuuid")
}

// Use the kernel start time (including microseconds), not ps's rounded time,
// so a recycled PID cannot be mistaken for a recorded execution.
func Identity(pid int) (string, error) {
	boot, err := bootIdentity()
	if err != nil {
		return "", err
	}
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil {
		return "", err
	}
	if len(procs) == 0 {
		return "", os.ErrNotExist
	}
	p := procs[0].Proc
	return fmt.Sprintf("%s:%d.%06d", boot, p.P_starttime.Sec, p.P_starttime.Usec), nil
}

// Alive reports whether a process exists and is not a zombie.
func Alive(pid int) (bool, error) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil {
		return false, err
	}
	return len(procs) > 0 && procs[0].Proc.P_stat != 5, nil // SZOMB
}

func groupAlive(pgid int) bool {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", pgid)
	if err != nil {
		return true // Unknown is not confirmed cleanup.
	}
	for _, p := range procs {
		if p.Proc.P_stat != 5 { // SZOMB
			return true
		}
	}
	return false
}
