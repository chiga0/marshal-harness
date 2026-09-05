package processsupervisor

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

func testAttachRequestV2(t *testing.T) attachRequestV2 {
	t.Helper()
	common := validAttachAuthority()
	anchor := SessionAnchorV2{Generation: DormantV2ProtocolContract(), Binding: common.PreviousSupervisor, ControlDirectory: validBootstrapV2().ControlDirectoryIdentity}
	nonce := strings.Repeat("a", 64)
	anchor.Binding.SessionNonceDigest = canonical.DigestBytes([]byte(nonce))
	a := AttachAuthorityV2{PreviousSupervisor: anchor, Supervisor: common.Supervisor, CurrentAcquisition: common.CurrentAcquisition,
		CurrentOwnerBoundFact: common.CurrentOwnerBoundFact, Child: common.Child, ChildObservationDigest: common.ChildObservationDigest}
	c := a.CurrentAcquisition
	r := attachRequestV2{SchemaVersion: AttachSchemaV2, ProtocolRevision: protocolRevisionV2, LaunchChildProtocolRevision: launchChildProtocolRevisionV2,
		MechanicsIdentity: mechanicsIdentityV2, SessionNonce: nonce, Core: CoreIdentity{UID: c.OwnerUID, GID: c.OwnerGID, Process: c.OwnerProcess, Binary: c.OwnerBinary}, Authority: a}
	var err error
	r.RequestDigest, err = r.detachedDigest()
	if err != nil || r.validate() != nil {
		t.Fatalf("valid v2 Attach request: %v", err)
	}
	return r
}

func testAttachResponseV2(t *testing.T, request attachRequestV2) (attachResponseV2, CoreIdentity) {
	t.Helper()
	h := testHandshakeV2(request.Authority.PreviousSupervisor)
	h.SupervisorProcess = request.Authority.Supervisor
	r := attachResponseV2{SchemaVersion: AttachObservationSchemaV2, ProtocolRevision: protocolRevisionV2, LaunchChildProtocolRevision: launchChildProtocolRevisionV2,
		MechanicsIdentity: mechanicsIdentityV2, Status: "ok", ReasonCode: "process-supervisor-attached", RequestDigest: request.RequestDigest,
		Handshake: h, Authority: request.Authority, ObserverIdentity: observerIdentityV2, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	r.ResponseDigest, _ = r.detachedDigest()
	b := request.Authority.PreviousSupervisor.Binding
	peer := CoreIdentity{UID: b.UID, GID: b.GID, Process: h.SupervisorProcess, Binary: h.SupervisorBinary}
	if err := r.validate(request, peer); err != nil {
		t.Fatal(err)
	}
	return r, peer
}

func TestReadOnlyAttachV2ExactObservationAndClosedDecode(t *testing.T) {
	request := testAttachRequestV2(t)
	response, peer := testAttachResponseV2(t, request)
	observation := AttachObservationV2{Response: response, Peer: peer}
	raw := mustCanonical(observation)
	var replay AttachObservationV2
	if strictCanonicalDecode(raw, &replay) != nil || replay.Validate() != nil || !reflect.DeepEqual(replay, observation) || bytes.Contains(raw, []byte(request.SessionNonce)) {
		t.Fatal("observation lost exact binding or exposed nonce")
	}
	for name, mutate := range map[string]func(*attachResponseV2){
		"old-schema":           func(r *attachResponseV2) { r.SchemaVersion = AttachObservationSchema },
		"owner-changed":        func(r *attachResponseV2) { r.Handshake.OwnerEpoch++ },
		"head-changed":         func(r *attachResponseV2) { r.Handshake.CurrentAuthorityHead = digest("other-head") },
		"reconnect-not-attach": func(r *attachResponseV2) { r.Handshake.Reconciliation = ReconciliationUnchanged },
		"other-child":          func(r *attachResponseV2) { r.Authority.Child.PID++ },
		"other-peer":           func(r *attachResponseV2) { r.Handshake.SupervisorProcess.PID++ },
		"other-observer":       func(r *attachResponseV2) { r.ObserverIdentity = "darwin-fixed-process-supervisor-v1" },
	} {
		bad := response
		mutate(&bad)
		bad.ResponseDigest, _ = bad.detachedDigest()
		if bad.validate(request, peer) == nil {
			t.Fatalf("self-consistent %s accepted", name)
		}
	}
	for i := 0; i < reflect.TypeOf(request.Authority.PreviousSupervisor.Generation).NumField(); i++ {
		bad := request
		reflect.ValueOf(&bad.Authority.PreviousSupervisor.Generation).Elem().Field(i).SetString("")
		bad.RequestDigest, _ = bad.detachedDigest()
		if bad.validate() == nil {
			t.Fatalf("generation field %d omitted", i)
		}
	}
	bad := request
	bad.SessionNonce = strings.Repeat("b", 64)
	if bad.validate() == nil {
		t.Fatal("nonce challenge not bound")
	}
	var fields map[string]any
	if json.Unmarshal(mustCanonical(request), &fields) != nil {
		t.Fatal("request decode")
	}
	fields["pendingRequest"] = map[string]any{}
	if strictCanonicalDecode(mustCanonical(fields), &bad) == nil {
		t.Fatal("reconnect field smuggled into read-only Attach")
	}
}
