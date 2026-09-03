package verificationbuiltin

import (
	"testing"

	"github.com/chiga0/marshal-harness/internal/domain"
)

func TestPreflightReservedNamespaceIsClosed(t *testing.T) {
	deliverable := domain.TaskDeliverable{ID: "task-spec", Required: true, PathGlob: "out/task.json", MinimumCount: 1}
	valid := domain.TaskCommand{Argv: []string{TaskSpecV1, "deliverable:task-spec"}, CWD: ".", Required: true, BaselinePolicy: "none"}
	if plan, reserved, err := Parse(valid, []domain.TaskDeliverable{deliverable}); err != nil || !reserved || plan.DeliverableID != "task-spec" || plan.PathGlob != "out/task.json" {
		t.Fatalf("valid parse = %+v reserved=%t err=%v", plan, reserved, err)
	}
	ordinary := domain.TaskCommand{Argv: []string{"sh", "-c", "true"}, CWD: ".", Required: true}
	if _, reserved, err := Parse(ordinary, nil); err != nil || reserved {
		t.Fatalf("ordinary command changed: reserved=%t err=%v", reserved, err)
	}
	cases := []domain.TaskCommand{
		{Argv: []string{"marshal-builtin:unknown:v1", "deliverable:task-spec"}, CWD: ".", Required: true},
		{Argv: []string{TaskSpecV1}, CWD: ".", Required: true},
		{Argv: []string{TaskSpecV1, "task-spec"}, CWD: ".", Required: true},
		{Argv: []string{TaskSpecV1, "deliverable:task-spec", "extra"}, CWD: ".", Required: true},
		{Argv: []string{TaskSpecV1, "deliverable:task-spec"}, CWD: "sub", Required: true},
		{Argv: []string{TaskSpecV1, "deliverable:task-spec"}, CWD: ".", Required: false},
		{Argv: []string{TaskSpecV1, "deliverable:task-spec"}, CWD: ".", Required: true, BaselinePolicy: "always"},
	}
	for index, command := range cases {
		if _, reserved, err := Parse(command, []domain.TaskDeliverable{deliverable}); !reserved || err == nil || err.Error() != ReasonDenied {
			t.Fatalf("case %d reserved=%t err=%v", index, reserved, err)
		}
	}
}

func TestPreflightRejectsAmbiguousOrOptionalDeliverables(t *testing.T) {
	command := domain.TaskCommand{ID: "validate", Argv: []string{TaskSpecV1, "deliverable:task-spec"}, CWD: ".", Required: true, BaselinePolicy: "none"}
	cases := [][]domain.TaskDeliverable{
		nil,
		{{ID: "task-spec", Required: false, PathGlob: "out/task.json", MinimumCount: 1}},
		{{ID: "task-spec", Required: true, PathGlob: "", MinimumCount: 1}},
		{{ID: "task-spec", Required: true, PathGlob: "out/*.json", MinimumCount: 2}},
		{{ID: "task-spec", Required: true, PathGlob: "a.json", MinimumCount: 1}, {ID: "task-spec", Required: true, PathGlob: "b.json", MinimumCount: 1}},
	}
	for index, deliverables := range cases {
		if _, _, err := Parse(command, deliverables); err == nil {
			t.Fatalf("case %d unexpectedly accepted", index)
		}
	}
	task := domain.TaskSpec{Acceptance: domain.TaskAcceptance{Commands: []domain.TaskCommand{command, command}}, Deliverables: []domain.TaskDeliverable{{ID: "task-spec", Required: true, PathGlob: "out/task.json", MinimumCount: 1}}}
	if err := Preflight(task); err == nil || err.Error() != ReasonDenied {
		t.Fatalf("duplicate reference error = %v", err)
	}
}
