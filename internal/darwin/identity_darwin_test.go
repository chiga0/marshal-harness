//go:build darwin

package darwin

import (
	"os"
	"testing"
)

func TestInspectExecutableRejectsNonExecutableFD(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "not-executable")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := InspectExecutable(file, ""); err == nil {
		t.Fatal("non-executable fd was accepted")
	}
}
