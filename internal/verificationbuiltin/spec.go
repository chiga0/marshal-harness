// Package verificationbuiltin owns the closed, pathless verifier-builtin
// namespace. It contains admission only; execution remains inside the
// independent verification candidate isolate.
package verificationbuiltin

import (
	"errors"
	"strings"

	"github.com/chiga0/marshal-harness/internal/domain"
)

const (
	ReservedPrefix  = "marshal-builtin:"
	TaskSpecV1      = "marshal-builtin:contract-task-spec:v1"
	DeliverableMark = "deliverable:"
	ReasonDenied    = "contract-builtin-denied"
)

// Plan is the normalized, pathless input accepted by the verifier. PathGlob
// never belongs in argv or CommandRecord; the executor uses it only inside the
// already-created candidate isolate.
type Plan struct {
	DeliverableID string
	PathGlob      string
}

// Preflight validates every reserved command before planning side effects.
// Ordinary commands are deliberately ignored byte-for-byte.
func Preflight(task domain.TaskSpec) error {
	seen := make(map[string]struct{})
	for _, command := range task.Acceptance.Commands {
		plan, reserved, err := Parse(command, task.Deliverables)
		if err != nil {
			return err
		}
		if !reserved {
			continue
		}
		if _, duplicate := seen[plan.DeliverableID]; duplicate {
			return errors.New(ReasonDenied)
		}
		seen[plan.DeliverableID] = struct{}{}
	}
	return nil
}

// Parse recognizes the permanently reserved namespace. Every reserved but
// non-exact shape is denied and must never fall through to an external runner.
func Parse(command domain.TaskCommand, deliverables []domain.TaskDeliverable) (Plan, bool, error) {
	if len(command.Argv) == 0 || !strings.HasPrefix(command.Argv[0], ReservedPrefix) {
		return Plan{}, false, nil
	}
	if len(command.Argv) != 2 || command.Argv[0] != TaskSpecV1 || !strings.HasPrefix(command.Argv[1], DeliverableMark) {
		return Plan{}, true, errors.New(ReasonDenied)
	}
	id := strings.TrimPrefix(command.Argv[1], DeliverableMark)
	if id == "" || domain.ValidateID(id) != nil || command.CWD != "." || !command.Required || (command.BaselinePolicy != "" && command.BaselinePolicy != "none") {
		return Plan{}, true, errors.New(ReasonDenied)
	}
	var matched *domain.TaskDeliverable
	for index := range deliverables {
		if deliverables[index].ID != id {
			continue
		}
		if matched != nil {
			return Plan{}, true, errors.New(ReasonDenied)
		}
		matched = &deliverables[index]
	}
	if matched == nil || !matched.Required || matched.PathGlob == "" || (matched.MinimumCount != 0 && matched.MinimumCount != 1) {
		return Plan{}, true, errors.New(ReasonDenied)
	}
	return Plan{DeliverableID: id, PathGlob: matched.PathGlob}, true, nil
}
