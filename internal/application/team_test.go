package application

import (
	"bytes"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

func TestInitialTeamRequestFreezesExactApproval(t *testing.T) {
	raw := []byte(`{"plan":1}`)
	r := ApproveInitialTeamRequest{ProtocolRevision: InitialTeamApprovalProtocol, RequestID: "approval-1", InputsDigest: canonical.DigestBytes(raw), Inputs: raw}
	frozen, digest, err := r.Frozen()
	if err != nil || !validDigest(digest) {
		t.Fatal(err)
	}
	r.Inputs = []byte(" { \"plan\": 1 } ")
	replay, again, err := r.Frozen()
	if err != nil || digest != again || !bytes.Equal(replay.Inputs, frozen.Inputs) {
		t.Fatal("formatting changed approval identity")
	}
	raw[2] = 'x'
	if string(frozen.Inputs) != `{"plan":1}` {
		t.Fatal("caller mutated frozen approval")
	}
	r.RequestID = "approval-2"
	_, changed, err := r.Frozen()
	if err != nil || changed == digest {
		t.Fatal("request identity not bound")
	}
	for _, bad := range []ApproveInitialTeamRequest{
		{},
		{ProtocolRevision: InitialTeamApprovalProtocol, RequestID: "approval-1", InputsDigest: r.InputsDigest, Inputs: []byte(`{"plan":1,"plan":1}`)},
		{ProtocolRevision: InitialTeamApprovalProtocol, RequestID: "approval-1", InputsDigest: r.InputsDigest, Inputs: []byte(`{"plan":2}`)},
		{ProtocolRevision: InitialTeamApprovalProtocol, RequestID: "approval-1", InputsDigest: r.InputsDigest, ExpectedHead: r.InputsDigest, Inputs: frozen.Inputs},
		{ProtocolRevision: InitialTeamApprovalProtocol, RequestID: "approval-1", InputsDigest: r.InputsDigest, Inputs: []byte(strings.Repeat(" ", MaxInitialTeamInputsBytes+1))},
	} {
		if _, _, err := bad.Frozen(); !HasReason(err, ReasonInvalidRequest) {
			t.Fatal("invalid approval accepted")
		}
	}
}
