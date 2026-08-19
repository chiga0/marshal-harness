// Command transcript_attestation_hang_probe is an adversarial fixture that
// consumes stdin and never exits, proving the validator enforces its deadline.
package main

import (
	"io"
	"os"
	"time"
)

func main() {
	_, _ = io.ReadAll(os.Stdin)
	for {
		time.Sleep(time.Hour)
	}
}
