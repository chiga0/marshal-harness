package resultingress

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/processsupervisor"
)

func testBootstrapV2Input() (CurrentOwnerBinding, processsupervisor.BootstrapRequestV2) {
	g := processsupervisor.DormantV2ProtocolContract()
	owner := CurrentOwnerBinding{Scope: attemptTestOwnerScope(attemptTestIdentity()), OwnerEpoch: 1, ControlOwnerAcquiredFactDigest: attemptTestDigest("bootstrap-owner")}
	r := processsupervisor.BootstrapRequestV2{SchemaVersion: g.BootstrapSchema, ProtocolRevision: g.ProtocolRevision, LaunchChildProtocolRevision: g.LaunchChildProtocolRevision, MechanicsIdentity: g.MechanicsIdentity,
		SessionID: "bootstrap-v2", SessionNonce: strings.Repeat("9", 64), OwnerEpoch: 1, Authority: supervisorAuthorityTuple(attemptTestIdentity()),
		LaunchAuthorizedFact: attemptTestDigest("bootstrap-launch"), CurrentAuthorityHead: attemptTestDigest("bootstrap-head"),
		ControlDirectoryIdentity: processsupervisor.ControlDirectoryIdentity{CanonicalPath: "/fixed/control", Device: 3, Inode: 5, FileType: "directory", UID: 501, GID: 20, Mode: POSIXFileTypeDirectory | 0700, LinkCount: 2},
		Core:                     processsupervisor.CoreIdentity{UID: 501, GID: 20, Binary: attemptTestBinary(), Process: processsupervisor.ProcessIdentity{PID: 8101, BirthSeconds: 1700000000, BirthMicroseconds: 31, SessionID: 8101, ProcessGroupID: 8101}}}
	return owner, r
}

func TestSupervisorBootstrapV2ExactGenerationAndSecretFreeProjection(t *testing.T) {
	owner, request := testBootstrapV2Input()
	prepared, err := NewSupervisorBootstrapPreparedV2(owner, request)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(prepared)
	if err != nil || bytes.Contains(raw, []byte(request.SessionNonce)) {
		t.Fatal("raw nonce in evidence")
	}
	var decoded SupervisorBootstrapPrepared
	if json.Unmarshal(raw, &decoded) != nil || decoded != prepared || decoded.Validate() != nil {
		t.Fatal("projection round trip")
	}
	for i := 0; i < reflect.TypeOf(prepared.Request.Generation).NumField(); i++ {
		t.Run(reflect.TypeOf(prepared.Request.Generation).Field(i).Name, func(t *testing.T) {
			changed := prepared
			reflect.ValueOf(&changed.Request.Generation).Elem().Field(i).SetString("")
			changed.BootstrapRequestDigest, _ = canonicalDigest(changed.Request)
			if changed.Validate() == nil {
				t.Fatal("missing generation accepted after rehash")
			}
		})
	}
	for name, change := range map[string]func(*SupervisorBootstrapPrepared){
		"outer-v1":  func(p *SupervisorBootstrapPrepared) { p.ProtocolRevision = processsupervisor.ProtocolRevision },
		"schema":    func(p *SupervisorBootstrapPrepared) { p.Request.SchemaVersion = processsupervisor.BootstrapSchema },
		"mechanics": func(p *SupervisorBootstrapPrepared) { p.Request.MechanicsIdentity = "darwin-ptrace-exec-stop/v1" },
		"child":     func(p *SupervisorBootstrapPrepared) { p.Request.LaunchChildProtocolRevision = "" },
		"uid":       func(p *SupervisorBootstrapPrepared) { p.Request.Core.UID++ },
		"owner":     func(p *SupervisorBootstrapPrepared) { p.Owner.OwnerEpoch++ },
	} {
		t.Run(name, func(t *testing.T) {
			changed := prepared
			change(&changed)
			changed.BootstrapRequestDigest, _ = canonicalDigest(changed.Request)
			if changed.Validate() == nil {
				t.Fatal("mixed projection accepted")
			}
		})
	}
}

func TestSupervisorBootstrapLegacySerializationDoesNotGainV2Fields(t *testing.T) {
	owner, v2 := testBootstrapV2Input()
	request := processsupervisor.BootstrapRequest{SchemaVersion: processsupervisor.BootstrapSchema, ProtocolRevision: processsupervisor.ProtocolRevision, SessionID: v2.SessionID, SessionNonce: v2.SessionNonce,
		OwnerEpoch: v2.OwnerEpoch, Authority: v2.Authority, LaunchAuthorizedFact: v2.LaunchAuthorizedFact, CurrentAuthorityHead: v2.CurrentAuthorityHead, ControlDirectoryIdentity: v2.ControlDirectoryIdentity, Core: v2.Core}
	prepared, err := NewSupervisorBootstrapPrepared(owner, request)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(prepared.Request)
	if err != nil {
		t.Fatal(err)
	}
	// Compare against the complete original projection, not a digest rehashed
	// from an upgraded shape. Empty v2 fields must never alter old ledger bytes.
	legacy := struct {
		SchemaVersion            string                                     `json:"schemaVersion"`
		ProtocolRevision         string                                     `json:"protocolRevision"`
		SessionID                string                                     `json:"sessionId"`
		SessionNonceDigest       string                                     `json:"sessionNonceDigest"`
		OwnerEpoch               uint64                                     `json:"ownerEpoch"`
		Authority                processsupervisor.AuthorityTuple           `json:"authority"`
		LaunchAuthorizedFact     string                                     `json:"launchAuthorizedFactDigest"`
		CurrentAuthorityHead     string                                     `json:"currentAuthorityHead"`
		ControlDirectoryIdentity processsupervisor.ControlDirectoryIdentity `json:"controlDirectoryIdentity"`
		Core                     processsupervisor.CoreIdentity             `json:"core"`
	}{prepared.Request.SchemaVersion, prepared.Request.ProtocolRevision, prepared.Request.SessionID, prepared.Request.SessionNonceDigest, prepared.Request.OwnerEpoch, prepared.Request.Authority, prepared.Request.LaunchAuthorizedFact, prepared.Request.CurrentAuthorityHead, prepared.Request.ControlDirectoryIdentity, prepared.Request.Core}
	want, err := json.Marshal(legacy)
	if err != nil || !bytes.Equal(raw, want) {
		t.Fatal("legacy projection bytes changed")
	}
	digest, err := canonicalDigest(legacy)
	if err != nil || prepared.BootstrapRequestDigest != digest {
		t.Fatal("legacy projection digest changed")
	}
}
