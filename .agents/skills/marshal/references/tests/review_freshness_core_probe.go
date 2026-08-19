// Command review_freshness_core_probe exposes Marshal's exact JCS and
// contract implementations to the operator-local freshness preflight.  It is
// intentionally tiny: the Python orchestrator owns pathname/claim handling,
// while this command is the sole JSON canonicalization and marshal.dev
// contract authority.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/verification"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

type request struct {
	Mode         string `json:"mode"`
	KindOrSchema string `json:"kindOrSchema"`
	Path         string `json:"path"`
}

type response struct {
	OK     bool   `json:"ok"`
	Digest string `json:"digest,omitempty"`
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "serve" {
		serve()
		return
	}
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: review_freshness_core_probe MODE KIND_OR_SCHEMA FILE")
		os.Exit(2)
	}
	validator, err := contract.NewValidator()
	if err != nil {
		fail()
	}
	digest, err := process(request{Mode: os.Args[1], KindOrSchema: os.Args[2], Path: os.Args[3]}, validator, make(map[string]*jsonschema.Schema))
	if err != nil {
		fail()
	}
	fmt.Println(digest)
}

func serve() {
	validator, err := contract.NewValidator()
	if err != nil {
		fail()
	}
	schemas := make(map[string]*jsonschema.Schema)
	decoder := json.NewDecoder(bufio.NewReader(os.Stdin))
	encoder := json.NewEncoder(os.Stdout)
	for {
		var input request
		if err := decoder.Decode(&input); err != nil {
			return
		}
		digest, err := process(input, validator, schemas)
		if err != nil {
			_ = encoder.Encode(response{OK: false})
			continue
		}
		if err := encoder.Encode(response{OK: true, Digest: digest}); err != nil {
			return
		}
	}
}

func process(input request, validator *contract.Validator, schemas map[string]*jsonschema.Schema) (string, error) {
	if input.Mode == "observe" {
		observed, err := verification.Observe(input.Path, input.KindOrSchema, 128<<20)
		if err != nil {
			return "", err
		}
		data, err := json.Marshal(observed)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	raw, err := os.ReadFile(input.Path)
	if err != nil {
		return "", err
	}
	switch input.Mode {
	case "contract":
		kind, err := domain.ParseKind(input.KindOrSchema)
		if err != nil {
			return "", err
		}
		if err := validator.Validate(kind, raw); err != nil {
			return "", err
		}
	case "candidate":
		if err := validator.Validate(domain.KindCandidate, raw); err != nil {
			return "", err
		}
		var candidate domain.Candidate
		if err := json.Unmarshal(raw, &candidate); err != nil {
			return "", err
		}
		if err := candidate.Validate(); err != nil {
			return "", err
		}
		return candidate.Digest()
	case "schema":
		compiled := schemas[input.KindOrSchema]
		if compiled == nil {
			compiler := jsonschema.NewCompiler()
			compiler.DefaultDraft(jsonschema.Draft2020)
			schemaPath, err := filepath.Abs(input.KindOrSchema)
			if err != nil {
				return "", err
			}
			compiled, err = compiler.Compile("file://" + filepath.ToSlash(schemaPath))
			if err != nil {
				return "", err
			}
			schemas[input.KindOrSchema] = compiled
		}
		document, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			return "", err
		}
		if err := compiled.Validate(document); err != nil {
			return "", err
		}
	case "canonical":
		// KindOrSchema is deliberately ignored for this mode.
	default:
		return "", fmt.Errorf("unsupported mode %q", input.Mode)
	}
	return canonical.DigestJSON(raw)
}

func fail() {
	fmt.Println("rejected")
	os.Exit(2)
}
