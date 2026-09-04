package processsupervisor

import (
	"bytes"
	"testing"
)

func TestSupervisorJournalReadOnlyRoutingRejectsGenerationMix(t *testing.T) {
	v2Record := validJournalRecordV2(t)
	v2Frame, err := encodeFrame(v2Record, MaxJournalPayload)
	if err != nil {
		t.Fatal(err)
	}
	v2, err := DecodeSupervisorJournalReadOnly(journalFileNameV2, v2Frame)
	if err != nil || v2.ProtocolRevision != protocolRevisionV2 || v2.SchemaVersion != journalSchemaV2 || v2.Sequence != 1 || v2.Head != v2Record.RecordDigest || v2.PartialTail {
		t.Fatalf("v2 observation=%+v err=%v", v2, err)
	}
	if _, err := DecodeSupervisorJournalReadOnly(JournalFileName, v2Frame); err == nil {
		t.Fatal("v2 bytes accepted under v1 leaf")
	}

	v1Record := validJournalRecordV1(t)
	v1Frame, err := encodeFrame(v1Record, MaxJournalPayload)
	if err != nil {
		t.Fatal(err)
	}
	v1, err := DecodeSupervisorJournalReadOnly(JournalFileName, v1Frame)
	if err != nil || v1.ProtocolRevision != ProtocolRevision || v1.SchemaVersion != JournalSchema || v1.Sequence != 1 || v1.Head != v1Record.RecordDigest || v1.PartialTail {
		t.Fatalf("v1 observation=%+v err=%v", v1, err)
	}
	if _, err := DecodeSupervisorJournalReadOnly(journalFileNameV2, v1Frame); err == nil {
		t.Fatal("v1 bytes accepted under v2 leaf")
	}
	if _, err := DecodeSupervisorJournalReadOnly("process-supervisor.journal", v1Frame); err == nil {
		t.Fatal("unknown journal leaf accepted")
	}
}

func TestSupervisorJournalReadOnlyReportsButDoesNotRepairPartialTail(t *testing.T) {
	record := validJournalRecordV2(t)
	frame, err := encodeFrame(record, MaxJournalPayload)
	if err != nil {
		t.Fatal(err)
	}
	torn := append([]byte(nil), frame[:len(frame)-1]...)
	before := append([]byte(nil), torn...)
	observation, err := DecodeSupervisorJournalReadOnly(journalFileNameV2, torn)
	if err != nil || !observation.PartialTail || observation.Sequence != 0 || observation.Head != journalGenesisDigestV2 {
		t.Fatalf("observation=%+v err=%v", observation, err)
	}
	if !bytes.Equal(before, torn) {
		t.Fatal("read-only decoder mutated source bytes")
	}
}

func TestSupervisorJournalLeafSetRejectsDualOrUnknownGeneration(t *testing.T) {
	for _, names := range [][]string{nil, {}, {JournalFileName}, {journalFileNameV2}} {
		if err := ValidateSupervisorJournalLeafSet(names); err != nil {
			t.Fatalf("valid leaf set %q rejected: %v", names, err)
		}
	}
	for _, names := range [][]string{{JournalFileName, journalFileNameV2}, {JournalFileName, JournalFileName}, {"other"}} {
		if err := ValidateSupervisorJournalLeafSet(names); err == nil {
			t.Fatalf("invalid leaf set %q accepted", names)
		}
	}
}

func TestDormantV2ControlDirectoryPhaseSetsAreExact(t *testing.T) {
	base := []string{"session.nonce", journalFileNameV2, "control.sock"}
	stdout := append(append([]string(nil), base...), "stdout.bin")
	stderr := append(append([]string(nil), stdout...), "stderr.bin")
	collected := append(append([]string(nil), stderr...), "transcript.jcs")
	for phase, entries := range map[string][]string{
		v2ControlPhaseBootstrapInitial: nil,
		v2ControlPhaseRuntimeBase:      base,
		v2ControlPhaseCollected:        collected,
	} {
		if err := ValidateDormantV2ControlDirectoryEntries(phase, entries); err != nil {
			t.Fatalf("phase %q rejected: %v", phase, err)
		}
	}
	for _, entries := range [][]string{base, stdout, stderr, collected} {
		if err := ValidateDormantV2ControlDirectoryEntries(v2ControlPhaseCollectIntent, entries); err != nil {
			t.Fatalf("collect-intent set %q rejected: %v", entries, err)
		}
	}
	for name, entries := range map[string][]string{
		"v1-leaf":        {"session.nonce", JournalFileName, "control.sock"},
		"dual-leaf":      {"session.nonce", JournalFileName, journalFileNameV2, "control.sock"},
		"skipped-stderr": {"session.nonce", journalFileNameV2, "control.sock", "stdout.bin", "transcript.jcs"},
		"duplicate":      {"session.nonce", journalFileNameV2, "control.sock", "control.sock"},
		"unknown":        {"session.nonce", journalFileNameV2, "control.sock", "migration.marker"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateDormantV2ControlDirectoryEntries(v2ControlPhaseCollectIntent, entries); err == nil {
				t.Fatal("invalid v2 phase set accepted")
			}
		})
	}
}

func TestJournalV2BindingAndGenesisAreFailClosed(t *testing.T) {
	base := validJournalRecordV2(t)
	for name, mutate := range map[string]func(*journalRecordV2){
		"protocol":     func(value *journalRecordV2) { value.ProtocolRevision = ProtocolRevision },
		"launch-child": func(value *journalRecordV2) { value.LaunchChildProtocolRevision = "process-supervisor-launch-child/v1" },
		"mechanics":    func(value *journalRecordV2) { value.MechanicsIdentity = "darwin-ptrace-exec-stop/v1" },
		"schema":       func(value *journalRecordV2) { value.SchemaVersion = JournalSchema },
		"genesis":      func(value *journalRecordV2) { value.PreviousRecordDigest = JournalGenesisDigest },
	} {
		t.Run(name, func(t *testing.T) {
			changed := base
			mutate(&changed)
			changed.RecordDigest, _ = changed.detachedDigest()
			frame, err := encodeFrame(changed, MaxJournalPayload)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeSupervisorJournalReadOnly(journalFileNameV2, frame); err == nil {
				t.Fatal("mixed v2 journal accepted")
			}
		})
	}
}

func TestJournalV2ReadOnlyDecoderRejectsSemanticallyImpossibleCommandChain(t *testing.T) {
	created := validJournalRecordV2(t)
	createdFrame, err := encodeFrame(created, MaxJournalPayload)
	if err != nil {
		t.Fatal(err)
	}
	request := validRequestV2(t)
	projection := requestProjection{
		Command: request.Command, CommandID: request.CommandID, Sequence: 2, RequestDigest: request.RequestDigest,
		PreviousCommandDigest: request.PreviousCommandDigest, CurrentAuthorityHead: request.CurrentAuthorityHead, Deadline: request.Deadline,
	}
	if _, err := decodePayload(request.Command, request.Payload, &projection); err != nil {
		t.Fatal(err)
	}
	intent := journalRecordV2{
		SchemaVersion: journalSchemaV2, ProtocolRevision: protocolRevisionV2, LaunchChildProtocolRevision: launchChildProtocolRevisionV2, MechanicsIdentity: mechanicsIdentityV2,
		JournalSequence: 2, Kind: journalCommandIntent, SessionID: created.SessionID, SessionNonceDigest: created.SessionNonceDigest, Authority: created.Authority,
		OwnerEpoch: created.OwnerEpoch, CurrentAuthorityHead: created.CurrentAuthorityHead, Request: &projection, PreviousRecordDigest: created.RecordDigest,
	}
	intent.RecordDigest, err = intent.detachedDigest()
	if err != nil {
		t.Fatal(err)
	}
	intentFrame, err := encodeFrame(intent, MaxJournalPayload)
	if err != nil {
		t.Fatal(err)
	}
	data := append(append([]byte(nil), createdFrame...), intentFrame...)
	if _, err := DecodeSupervisorJournalReadOnly(journalFileNameV2, data); err == nil {
		t.Fatal("sequence-2 first command accepted")
	}
	torn := append(append([]byte(nil), createdFrame...), intentFrame[:len(intentFrame)-1]...)
	if _, err := DecodeSupervisorJournalReadOnly(journalFileNameV2, torn); err == nil {
		t.Fatal("complete semantically invalid torn command accepted")
	}
}

func validJournalRecordV2(t *testing.T) journalRecordV2 {
	t.Helper()
	bootstrap := validBootstrapV2()
	record := journalRecordV2{
		SchemaVersion: journalSchemaV2, ProtocolRevision: protocolRevisionV2, LaunchChildProtocolRevision: launchChildProtocolRevisionV2, MechanicsIdentity: mechanicsIdentityV2,
		JournalSequence: 1, Kind: journalSessionCreated, SessionID: bootstrap.SessionID, SessionNonceDigest: digest("v2-nonce"), Authority: bootstrap.Authority,
		OwnerEpoch: bootstrap.OwnerEpoch, CurrentAuthorityHead: bootstrap.CurrentAuthorityHead, PreviousRecordDigest: journalGenesisDigestV2,
	}
	var err error
	record.RecordDigest, err = record.detachedDigest()
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func validJournalRecordV1(t *testing.T) journalRecord {
	t.Helper()
	bootstrap := validBootstrap()
	record := journalRecord{
		SchemaVersion: JournalSchema, JournalSequence: 1, Kind: journalSessionCreated, SessionID: bootstrap.SessionID, SessionNonceDigest: digest("v1-nonce"), Authority: bootstrap.Authority,
		OwnerEpoch: bootstrap.OwnerEpoch, CurrentAuthorityHead: bootstrap.CurrentAuthorityHead, PreviousRecordDigest: JournalGenesisDigest,
	}
	var err error
	record.RecordDigest, err = record.detachedDigest()
	if err != nil {
		t.Fatal(err)
	}
	return record
}
