//go:build darwin

package processsupervisor

import (
	"os"

	"golang.org/x/sys/unix"
)

func validateJournalFile(file *os.File) error {
	if file == nil {
		return ErrIntervention
	}
	var stat unix.Stat_t
	if unix.Fstat(int(file.Fd()), &stat) != nil || uint32(stat.Mode)&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(os.Geteuid()) || uint32(stat.Mode)&0o777 != 0o600 || stat.Nlink != 1 {
		return ErrIntervention
	}
	return nil
}
