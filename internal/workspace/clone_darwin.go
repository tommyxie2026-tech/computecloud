package workspace

import (
	"golang.org/x/sys/unix"
	"os"
)

func cloneFile(source, target string, mode os.FileMode) error {
	if err := unix.Clonefile(source, target, 0); err != nil {
		return err
	}
	if err := os.Chmod(target, mode); err != nil {
		_ = os.Remove(target)
		return err
	}
	return nil
}
