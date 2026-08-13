// This file deliberately stays contract-free: the domain package test
// binary cannot import internal/contract (contract imports domain, which
// would form an import cycle), so schema re-validation assertions live with
// the review packet tests, which legitimately consume both packages.

package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// baseReviewPacket builds a ReviewPacket carrying every frozen legacy field
// and no Candidate identities, mirroring a packet archived before ADR 0027
// adoption.
func baseReviewPacket() ReviewPacket {
	return ReviewPacket{
		APIVersion:               APIVersionV1Alpha1,
		Kind:                     KindReviewPacket,
		TaskID:                   "task-01",
		RunID:                    "run-01",
		ReviewRound:              1,
		SpecDigest:               testDigestValue("a"),
		BaseSHA:                  testSHA("0"),
		SnapshotDigest:           testDigestValue("b"),
		DiffDigest:               testDigestValue("c"),
		VerificationDigest:       testDigestValue("d"),
		ArtifactManifestDigest:   testDigestValue("e"),
		WorkerResultDigests:      []string{testDigestValue("f")},
		EvidenceDigest:           testDigestValue("1"),
		Inputs:                   PacketInputs{TaskSpec: "task-spec.json", Patch: "observed.patch", VerificationReport: "verification-report.json", ArtifactManifest: "artifact-manifest.json", WorkerResults: []string{"attempts/attempt-01/worker-result.json"}},
		PreviousBlockingFindings: []PreviousFinding{},
		GeneratedAt:              time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC),
	}
}

// TestReviewPacketLegacyWireOmitsCandidateFields pins the §5.1 byte-stability
// rule: a packet without Candidate identities must serialize exactly as
// before adoption, with no candidateDigest/workerCandidateDigest member
// present on the wire.
func TestReviewPacketLegacyWireOmitsCandidateFields(t *testing.T) {
	data, err := json.Marshal(baseReviewPacket())
	if err != nil {
		t.Fatal(err)
	}
	wire := string(data)
	for _, key := range []string{`"candidateDigest"`, `"workerCandidateDigest"`} {
		if strings.Contains(wire, key) {
			t.Fatalf("legacy packet wire bytes carry %s: %s", key, wire)
		}
	}
	var decoded ReviewPacket
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.CandidateDigest != "" || decoded.WorkerCandidateDigest != "" {
		t.Fatalf("legacy packet round-trip must leave candidate fields empty: %+v", decoded)
	}
}

// TestReviewPacketCandidateWireRoundTrip proves the optional Candidate
// binding fields survive a wire round-trip when present.
func TestReviewPacketCandidateWireRoundTrip(t *testing.T) {
	packet := baseReviewPacket()
	packet.WorkerCandidateDigest = testDigestValue("2")
	packet.CandidateDigest = testDigestValue("3")
	data, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	wire := string(data)
	if !strings.Contains(wire, `"workerCandidateDigest":"`+testDigestValue("2")+`"`) || !strings.Contains(wire, `"candidateDigest":"`+testDigestValue("3")+`"`) {
		t.Fatalf("candidate packet wire bytes miss the binding fields: %s", wire)
	}
	var decoded ReviewPacket
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.WorkerCandidateDigest != packet.WorkerCandidateDigest || decoded.CandidateDigest != packet.CandidateDigest {
		t.Fatalf("candidate binding round-trip diverged: %+v", decoded)
	}
}

// TestPreviousFindingCandidateDigestOptionalOnWire pins the same omission
// discipline for PreviousFinding: findings raised by legacy rounds keep
// their exact serialization, candidate-round findings carry the head
// Candidate identity.
func TestPreviousFindingCandidateDigestOptionalOnWire(t *testing.T) {
	finding := PreviousFinding{
		Finding:            Finding{ID: "F-1", Severity: "P1", Title: "race", Description: "data race"},
		EvidenceDigest:     testDigestValue("4"),
		SnapshotDigest:     testDigestValue("5"),
		VerificationDigest: testDigestValue("6"),
	}
	data, err := json.Marshal(finding)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"candidateDigest"`) {
		t.Fatalf("legacy finding wire bytes carry candidateDigest: %s", data)
	}
	finding.CandidateDigest = testDigestValue("7")
	data, err = json.Marshal(finding)
	if err != nil {
		t.Fatal(err)
	}
	var decoded PreviousFinding
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.CandidateDigest != testDigestValue("7") {
		t.Fatalf("finding candidate binding round-trip diverged: %+v", decoded)
	}
}
