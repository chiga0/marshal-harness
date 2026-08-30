package productionruntime

import (
	"context"
	"os"
	"testing"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/processcontrol"
	"github.com/chiga0/marshal-harness/internal/resultingress"
	"github.com/chiga0/marshal-harness/internal/sandboxbridge"
)

type exactFactoryTestBridge struct{ testBridge }

func (*exactFactoryTestBridge) BindExactProcessRuntime(sandboxbridge.ExactProcessRuntime) error {
	return nil
}

func (*exactFactoryTestBridge) BindExactAllocationRuntime(sandboxbridge.ExactAllocationRuntime) error {
	return nil
}

func validDarwinFactoryConfig(t *testing.T) DarwinLocalDogfoodFactoryConfig {
	t.Helper()
	acquisition := testAcquisition()
	ownerDirectory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ownerDirectory.Close() })
	store, err := resultingress.OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	profile, err := NewPi0843Profile("/fixed/bin/pi", "/fixed/bin/node", runtimeTestDigest)
	if err != nil {
		t.Fatal(err)
	}
	return DarwinLocalDogfoodFactoryConfig{
		Authority:               &testAuthority{},
		Bridge:                  &exactFactoryTestBridge{},
		OwnerDirectory:          ownerDirectory,
		OwnerScope:              acquisition.Scope,
		Store:                   store,
		ExpectedOwnerEpoch:      0,
		ExpectedOwnerFactDigest: "",
		Acquisition:             acquisition,
		FixedMarshalPath:        acquisition.OwnerBinary.CanonicalPath,
		Profile:                 profile,
	}
}

func TestDarwinFactoryValidationRequiresExactRuntimes(t *testing.T) {
	config := validDarwinFactoryConfig(t)
	err := validateDarwinFactoryConfig(config, sandboxbridge.ExactProcessRuntime{}, sandboxbridge.ExactAllocationRuntime{})
	if !application.HasReason(err, application.ReasonCompositionIncomplete) {
		t.Fatalf("err=%v, want composition-incomplete", err)
	}
}

func TestDarwinFactoryValidationRejectsUnmarkedBridge(t *testing.T) {
	config := validDarwinFactoryConfig(t)
	exactProcess, exactAllocation := validExactRuntimePair()
	err := validateDarwinFactoryConfig(config, exactProcess, exactAllocation)
	if !application.HasReason(err, application.ReasonCompositionIncomplete) {
		t.Fatalf("err=%v, want composition-incomplete", err)
	}
}

func TestDarwinFactoryValidationRejectsScopeMismatch(t *testing.T) {
	config := validDarwinFactoryConfig(t)
	config.OwnerScope.RepositoryIdentityDigest = runtimeSuccessDigest
	exactProcess, exactAllocation := validExactRuntimePair()
	err := validateDarwinFactoryConfig(config, exactProcess, exactAllocation)
	if !application.HasReason(err, application.ReasonInvalidRequest) {
		t.Fatalf("err=%v, want invalid-request", err)
	}
}

func validExactRuntimePair() (sandboxbridge.ExactProcessRuntime, sandboxbridge.ExactAllocationRuntime) {
	exactProcess := sandboxbridge.ExactProcessRuntime{
		Resolve: func(context.Context, sandboxbridge.ExactProcessAttempt) (*processcontrol.Coordinator, sandboxbridge.DurableProcessAuthority, error) {
			return nil, sandboxbridge.DurableProcessAuthority{}, nil
		},
		Retain: func(sandboxbridge.ExactProcessAttempt, *processcontrol.Process, error) {},
	}
	exactAllocation := sandboxbridge.ExactAllocationRuntime{
		Resolve: func(context.Context, sandboxbridge.ExactProcessAttempt) (sandboxbridge.ExactAllocationResolution, error) {
			return sandboxbridge.ExactAllocationResolution{}, nil
		},
	}
	return exactProcess, exactAllocation
}
