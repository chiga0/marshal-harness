//go:build darwin && arm64

package resultingress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/processsupervisor"
)

type fakeAttachedRebindV2 struct {
	observation processsupervisor.AttachObservationV2
	execute     func(processsupervisor.PreparedCommandV2) (processsupervisor.VerifiedCommandOutcomeV2, error)
}

func (s fakeAttachedRebindV2) Observation() (processsupervisor.AttachObservationV2, error) {
	return s.observation, nil
}
func (s fakeAttachedRebindV2) ExecutePreparedBindAuthority(_ context.Context, p processsupervisor.PreparedCommandV2) (processsupervisor.VerifiedCommandOutcomeV2, error) {
	return s.execute(p)
}

func testRebindObservationV2(t *testing.T, a processsupervisor.AttachAuthorityV2) processsupervisor.AttachObservationV2 {
	t.Helper()
	g, b := a.PreviousSupervisor.Generation, a.PreviousSupervisor.Binding
	var o processsupervisor.AttachObservationV2
	r := &o.Response
	r.SchemaVersion = processsupervisor.AttachObservationSchemaV2
	r.ProtocolRevision, r.LaunchChildProtocolRevision, r.MechanicsIdentity = g.ProtocolRevision, g.LaunchChildProtocolRevision, g.MechanicsIdentity
	r.Status, r.ReasonCode = "ok", "process-supervisor-attached"
	r.Authority, r.ObserverIdentity, r.ObservedAt = a, g.ObserverIdentity, time.Now().UTC().Format(time.RFC3339Nano)
	c := a.CurrentAcquisition
	request := map[string]any{"schemaVersion": processsupervisor.AttachSchemaV2, "protocolRevision": g.ProtocolRevision, "launchChildProtocolRevision": g.LaunchChildProtocolRevision,
		"mechanicsIdentity": g.MechanicsIdentity, "sessionNonceDigest": b.SessionNonceDigest, "core": processsupervisor.CoreIdentity{UID: c.OwnerUID, GID: c.OwnerGID, Process: c.OwnerProcess, Binary: c.OwnerBinary}, "authority": a}
	r.RequestDigest, _ = canonicalDigest(request)
	r.Handshake = processsupervisor.HandshakeResponseV2{SchemaVersion: g.HandshakeSchema, ProtocolRevision: g.ProtocolRevision, LaunchChildProtocolRevision: g.LaunchChildProtocolRevision, MechanicsIdentity: g.MechanicsIdentity,
		Status: "ok", ReasonCode: "process-supervisor-ready", SessionID: b.SessionID, SessionNonceDigest: b.SessionNonceDigest, OwnerEpoch: b.OwnerEpoch, CurrentAuthorityHead: b.CurrentAuthorityHead,
		CommandSequence: b.CommandSequence, CommandHead: b.CommandHead, JournalSequence: b.JournalSequence, JournalHead: b.JournalHead,
		SupervisorProcess: a.Supervisor, SupervisorBinary: b.FixedBinary, ControlSocket: b.ControlSocket, ControlFiles: b.ControlFiles, ObserverIdentity: g.ObserverIdentity, ObservedAt: r.ObservedAt}
	r.ResponseDigest, _ = canonicalDigest(r)
	o.Peer = processsupervisor.CoreIdentity{UID: b.UID, GID: b.GID, Process: a.Supervisor, Binary: b.FixedBinary}
	if o.Validate() != nil {
		t.Fatal("invalid independently constructed Attach fixture")
	}
	return o
}

// Extends the real durable bootstrap/start/resume test chain. Only the
// Supervisor peer is simulated; owner, intent, outcome and cold replay use RB1.
func testLauncherV2OwnerRebind(t *testing.T, fixture preparedExecutionFixture, state AttemptAuthorityState) {
	t.Helper()
	store := fixture.store
	prior, found, err := store.OpenOwner(state.Owner.Scope)
	if err != nil || !found {
		t.Fatal(err)
	}
	successor := prior.Acquisition
	successor.OwnerEpoch++
	successor.OwnerProcess = processsupervisor.ProcessIdentity{PID: 14002, BirthSeconds: 1700000020, BirthMicroseconds: 2, SessionID: 14002, ProcessGroupID: 14002}
	successor.ObservedAt = "2026-09-05T00:00:30Z"
	verifier := attemptOwnerVerifier{want: successor}
	if _, err := store.AcquireOwner(context.Background(), verifier, prior.Acquisition.OwnerEpoch, prior.FactDigest, successor); err != nil {
		t.Fatal(err)
	}
	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	calls, executes := 0, 0
	badObservation := true
	lostReply := false
	transport := func(ctx context.Context, options processsupervisor.AttachOptionsV2, fn func(attachedRebindSessionV2) error) error {
		calls++
		observation := testRebindObservationV2(t, options.Authority)
		if badObservation {
			observation.Response.Authority.Child.PID++
		}
		return options.OwnerVerifier.WithCurrentAttachOwnerV2(ctx, options.Authority, func() error {
			return fn(fakeAttachedRebindV2{observation: observation, execute: func(p processsupervisor.PreparedCommandV2) (processsupervisor.VerifiedCommandOutcomeV2, error) {
				executes++
				intent, err := NewSupervisorCommandIntentV2(p.Evidence())
				if err != nil {
					t.Fatal(err)
				}
				raw, err := os.ReadFile(store.ledgerPath())
				if err != nil {
					t.Fatal(err)
				}
				lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
				var fact supervisorCommandFact
				if json.Unmarshal(lines[len(lines)-1], &fact) != nil || fact.Intent != intent || fact.FactType != supervisorCommandIntentFactType || fact.ProtocolRevision != p.Evidence().PreCommand.Generation.CommandRecoveryRevision {
					t.Fatal("v2 command sent without its exact durable intent")
				}
				if lostReply {
					return processsupervisor.VerifiedCommandOutcomeV2{}, processsupervisor.ErrIntervention
				}
				e := testBindOutcomeV2(t, intent)
				return processsupervisor.VerifiedCommandOutcomeV2{Preparation: e.V2Preparation, JournalRequest: e.JournalRequest, PostCommand: supervisorSessionAnchorV2(e.PostCommand),
					Status: e.Disposition, ReasonCode: e.ReasonCode, ReceiptDigest: e.ReceiptDigest, ObservationDigest: e.ObservationDigest, CommandHead: e.CommandHead}, nil
			}})
		})
	}
	legacy := func(context.Context, processsupervisor.AttachOptions, func(AttachedRebindSession) error) error {
		t.Fatal("v2 recovery entered v1 transport")
		return nil
	}
	run := func() (AttemptAuthorityState, error) {
		return store.rebindOwnerSuccessorForAttachedRecoveryWithTransports(context.Background(), verifier, successor, state.Identity, directory, successor.OwnerBinary.CanonicalPath, legacy, transport)
	}
	if _, err := run(); err == nil || executes != 0 {
		t.Fatal("forged observation produced command")
	}
	bound, found, err := store.AttemptState(state.Identity)
	if err != nil || !found || bound.SupervisorPendingIntentDigest != "" || bound.SupervisorCommandSequence != state.SupervisorCommandSequence || bound.Owner.OwnerEpoch != successor.OwnerEpoch {
		t.Fatal("failed observation lost durable owner-only successor")
	}
	badObservation = false
	result, err := run()
	if err != nil || executes != 1 || result.SupervisorPendingIntentDigest != "" || result.SupervisorCommandSequence != state.SupervisorCommandSequence+1 || result.SupervisorBoundAuthorityHead != result.HeadDigest {
		t.Fatalf("v2 owner rebind: %v", err)
	}
	if result.SupervisorMechanicsAnchor.OwnerEpoch != state.SupervisorMechanicsAnchor.OwnerEpoch {
		t.Fatal("rebind rewrote predecessor mechanics owner")
	}
	repeated, err := run()
	if err != nil || !reflect.DeepEqual(repeated, result) || calls != 2 || executes != 1 {
		t.Fatal("idempotent owner recovery repeated transport")
	}
	reopened, err := OpenResultIngressStore(store.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	cold, found, err := reopened.AttemptState(state.Identity)
	if err != nil || !found || !reflect.DeepEqual(cold, result) {
		t.Fatalf("v2 rebind cold replay: %v", err)
	}
	// Until the descriptor-bound v2 recovery path classifies the old command,
	// neither same-owner retry nor a later owner may blindly repeat its effect.
	currentOwner, found, err := store.OpenOwner(result.Owner.Scope)
	if err != nil || !found {
		t.Fatal(err)
	}
	successor.OwnerEpoch++
	successor.OwnerProcess.PID++
	successor.OwnerProcess.SessionID, successor.OwnerProcess.ProcessGroupID = successor.OwnerProcess.PID, successor.OwnerProcess.PID
	successor.ObservedAt = "2026-09-05T00:00:40Z"
	verifier = attemptOwnerVerifier{want: successor}
	if _, err := store.AcquireOwner(context.Background(), verifier, currentOwner.Acquisition.OwnerEpoch, currentOwner.FactDigest, successor); err != nil {
		t.Fatal(err)
	}
	lostReply = true
	if _, err := run(); !errors.Is(err, processsupervisor.ErrIntervention) {
		t.Fatalf("lost response: %v", err)
	}
	pending, found, err := store.AttemptState(state.Identity)
	if err != nil || !found || pending.SupervisorPendingIntentDigest == "" || pending.SupervisorCommandSequence != result.SupervisorCommandSequence {
		t.Fatal("lost reply erased pending intent")
	}
	before, err := os.ReadFile(store.ledgerPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run(); !errors.Is(err, processsupervisor.ErrIntervention) {
		t.Fatal("unclassified pending repeated")
	}
	after, err := os.ReadFile(store.ledgerPath())
	if err != nil || !bytes.Equal(before, after) || calls != 3 || executes != 2 {
		t.Fatal("pending retry modified authority or repeated transport")
	}
}
