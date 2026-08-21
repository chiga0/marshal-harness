package cli

import (
	"fmt"
	"io"
)

func awaitStableAttestation(stdin io.Reader) error {
	var token [1]byte
	if _, err := io.ReadFull(stdin, token[:]); err != nil {
		return err
	}
	return nil
}

func consumeStableAttestation(args []string, stdin io.Reader) ([]string, error) {
	if len(args) == 0 || args[0] != "--attestation-ready" {
		return args, nil
	}
	if err := awaitStableAttestation(stdin); err != nil {
		return nil, fmt.Errorf("attestation handshake: %w", err)
	}
	return args[1:], nil
}
