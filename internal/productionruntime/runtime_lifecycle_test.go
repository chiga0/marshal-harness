package productionruntime

import (
	"context"
	"io"
	"reflect"
	goruntime "runtime"
	"testing"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/resultingress"
)

type orderedRuntimeCloser struct {
	name  string
	order *[]string
}

func (closer orderedRuntimeCloser) Close() error {
	*closer.order = append(*closer.order, closer.name)
	return nil
}

var _ io.Closer = orderedRuntimeCloser{}

type blockingStatusBridge struct {
	lock       *testOwnerLock
	configured PiProfile
	entered    chan struct{}
	release    chan struct{}
}

func (bridge *blockingStatusBridge) VerifyAgentProfile(_ context.Context, _ resultingress.CurrentOwnerLockVerifier, _ resultingress.ControlOwnerAcquisition, _ OwnerProjection, expected PiProfile) error {
	if bridge.lock == nil || !bridge.lock.inCritical || expected != bridge.configured {
		return application.NewError("test-bridge", application.ReasonBridgeUnavailable)
	}
	close(bridge.entered)
	<-bridge.release
	if bridge.lock.closed {
		return application.NewError("test-bridge", application.ReasonOwnerUnavailable)
	}
	return nil
}

func (*blockingStatusBridge) StartPreparedRun(context.Context, resultingress.CurrentOwnerLockVerifier, resultingress.ControlOwnerAcquisition, OwnerProjection, PiProfile, application.PreparedRunStart) error {
	return application.NewError("test-bridge", application.ReasonBridgeUnavailable)
}

func TestProductionRuntimeReportsReadyAndClosesResourcesInReverse(t *testing.T) {
	controller, lock, _, _, _ := testComponents(t)
	var order []string
	runtime, err := newProductionRuntime(controller,
		orderedRuntimeCloser{name: "store", order: &order},
		orderedRuntimeCloser{name: "bridge", order: &order},
	)
	if application.HasReason(err, application.ReasonPlatformProfileUnavailable) {
		t.Skip("darwin/arm64-only runtime")
	}
	if err != nil {
		t.Fatal(err)
	}
	status, err := runtime.Status(context.Background(), application.StatusRequest{})
	if err != nil || status.Availability != application.AvailabilityReady || status.ReasonCode != "" {
		t.Fatalf("production status=%#v err=%v", status, err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"bridge", "store"}) || !lock.closed {
		t.Fatalf("close order=%v ownerClosed=%t", order, lock.closed)
	}
	if err := runtime.Close(); err != nil || !reflect.DeepEqual(order, []string{"bridge", "store"}) {
		t.Fatalf("idempotent Close err=%v order=%v", err, order)
	}
	if _, err := runtime.Status(context.Background(), application.StatusRequest{}); !application.HasReason(err, application.ReasonBridgeUnavailable) {
		t.Fatalf("Status after Close err=%v", err)
	}
}

func TestProductionRuntimeConstructionFailureClosesTransferredResourcesAndOwner(t *testing.T) {
	controller, lock, _, _, _ := testComponents(t)
	var order []string
	lock.closeOrder = &order
	runtime, err := newProductionRuntime(controller,
		orderedRuntimeCloser{name: "first", order: &order},
		nil,
		orderedRuntimeCloser{name: "last", order: &order},
	)
	if runtime != nil || !application.HasReason(err, application.ReasonInvalidRequest) {
		t.Fatalf("runtime=%#v err=%v", runtime, err)
	}
	if !reflect.DeepEqual(order, []string{"last", "first", "owner"}) || !lock.closed || lock.isClaimed {
		t.Fatalf("close order=%v ownerClosed=%t ownerClaimed=%t", order, lock.closed, lock.isClaimed)
	}
}

func TestInflightStatusBlocksCloseUntilOperationReturns(t *testing.T) {
	controller, lock, _, _, profile := testComponents(t)
	bridge := &blockingStatusBridge{lock: lock, configured: profile, entered: make(chan struct{}), release: make(chan struct{})}
	controller.bridge = bridge
	runtime, err := newProductionRuntime(controller)
	if application.HasReason(err, application.ReasonPlatformProfileUnavailable) {
		t.Skip("darwin/arm64-only runtime")
	}
	if err != nil {
		t.Fatal(err)
	}
	type statusResult struct {
		status application.StatusProjection
		err    error
	}
	statusDone := make(chan statusResult, 1)
	go func() {
		status, statusErr := runtime.Status(context.Background(), application.StatusRequest{})
		statusDone <- statusResult{status: status, err: statusErr}
	}()
	<-bridge.entered

	closeStarted := make(chan struct{})
	closeDone := make(chan error, 1)
	go func() {
		close(closeStarted)
		closeDone <- runtime.Close()
	}()
	<-closeStarted

	// A pending writer makes TryRLock fail even while another reader holds
	// the lock. This deterministically proves Close reached the lifecycle
	// barrier and is waiting for the real Status operation; no timer is used.
	writerPending := false
	for attempts := 0; attempts < 10_000; attempts++ {
		if !runtime.mu.TryRLock() {
			writerPending = true
			break
		}
		runtime.mu.RUnlock()
		goruntime.Gosched()
	}
	if !writerPending {
		close(bridge.release)
		<-statusDone
		<-closeDone
		t.Fatal("Close never reached the lifecycle write barrier")
	}
	close(bridge.release)
	result := <-statusDone
	if result.err != nil || result.status.Availability != application.AvailabilityReady {
		t.Fatalf("status=%#v err=%v", result.status, result.err)
	}
	if err := <-closeDone; err != nil || !lock.closed {
		t.Fatalf("Close err=%v ownerClosed=%t", err, lock.closed)
	}
}
