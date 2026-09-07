package contract

import (
	"github.com/chiga0/marshal-harness/internal/domain"
	"testing"
)

func TestTaskSpecResultContractIsOptionalAndClosed(t *testing.T) {
	validator := mustValidator(t)
	for _, mode := range []string{"", domain.ResultContractWorkerJSON, domain.ResultContractNativeTerminal, "unknown"} {
		data := mutateFixture(t, "examples/happy-path/task-spec.json", func(document map[string]any) {
			worker := document["worker"].(map[string]any)
			if mode != "" {
				worker["resultContract"] = mode
			} else {
				delete(worker, "resultContract")
			}
		})
		err := validator.Validate(domain.KindTask, data)
		if (err == nil) != (mode != "unknown") {
			t.Fatalf("mode %q admission = %v", mode, err)
		}
	}
}
