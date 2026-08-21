package cli

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/chiga0/marshal-harness/internal/adapter/codex"
)

const codexSchemaCheckMaxInputBytes = 12 << 20

// runInternalCodexSchemaCheck keeps provider compatibility semantics in the
// stable Marshal binary. The old operator-local Go checker remains only as a
// test fixture; production validators invoke this command.
func runInternalCodexSchemaCheck(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	var err error
	args, err = consumeStableAttestation(args, stdin)
	if err != nil {
		return writeCodexSchemaFailure(stderr, "codex-provider-checker-input-invalid", ExitUsage)
	}
	if len(args) != 0 {
		return writeCodexSchemaFailure(stderr, "codex-provider-checker-input-invalid", ExitUsage)
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, codexSchemaCheckMaxInputBytes+1))
	if err != nil || int64(len(raw)) > codexSchemaCheckMaxInputBytes {
		return writeCodexSchemaFailure(stderr, "codex-provider-checker-input-invalid", ExitUsage)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var input struct {
		Schema  string `json:"schema"`
		Profile string `json:"profile"`
	}
	if err := decoder.Decode(&input); err != nil || ensureCodexEOF(decoder) != nil {
		return writeCodexSchemaFailure(stderr, "codex-provider-checker-input-invalid", ExitUsage)
	}
	schema, err := base64.StdEncoding.Strict().DecodeString(input.Schema)
	if err != nil {
		return writeCodexSchemaFailure(stderr, "codex-provider-checker-input-invalid", ExitUsage)
	}
	profile, err := base64.StdEncoding.Strict().DecodeString(input.Profile)
	if err != nil {
		return writeCodexSchemaFailure(stderr, "codex-provider-checker-input-invalid", ExitUsage)
	}
	result, err := codex.CheckProviderSchemaCompatibility(schema, profile)
	if err != nil {
		var checkErr *codex.ProviderSchemaCheckError
		if errors.As(err, &checkErr) {
			return writeCodexSchemaFailure(stderr, checkErr.ReasonCode, ExitUsage)
		}
		return writeCodexSchemaFailure(stderr, "codex-provider-checker-failed", ExitUsage)
	}
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		return ExitFailure
	}
	if result.Status == "pass" {
		return ExitOK
	}
	return ExitFailure
}

func ensureCodexEOF(decoder *json.Decoder) error {
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

func writeCodexSchemaFailure(stderr io.Writer, reason string, code int) int {
	_, _ = fmt.Fprintf(stderr, "{\"status\":\"fatal\",\"reasonCode\":%q}\n", reason)
	return code
}
