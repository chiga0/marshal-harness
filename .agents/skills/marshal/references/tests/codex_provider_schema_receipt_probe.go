package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: receipt-probe SCHEMA INSTANCE...")
		os.Exit(2)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	schemaPath, err := filepath.Abs(os.Args[1])
	if err != nil {
		panic(err)
	}
	compiled, err := compiler.Compile("file://" + filepath.ToSlash(schemaPath))
	if err != nil {
		panic(err)
	}
	for _, instancePath := range os.Args[2:] {
		raw, readErr := os.ReadFile(instancePath)
		if readErr != nil {
			panic(readErr)
		}
		document, decodeErr := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if decodeErr != nil {
			panic(decodeErr)
		}
		if validateErr := compiled.Validate(document); validateErr != nil {
			panic(validateErr)
		}
	}
	fmt.Println("draft-2020-12-receipt-schema-and-instances-ok")
}
