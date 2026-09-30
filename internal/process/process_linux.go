package process

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func Identity(pid int) (string, error) {
	b, e := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if e != nil {
		return "", e
	}
	i := strings.LastIndexByte(string(b), ')')
	if i < 0 {
		return "", errors.New("invalid process stat")
	}
	f := strings.Fields(string(b[i+1:]))
	if len(f) < 20 {
		return "", errors.New("short process stat")
	}
	boot, e := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if e != nil {
		return "", e
	}
	return strings.TrimSpace(string(boot)) + ":" + f[19], nil
}
func groupAlive(pgid int) bool {
	entries, e := os.ReadDir("/proc")
	if e != nil {
		return true
	}
	for _, v := range entries {
		pid, e := strconv.Atoi(v.Name())
		if e != nil {
			continue
		}
		b, e := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if e != nil {
			continue
		}
		i := strings.LastIndexByte(string(b), ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(string(b[i+1:]))
		if len(f) > 2 && f[0] != "Z" && f[0] != "X" && f[2] == strconv.Itoa(pgid) {
			return true
		}
	}
	return false
}
func bootIdentity() (string, error) {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(b)), err
}

// Alive reports whether a process exists and is not a zombie.
func Alive(pid int) (bool, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	i := strings.LastIndexByte(string(b), ')')
	if i < 0 {
		return false, errors.New("invalid process stat")
	}
	f := strings.Fields(string(b[i+1:]))
	if len(f) == 0 {
		return false, errors.New("short process stat")
	}
	return f[0] != "Z" && f[0] != "X", nil
}
