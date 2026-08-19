// Command transcript_attestation_flood_probe is an adversarial fixture that
// floods stdout before reading stdin. Tests bind markerPath at build time and
// prove the validator terminates it before evidence is consumed.
package main

import (
	"io"
	"os"
)

var markerPath string

func main() {
	chunk := make([]byte, 32*1024)
	for written := 0; written < 32*1024*1024; written += len(chunk) {
		if _, err := os.Stdout.Write(chunk); err != nil {
			return
		}
	}
	raw, _ := io.ReadAll(os.Stdin)
	if len(raw) > 0 && markerPath != "" {
		_ = os.WriteFile(markerPath, []byte("evidence-consumed"), 0o600)
	}
}
