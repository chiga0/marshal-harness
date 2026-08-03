// Package app contains Marshal application services. CLI parsing and provider
// implementations remain outside this package.
package app

import (
	"fmt"

	"github.com/chiga0/marshal-harness/internal/adapter"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
)

// App is the composition root for deterministic Core services available in
// the current milestone.
type App struct {
	contracts *contract.Validator
	workers   *adapter.Registry
}

// New constructs the application and compiles all durable contracts.
func New() (*App, error) {
	validator, err := contract.NewValidator()
	if err != nil {
		return nil, fmt.Errorf("initialize contract validator: %w", err)
	}
	return &App{
		contracts: validator,
		workers:   adapter.NewRegistry(),
	}, nil
}

// ValidateContract validates a document against an explicitly selected kind.
func (a *App) ValidateContract(kind domain.Kind, data []byte) error {
	return a.contracts.Validate(kind, data)
}

// ValidateDocument detects and validates a document's kind.
func (a *App) ValidateDocument(data []byte) (domain.Record, error) {
	return a.contracts.ValidateDocument(data)
}

// ContractCount reports how many durable record schemas are compiled.
func (a *App) ContractCount() int {
	return len(contract.Descriptors())
}

// AdapterCount reports registered Worker adapters. It remains zero in
// Milestone 0 because real and fake execution arrive in later milestones.
func (a *App) AdapterCount() int {
	return len(a.workers.IDs())
}
