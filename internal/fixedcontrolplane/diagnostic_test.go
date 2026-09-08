package fixedcontrolplane

import (
	"errors"
	"strings"
	"testing"
)

func TestRequestDiagnosticPreservesClassificationAndRedactsCause(t *testing.T) {
	for _, stage := range []string{"client-dial", "client-write", "client-response", "client-operation", "client-recheck", "client-half-close", "server-read", "server-admission", "server-precheck", "server-dispatch", "server-postcheck", "server-response", "server-half-close"} {
		err := atRequestStage(stage, errors.Join(ErrConflict, errors.New("private-path-and-secret")))
		if !errors.Is(err, ErrConflict) || DiagnosticStage(err) != stage || strings.Contains(err.Error(), "secret") {
			t.Fatalf("stage %s changed classification or exposed cause", stage)
		}
	}
	if atRequestStage("client-dial", nil) != nil {
		t.Fatal("successful operation became failure")
	}
	for _, err := range []error{nil, (*requestStageError)(nil), ErrConflict, atRequestStage("private-path", ErrConflict)} {
		if DiagnosticStage(err) != "" {
			t.Fatal("unknown diagnostic label escaped")
		}
	}
}
