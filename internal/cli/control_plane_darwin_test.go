//go:build darwin && arm64

package cli

import (
	"bytes"
	"context"
	"errors"
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
