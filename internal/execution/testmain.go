package execution

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	if inheritedTestEntry() {
		os.Exit(0)
	}
	os.Exit(m.Run())
}
