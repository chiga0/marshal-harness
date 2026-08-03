package fake

import (
	"context"
	"os"
	"testing"

	"github.com/chiga0/marshal-harness/internal/domain"
)

func TestTranscriptAdapterIsDeterministic(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("testdata/success.json")
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := New(data)
	if err != nil {
		t.Fatal(err)
	}
	capability, err := adapter.Probe(context.Background())
	if err != nil || capability.Kind != domain.KindCapabilitySnapshot {
		t.Fatalf("Probe = %+v, %v", capability, err)
	}
	result, err := adapter.Run(context.Background(), domain.Record{})
	if err != nil || result.Kind != domain.KindWorkerResult {
		t.Fatalf("Run = %+v, %v", result, err)
	}
	events := adapter.Events()
	events[0][0] = 'X'
	if adapter.Events()[0][0] == 'X' {
		t.Fatal("Events exposed mutable transcript storage")
	}
}
