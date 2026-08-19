package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
)

type result struct {
	Validated int      `json:"validated"`
	JCS       []string `json:"jcs"`
	Raw       []string `json:"raw"`
}

func main() {
	validator, err := contract.NewValidator()
	if err != nil {
		reject("validator init rejected")
	}
	output := result{JCS: []string{}, Raw: []string{}}
	for index := 1; index < len(os.Args); {
		switch os.Args[index] {
		case "validate":
			if index+2 >= len(os.Args) {
				reject("invalid validate arguments")
			}
			kind, parseErr := domain.ParseKind(os.Args[index+1])
			if parseErr != nil {
				reject("unknown contract kind")
			}
			data := read(os.Args[index+2])
			if validateErr := validator.Validate(kind, data); validateErr != nil {
				reject("contract rejected")
			}
			output.Validated++
			index += 3
		case "jcs":
			if index+1 >= len(os.Args) {
				reject("invalid jcs arguments")
			}
			digest, digestErr := canonical.DigestJSON(read(os.Args[index+1]))
			if digestErr != nil {
				reject("canonicalization rejected")
			}
			output.JCS = append(output.JCS, digest)
			index += 2
		case "raw":
			if index+1 >= len(os.Args) {
				reject("invalid raw arguments")
			}
			output.Raw = append(output.Raw, canonical.DigestBytes(read(os.Args[index+1])))
			index += 2
		default:
			reject("unknown operation")
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(output); err != nil {
		reject("encoding rejected")
	}
}

func read(path string) []byte {
	data, err := os.ReadFile(path)
	if err != nil {
		reject("read rejected")
	}
	return data
}

func reject(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
