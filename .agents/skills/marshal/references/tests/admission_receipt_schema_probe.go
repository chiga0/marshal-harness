package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: admission-receipt-schema-probe SCHEMA INSTANCE")
		os.Exit(2)
	}
	schemaPath, err := filepath.Abs(os.Args[1])
	if err != nil {
		panic(err)
	}
	compiler := jsonschema.NewCompiler()
	compiled, err := compiler.Compile("file://" + filepath.ToSlash(schemaPath))
	if err != nil {
		panic(err)
	}
	raw, err := os.ReadFile(os.Args[2])
	if err != nil {
		panic(err)
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		panic(err)
	}
	if err := compiled.Validate(document); err != nil {
		panic(err)
	}
	fmt.Println("draft-2020-12-schema-and-instance-ok")
}
