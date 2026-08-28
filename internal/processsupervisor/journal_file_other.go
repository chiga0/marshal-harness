//go:build !darwin

package processsupervisor

import "os"

func validateJournalFile(file *os.File) error {
	if file == nil {
		return ErrIntervention
	}
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Mode().Perm() != 0o600 {
		return ErrIntervention
	}
	return nil
}
