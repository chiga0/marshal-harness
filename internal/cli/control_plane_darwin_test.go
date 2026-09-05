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
)

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
