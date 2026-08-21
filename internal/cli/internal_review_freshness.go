package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/verification"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

// runInternalReviewFreshnessCheck is the fixed-binary replacement for the
// historical review_freshness_core_probe.go temporary executable. It serves
// bounded JSON requests until stdin closes and never mutates Core state.
func runInternalReviewFreshnessCheck(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	var err error
	args, err = consumeStableAttestation(args, stdin)
	if err != nil {
		return ExitFailure
	}
	if len(args) != 0 {
		fmt.Fprintln(stderr, "{\"status\":\"fail\",\"reasonCode\":\"checker-arguments-invalid\"}")
		return ExitUsage
	}
	validator, err := contract.NewValidator()
	if err != nil {
		fmt.Fprintln(stderr, "{\"status\":\"fail\",\"reasonCode\":\"core-contract-unavailable\"}")
		return ExitFailure
	}
	schemas := make(map[string]*jsonschema.Schema)
	decoder := json.NewDecoder(io.LimitReader(stdin, 32<<20))
	encoder := json.NewEncoder(stdout)
	for {
		var input reviewFreshnessRequest
		if err := decoder.Decode(&input); err != nil {
			if err == io.EOF {
				return ExitOK
			}
			return ExitFailure
		}
		digest, err := processReviewFreshness(input, validator, schemas)
		if err != nil {
			if err := encoder.Encode(reviewFreshnessResponse{OK: false}); err != nil {
				return ExitFailure
			}
			continue
		}
		if err := encoder.Encode(reviewFreshnessResponse{OK: true, Digest: digest}); err != nil {
			return ExitFailure
		}
	}
}

type reviewFreshnessRequest struct {
	Mode         string `json:"mode"`
	KindOrSchema string `json:"kindOrSchema"`
	Path         string `json:"path"`
}

type reviewFreshnessResponse struct {
	OK     bool   `json:"ok"`
	Digest string `json:"digest,omitempty"`
}

func processReviewFreshness(input reviewFreshnessRequest, validator *contract.Validator, schemas map[string]*jsonschema.Schema) (string, error) {
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
	default:
		return "", fmt.Errorf("unsupported mode %q", input.Mode)
	}
	return canonical.DigestJSON(raw)
}
