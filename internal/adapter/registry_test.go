package adapter

import (
	"context"
	"testing"

	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
)

type fakeWorker struct {
	id string
}

func (f fakeWorker) ID() string {
	return f.id
}

func (f fakeWorker) Probe(context.Context) (domain.Record, error) {
	return domain.Record{}, nil
}

func (f fakeWorker) Run(context.Context, domain.Record) (domain.Record, error) {
	return domain.Record{}, nil
}

var _ port.WorkerAdapter = fakeWorker{}

func TestRegistryUsesExactStableIDs(t *testing.T) {
	t.Parallel()

	registry := NewRegistry()
	worker := fakeWorker{id: "qwen"}
	if err := registry.Register(worker); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if _, err := registry.Resolve("qwen"); err != nil {
		t.Fatalf("Resolve(qwen) error = %v", err)
	}
	if _, err := registry.Resolve("qwen-code"); err == nil {
		t.Fatal("Resolve(qwen-code) performed an implicit fallback")
	}
	if err := registry.Register(worker); err == nil {
		t.Fatal("duplicate Register() unexpectedly succeeded")
	}
}

func TestRegistryRejectsNilAndEmptyAdapters(t *testing.T) {
	t.Parallel()

	registry := NewRegistry()
	if err := registry.Register(nil); err == nil {
		t.Fatal("Register(nil) unexpectedly succeeded")
	}
	if err := registry.Register(fakeWorker{id: "  "}); err == nil {
		t.Fatal("Register(empty ID) unexpectedly succeeded")
	}
}
