//go:build darwin && arm64

package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/fixedcontrolplane"
	"github.com/chiga0/marshal-harness/internal/selfidentity"
)

func TestControlPlaneRequestContextPreservesIdentityAndDrainOwnership(t *testing.T) {
	identity := selfidentity.LocalSelfIdentityObservationV2{ObservationDigest: "sha256:" + strings.Repeat("a", 64)}
	parent, stopService := context.WithTimeout(context.WithValue(context.Background(), localDogfoodObservationContextKey{}, identity), time.Minute)
	defer stopService()
	request, cancelRequest := newControlPlaneRequestContext(parent)
	defer cancelRequest()
	if got := localDogfoodObservation(request); got == nil || *got != identity {
		t.Fatal("request lost admitted process identity")
	}
	if _, ok := request.Deadline(); ok {
		t.Fatal("request inherited service deadline instead of owning its drain lifetime")
	}
	stopService()
	if request.Err() != nil || request.Done() == nil {
		t.Fatal("service stop canceled request before drain")
	}
	cancelRequest()
	if request.Err() != context.Canceled {
		t.Fatal("explicit drain cancellation did not stop request")
	}
	if got := localDogfoodObservation(request); got == nil || *got != identity {
		t.Fatal("drain cancellation discarded identity")
	}
	ungated, cancelUngated := newControlPlaneRequestContext(context.Background())
	defer cancelUngated()
	if localDogfoodObservation(ungated) != nil {
		t.Fatal("request context invented an identity")
	}
}

func TestControlPlaneCancelDerivesOneStableIntentIdentity(t *testing.T) {
	input := controlPlaneCurrentInput{
		current:    application.CurrentRunRequest{RunID: "run:cancel", AttemptID: "attempt:cancel", ExpectedSequence: 3, ExpectedAuthorityHead: "sha256:" + strings.Repeat("a", 64)},
		requestKey: "client:cancel:stable-key",
	}
	first, err := controlPlaneCancelRequest(input)
	if err != nil || first.Validate() != nil || first.CurrentRunRequest != input.current {
		t.Fatalf("cancel request: %+v, %v", first, err)
	}
	replay, err := controlPlaneCancelRequest(input)
	if err != nil || replay != first {
		t.Fatalf("cancel replay: %+v, %v", replay, err)
	}
	input.requestKey = "client:cancel:different-key"
	different, err := controlPlaneCancelRequest(input)
	if err != nil || different.RequestID == first.RequestID {
		t.Fatal("different transport keys shared a stop intent identity")
	}
	input.current.ExpectedAuthorityHead = "forged"
	if _, err := controlPlaneCancelRequest(input); err == nil {
		t.Fatal("invalid current Run binding admitted")
	}
}

func TestControlPlaneCancelRejectsAuthorityOverridesBeforeConnection(t *testing.T) {
	for _, args := range [][]string{nil, {"--pid", "123"}, {"--actor", "root"}, {"--request-id", "free-form"}, {"--reason", "deadline"}} {
		var stdout, stderr bytes.Buffer
		if exit := runControlPlaneCancel(context.Background(), args, &stdout, &stderr); exit != ExitUsage || stdout.Len() != 0 {
			t.Fatalf("cancel %v: exit=%d stdout=%q", args, exit, stdout.String())
		}
	}
}

func TestControlPlaneCollectExportsOnlyVerifiedStoppedClassification(t *testing.T) {
	var stdout, stderr bytes.Buffer
	stopped := application.NewError("collect-run-result", application.ReasonRunStopped)
	if exit := writeControlPlaneCollectResult(&stdout, &stderr, fixedcontrolplane.CollectRunClientResult{}, stopped); exit != ExitFailure || stdout.String() != "{\"disposition\":\"stopped\",\"reasonCode\":\"run-stopped\"}\n" || stderr.Len() != 0 {
		t.Fatalf("stop classification: exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
	for _, test := range []struct {
		result fixedcontrolplane.CollectRunClientResult
		err    error
	}{
		{err: errors.New("run-stopped")},
		{result: fixedcontrolplane.CollectRunClientResult{Projection: application.CollectedRunProjection{Run: application.RunProjection{RunID: "run:partial"}}}, err: stopped},
	} {
		stdout.Reset()
		stderr.Reset()
		if exit := writeControlPlaneCollectResult(&stdout, &stderr, test.result, test.err); exit != ExitFailure || stdout.Len() != 0 {
			t.Fatalf("unverified stopped reply emitted: exit=%d stdout=%q", exit, stdout.String())
		}
	}
}

func TestParseControlPlaneDeadlineRequiresCanonicalFrozenUTC(t *testing.T) {
	now := time.Date(2026, 9, 3, 1, 2, 3, 0, time.UTC)
	want := now.Add(5 * time.Minute)
	raw := want.Format(time.RFC3339Nano)
	got, err := parseControlPlaneDeadline(raw, now)
	if err != nil || !got.Equal(want) {
		t.Fatalf("deadline=%v err=%v", got, err)
	}
	for _, invalid := range []string{
		"", now.Format(time.RFC3339Nano), now.Add(11 * time.Minute).Format(time.RFC3339Nano),
		"2026-09-03T09:07:03+08:00",
	} {
		if _, err := parseControlPlaneDeadline(invalid, now); err == nil {
			t.Fatalf("invalid deadline %q admitted", invalid)
		}
	}
}

func TestWriteControlPlaneRequestFailureRedactsRawError(t *testing.T) {
	var output bytes.Buffer
	writeControlPlaneRequestFailure(&output, errors.Join(errors.New("/private/secret/provider"), application.NewError("start-run", application.ReasonRecoveryRequired)))
	if got := output.String(); got != "control-plane request failed: operation=start-run reasonCode=recovery-required\n" {
		t.Fatalf("output=%q", got)
	}
	if strings.Contains(output.String(), "secret") {
		t.Fatal("raw error escaped stable diagnostic boundary")
	}
}

type cyclicControlPlaneError struct{}

func (*cyclicControlPlaneError) Error() string     { panic("raw error must not be formatted") }
func (err *cyclicControlPlaneError) Unwrap() error { return err }

func TestWriteControlPlaneRequestFailurePreservesBoundedTypedCauses(t *testing.T) {
	outer := application.NewError("reconcile-start-run-delivery", application.ReasonAuthorityConflict)
	start := application.NewError("prepare-run-start", application.ReasonRecoveryRequired)
	reconcile := application.NewError("reconcile-start-run-owner-read", application.ReasonOwnerNotCurrent)
	var output bytes.Buffer
	writeControlPlaneRequestFailure(&output, errors.Join(outer, start, fmt.Errorf("/private/secret: %w", reconcile), start))
	want := "control-plane request failed: operation=reconcile-start-run-delivery reasonCode=authority-conflict\n" +
		"control-plane request failed: operation=prepare-run-start reasonCode=recovery-required\n" +
		"control-plane request failed: operation=reconcile-start-run-owner-read reasonCode=production-owner-not-current\n"
	if output.String() != want {
		t.Fatalf("output=%q", output.String())
	}
	output.Reset()
	writeControlPlaneRequestFailure(&output, &cyclicControlPlaneError{})
	if output.String() != "control-plane request failed: reasonCode=transport-failure\n" {
		t.Fatalf("cycle output=%q", output.String())
	}
	output.Reset()
	var causes []error
	for index := 0; index < 40; index++ {
		causes = append(causes, application.NewError(fmt.Sprintf("stage-%d", index), application.ReasonAuthorityConflict))
	}
	writeControlPlaneRequestFailure(&output, errors.Join(causes...))
	if strings.Count(output.String(), "\n") != 8 {
		t.Fatalf("diagnostic output exceeded fixed budget: %q", output.String())
	}
}

func TestWriteControlPlaneRequestFailureRejectsInvalidTypedFields(t *testing.T) {
	for _, err := range []error{
		(*application.Error)(nil),
		application.NewError("/private/secret", application.ReasonAuthorityConflict),
		application.NewError("stage\nforged", application.ReasonAuthorityConflict),
		application.NewError(strings.Repeat("a", 97), application.ReasonAuthorityConflict),
		application.NewError("stage", application.ReasonCode("secret")),
	} {
		var output bytes.Buffer
		writeControlPlaneRequestFailure(&output, err)
		if output.String() != "control-plane request failed: reasonCode=transport-failure\n" {
			t.Fatalf("invalid typed error emitted: %q", output.String())
		}
	}
}

func TestSealedRunOpenDiagnosticsDoNotChangeApplicationReason(t *testing.T) {
	_, openErr := (&sealedRepositoryApplication{closed: true}).openRun(context.Background(), "run:closed")
	var phase *sealedRunOpenError
	if !application.HasReason(openErr, application.ReasonBridgeUnavailable) || !errors.As(openErr, &phase) || phase.stage != "sealed-run-open" {
		t.Fatal("actual openRun failure lost original reason or phase")
	}
	raw := errors.New("/private/secret")
	wrapped := &sealedRunOpenError{stage: "sealed-run-open-worktree", cause: raw}
	var typed *application.Error
	if !errors.Is(wrapped, raw) || errors.As(wrapped, &typed) {
		t.Fatal("diagnostic wrapper changed application error classification")
	}
	var output bytes.Buffer
	writeControlPlaneRequestFailure(&output, wrapped)
	if output.String() != "control-plane request failed: operation=sealed-run-open-worktree reasonCode=composition-failure\n" {
		t.Fatalf("output=%q", output.String())
	}
	wrapped.cause = application.NewError("sealed-repository-application", application.ReasonBridgeUnavailable)
	if !application.HasReason(wrapped, application.ReasonBridgeUnavailable) {
		t.Fatal("diagnostic wrapper masked original typed reason")
	}
}

func TestSealedRepositoryOpenStageReturnsOnlyStablePhase(t *testing.T) {
	err := fmt.Errorf("sealed repository application: resolve Pi runtime: %w", errors.New("/private/secret/runtime"))
	if got := sealedRepositoryOpenStage(err); got != "resolve-Pi-runtime" {
		t.Fatalf("stage=%q", got)
	}
	if got := sealedRepositoryOpenStage(errors.New("/private/secret/unknown")); got != "unknown" {
		t.Fatalf("unknown stage=%q", got)
	}
	err = fmt.Errorf("sealed repository application: open repository session: repository session: seal prepared execution: %w", errors.New("/private/secret/control"))
	if got := sealedRepositoryOpenStage(err); got != "repository-session-seal-prepared-execution" {
		t.Fatalf("nested stage=%q", got)
	}
}

func TestDrainControlPlaneRequestsDrainsBeforeCancel(t *testing.T) {
	var requests sync.WaitGroup
	requests.Add(1)
	canceled := false
	requests.Done()
	if !drainControlPlaneRequests(&requests, func() { canceled = true }, time.Second, time.Second) {
		t.Fatal("already drained requests did not complete")
	}
	if canceled {
		t.Fatal("application cancel ran before graceful drain")
	}
}

func TestDrainControlPlaneRequestsCancelsOnlyAfterDrainDeadline(t *testing.T) {
	var requests sync.WaitGroup
	requests.Add(1)
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	defer cancelRequest()
	workerDone := make(chan struct{})
	go func() {
		<-requestCtx.Done()
		requests.Done()
		close(workerDone)
	}()
	if !drainControlPlaneRequests(&requests, cancelRequest, 10*time.Millisecond, time.Second) {
		t.Fatal("requests did not stop during cancel window")
	}
	select {
	case <-workerDone:
	default:
		t.Fatal("application cancel was not delivered")
	}
}
