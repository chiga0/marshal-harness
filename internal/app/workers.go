package app

import (
	"github.com/chiga0/marshal-harness/internal/adapter"
	"github.com/chiga0/marshal-harness/internal/adapter/opencode"
	"github.com/chiga0/marshal-harness/internal/adapter/pi"
	"github.com/chiga0/marshal-harness/internal/adapter/qwen"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/port"
)

// Worker configuration outcomes are fixed, structured codes. They never embed
// executable paths, environment values, or underlying provider errors, so
// doctor output and selection evidence cannot leak local environment details.
const (
	WorkerOutcomeNotConfigured        = "not-configured"
	WorkerOutcomeRegistered           = "registered"
	WorkerOutcomeInvalidConfiguration = "invalid-configuration"
)

// WorkerConfiguration is the structured, provider-neutral outcome of one
// frozen adapter binding. It intentionally carries no executable path, no
// error, and no secret.
type WorkerConfiguration struct {
	AdapterID           string `json:"adapterId"`
	EnvironmentVariable string `json:"environmentVariable"`
	Configured          bool   `json:"configured"`
	Registered          bool   `json:"registered"`
	Outcome             string `json:"outcome"`
}

// workerBinding freezes the only adapter-to-environment mapping Marshal
// supports. Order here is the order Configurations reports; it never changes
// implicitly and Marshal never searches PATH or matches similar names.
type workerBinding struct {
	adapterID           string
	environmentVariable string
	construct           func(executable string, validator *contract.Validator) (port.WorkerAdapter, error)
}

var workerBindings = []workerBinding{
	{
		adapterID:           "opencode",
		environmentVariable: "MARSHAL_OPENCODE_PATH",
		construct: func(executable string, validator *contract.Validator) (port.WorkerAdapter, error) {
			return opencode.New(executable, validator)
		},
	},
	{
		adapterID:           "qwen",
		environmentVariable: "MARSHAL_QWEN_PATH",
		construct: func(executable string, validator *contract.Validator) (port.WorkerAdapter, error) {
			return qwen.New(executable, validator)
		},
	},
	{
		adapterID:           "pi",
		environmentVariable: "MARSHAL_PI_PATH",
		construct: func(executable string, validator *contract.Validator) (port.WorkerAdapter, error) {
			return pi.New(executable, validator)
		},
	},
}

// WorkerRuntime assembles the provider-neutral local Worker runtime shared by
// `task plan`, `task run`, and `doctor`. It constructs concrete adapters from
// explicit environment values but never probes, executes, searches PATH, reads
// os.Environ, or writes files; the caller supplies the environment lookup.
type WorkerRuntime struct {
	validator      *contract.Validator
	registry       *adapter.Registry
	selector       *adapter.Selector
	configurations []WorkerConfiguration
}

// NewWorkerRuntime builds the shared runtime from the caller-provided
// environment lookup. A nil lookup is fail-closed. An invalid or unconfigured
// adapter binding is recorded with a structured outcome and skipped so that
// explicit fallback can continue; only base initialization or invariant errors
// abort construction, always with a fixed, non-leaking message.
func NewWorkerRuntime(getenv func(string) string) (*WorkerRuntime, error) {
	if getenv == nil {
		return nil, port.Permanentf("worker runtime: nil environment lookup")
	}
	validator, err := contract.NewValidator()
	if err != nil {
		return nil, port.Permanentf("worker runtime: initialize contract validator")
	}
	registry := adapter.NewRegistry()
	configurations := make([]WorkerConfiguration, 0, len(workerBindings))
	for _, binding := range workerBindings {
		configuration := WorkerConfiguration{
			AdapterID:           binding.adapterID,
			EnvironmentVariable: binding.environmentVariable,
		}
		executable := getenv(binding.environmentVariable)
		if executable == "" {
			configuration.Outcome = WorkerOutcomeNotConfigured
			configurations = append(configurations, configuration)
			continue
		}
		configuration.Configured = true
		worker, constructErr := binding.construct(executable, validator)
		if constructErr != nil {
			// Deliberately discard the executable, the environment value, and
			// the underlying error: none may be stored or echoed.
			configuration.Outcome = WorkerOutcomeInvalidConfiguration
			configurations = append(configurations, configuration)
			continue
		}
		if registerErr := registry.Register(worker); registerErr != nil {
			return nil, port.Permanentf("worker runtime: invariant violation registering adapter")
		}
		configuration.Registered = true
		configuration.Outcome = WorkerOutcomeRegistered
		configurations = append(configurations, configuration)
	}
	selector, err := adapter.NewSelector(registry)
	if err != nil {
		return nil, port.Permanentf("worker runtime: initialize adapter selector")
	}
	return &WorkerRuntime{
		validator:      validator,
		registry:       registry,
		selector:       selector,
		configurations: configurations,
	}, nil
}

// Validator returns the shared contract validator.
func (r *WorkerRuntime) Validator() *contract.Validator { return r.validator }

// Registry returns the shared adapter registry.
func (r *WorkerRuntime) Registry() *adapter.Registry { return r.registry }

// Selector returns the adapter selector bound to the shared registry.
func (r *WorkerRuntime) Selector() *adapter.Selector { return r.selector }

// Configurations returns a clone of the structured binding outcomes in the
// frozen order. Mutating the returned slice cannot affect the runtime.
func (r *WorkerRuntime) Configurations() []WorkerConfiguration {
	result := make([]WorkerConfiguration, len(r.configurations))
	copy(result, r.configurations)
	return result
}
