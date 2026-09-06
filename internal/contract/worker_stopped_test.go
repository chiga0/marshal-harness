package contract

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/domain"
)

func TestWorkerStoppedSchemaClosesEvidenceAndActor(t *testing.T) {
	validator := mustValidator(t)
	fixture := func() map[string]any {
		payload := map[string]any{"terminalReason": "aborted-by-operator"}
		for _, field := range []string{"stopIntentDigest", "stopRequestDigest", "originalRunAuthorityHead", "terminalizationBarrierFactDigest", "processTerminalFactDigest", "allocationTerminatedFactDigest", "supervisorClosedFactDigest", "cleanupReleasedFactDigest"} {
			payload[field] = "sha256:" + strings.Repeat("a", 64)
		}
		return map[string]any{"apiVersion": "marshal.dev/v1alpha1", "kind": "RunEvent", "eventId": "event-stop", "runId": "run-1", "attemptId": "attempt-1", "sequence": 4, "type": "worker.stopped", "stateFrom": "RUNNING", "stateTo": "BLOCKED", "timestamp": "2026-09-06T00:00:00Z", "actor": map[string]any{"type": "system", "id": "marshal-core"}, "payload": payload}
	}
	for _, reason := range []string{"aborted-by-operator", "attempt-deadline-exceeded", "run-deadline-exceeded"} {
		document := fixture()
		document["payload"].(map[string]any)["terminalReason"] = reason
		data, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		if err := validator.Validate(domain.KindRunEvent, data); err != nil {
			t.Fatalf("valid reason %s: %v", reason, err)
		}
	}
	for name, mutate := range map[string]func(map[string]any){
		"missing-attempt": func(d map[string]any) { delete(d, "attemptId") },
		"missing-actor":   func(d map[string]any) { delete(d, "actor") },
		"worker-actor":    func(d map[string]any) { d["actor"].(map[string]any)["type"] = "worker" },
		"claimed-core":    func(d map[string]any) { d["actor"].(map[string]any)["id"] = "agent" },
		"wrong-source":    func(d map[string]any) { d["stateFrom"] = "READY" },
		"success-target":  func(d map[string]any) { d["stateTo"] = "ACCEPTED" },
		"free-reason":     func(d map[string]any) { d["payload"].(map[string]any)["terminalReason"] = "killed" },
		"pid-authority":   func(d map[string]any) { d["payload"].(map[string]any)["pid"] = 123 },
		"missing-cleanup": func(d map[string]any) { delete(d["payload"].(map[string]any), "cleanupReleasedFactDigest") },
		"invalid-digest":  func(d map[string]any) { d["payload"].(map[string]any)["stopIntentDigest"] = "claimed" },
		"invalid-time":    func(d map[string]any) { d["timestamp"] = "now" },
	} {
		t.Run(name, func(t *testing.T) {
			document := fixture()
			mutate(document)
			data, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if err := validator.Validate(domain.KindRunEvent, data); err == nil {
				t.Fatal("forged stop event accepted")
			}
		})
	}
}
