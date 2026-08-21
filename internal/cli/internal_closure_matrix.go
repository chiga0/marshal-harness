package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"syscall"

	"github.com/chiga0/marshal-harness/internal/buildinfo"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
)

const (
	closureMatrixMaxArgs      = 768
	closureMatrixMaxPathBytes = 4096
	closureMatrixMaxFileBytes = 128 << 20
)

type closureMatrixResult struct {
	Validated int      `json:"validated"`
	JCS       []string `json:"jcs"`
	Raw       []string `json:"raw"`
	Marshal   any      `json:"marshal"`
}

// runInternalClosureMatrixCheck keeps closure-matrix contract/JCS operations
// inside the fixed Marshal executable. The old go-run helper is test-only;
// production preflight invokes this command with a stable binary identity.
func runInternalClosureMatrixCheck(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	var err error
	args, err = consumeStableAttestation(args, stdin)
	if err != nil {
		return writeClosureMatrixFailure(stderr, "closure-matrix-checker-input-invalid")
	}
	if len(args) > closureMatrixMaxArgs {
		return writeClosureMatrixFailure(stderr, "closure-matrix-checker-input-invalid")
	}
	validator, err := contract.NewValidator()
	if err != nil {
		return writeClosureMatrixFailure(stderr, "closure-matrix-checker-unavailable")
	}
	result := closureMatrixResult{JCS: []string{}, Raw: []string{}, Marshal: map[string]any{
		"version":                buildinfo.Current().Version,
		"internalCommandVersion": "closure-matrix-check/v1",
	}}
	for index := 0; index < len(args); {
		switch args[index] {
		case "validate":
			if index+2 >= len(args) {
				return writeClosureMatrixFailure(stderr, "closure-matrix-checker-input-invalid")
			}
			kind, parseErr := domain.ParseKind(args[index+1])
			if parseErr != nil {
				return writeClosureMatrixFailure(stderr, "closure-matrix-checker-input-invalid")
			}
			data, readErr := readClosureMatrixFile(args[index+2])
			if readErr != nil || validator.Validate(kind, data) != nil {
				return writeClosureMatrixFailure(stderr, "closure-matrix-checker-rejected")
			}
			result.Validated++
			index += 3
		case "jcs", "raw":
			if index+1 >= len(args) {
				return writeClosureMatrixFailure(stderr, "closure-matrix-checker-input-invalid")
			}
			data, readErr := readClosureMatrixFile(args[index+1])
			if readErr != nil {
				return writeClosureMatrixFailure(stderr, "closure-matrix-checker-rejected")
			}
			if args[index] == "jcs" {
				digest, digestErr := canonical.DigestJSON(data)
				if digestErr != nil {
					return writeClosureMatrixFailure(stderr, "closure-matrix-checker-rejected")
				}
				result.JCS = append(result.JCS, digest)
			} else {
				result.Raw = append(result.Raw, canonical.DigestBytes(data))
			}
			index += 2
		default:
			return writeClosureMatrixFailure(stderr, "closure-matrix-checker-input-invalid")
		}
	}
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		return ExitFailure
	}
	return ExitOK
}

func readClosureMatrixFile(path string) ([]byte, error) {
	if path == "" || len(path) > closureMatrixMaxPathBytes {
		return nil, fmt.Errorf("path length")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() > closureMatrixMaxFileBytes {
		return nil, fmt.Errorf("file identity")
	}
	data, err := io.ReadAll(io.LimitReader(file, closureMatrixMaxFileBytes+1))
	if err != nil || len(data) > closureMatrixMaxFileBytes {
		return nil, fmt.Errorf("bounded read")
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || before.ModTime() != after.ModTime() {
		return nil, fmt.Errorf("file changed")
	}
	return data, nil
}

func writeClosureMatrixFailure(stderr io.Writer, reason string) int {
	_, _ = fmt.Fprintf(stderr, "{\"status\":\"fail\",\"reasonCode\":%q}\n", reason)
	return ExitFailure
}
