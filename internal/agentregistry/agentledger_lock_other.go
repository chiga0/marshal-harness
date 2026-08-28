//go:build !(aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris)

package agentregistry

import (
	"errors"
	"os"
)

func acquireAgentLedgerLock(string) (*os.File, error) {
	return nil, errors.New("agentregistry: durable authority locking is unsupported on this platform")
}

func releaseAgentLedgerLock(*os.File) error { return nil }
