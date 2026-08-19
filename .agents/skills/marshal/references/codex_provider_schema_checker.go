// Command codex_provider_schema_checker bridges operator-local nofollow input
// reads to the production Codex Adapter compatibility checker. It contains no
// schema rules of its own.
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/chiga0/marshal-harness/internal/adapter/codex"
)

type request struct {
	Schema  string `json:"schema"`
	Profile string `json:"profile"`
}

type fatal struct {
	Status     string `json:"status"`
	ReasonCode string `json:"reasonCode"`
}

func main() {
	decoder := json.NewDecoder(io.LimitReader(os.Stdin, 12<<20))
	decoder.DisallowUnknownFields()
	var input request
	if err := decoder.Decode(&input); err != nil {
		exitFatal("codex-provider-checker-input-invalid")
	}
	if err := ensureEOF(decoder); err != nil {
		exitFatal("codex-provider-checker-input-invalid")
	}
	schema, err := base64.StdEncoding.Strict().DecodeString(input.Schema)
	if err != nil {
		exitFatal("codex-provider-checker-input-invalid")
	}
	profile, err := base64.StdEncoding.Strict().DecodeString(input.Profile)
	if err != nil {
		exitFatal("codex-provider-checker-input-invalid")
	}
	result, err := codex.CheckProviderSchemaCompatibility(schema, profile)
	if err != nil {
		var checkErr *codex.ProviderSchemaCheckError
		if errors.As(err, &checkErr) {
			exitFatal(checkErr.ReasonCode)
		}
		exitFatal("codex-provider-checker-failed")
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		os.Exit(2)
	}
	if result.Status == "pass" {
		return
	}
	os.Exit(1)
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return errors.New("trailing JSON value")
	}
	return err
}

func exitFatal(reason string) {
	var output bytes.Buffer
	_ = json.NewEncoder(&output).Encode(fatal{Status: "fatal", ReasonCode: reason})
	_, _ = os.Stderr.Write(output.Bytes())
	os.Exit(2)
}
