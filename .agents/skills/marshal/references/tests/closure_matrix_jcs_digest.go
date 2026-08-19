package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: jcs-digest FILE...")
		os.Exit(2)
	}
	digests := make([]string, 0, len(os.Args)-1)
	for _, path := range os.Args[1:] {
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "read rejected")
			os.Exit(1)
		}
		digest, err := canonical.DigestJSON(data)
		if err != nil {
			fmt.Fprintln(os.Stderr, "canonicalization rejected")
			os.Exit(1)
		}
		digests = append(digests, digest)
	}
	if err := json.NewEncoder(os.Stdout).Encode(digests); err != nil {
		fmt.Fprintln(os.Stderr, "encoding rejected")
		os.Exit(1)
	}
}
