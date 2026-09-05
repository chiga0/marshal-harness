package processsupervisor

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func newWriterV2File(t *testing.T, data []byte) *os.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), journalFileNameV2)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func writerV2Records(t *testing.T) (journalRecordV2, journalRecordV2, journalRecordV2) {
	t.Helper()
	request := validRequestV2(t)
	created := validJournalRecordV2(t)
	created.SessionID, created.CurrentAuthorityHead = request.SessionID, request.CurrentAuthorityHead
	created.RecordDigest, _ = created.detachedDigest()
	projection := requestProjection{Command: request.Command, CommandID: request.CommandID, Sequence: request.Sequence,
		RequestDigest: request.RequestDigest, PreviousCommandDigest: request.PreviousCommandDigest,
		CurrentAuthorityHead: request.CurrentAuthorityHead, Deadline: request.Deadline}
	if _, err := decodePayload(request.Command, request.Payload, &projection); err != nil {
		t.Fatal(err)
	}
	intent := created
	intent.Kind, intent.Request = journalCommandIntent, &projection
	intent.JournalSequence, intent.PreviousRecordDigest = 2, created.RecordDigest
	intent.RecordDigest, _ = intent.detachedDigest()
	response := validResponseV2(t, request)
	receipt := intent
	receipt.Kind, receipt.Response = journalCommandReceipt, &response
	receipt.JournalSequence, receipt.PreviousRecordDigest = 3, intent.RecordDigest
	receipt.RecordDigest, _ = receipt.detachedDigest()
	return created, intent, receipt
}

func framesV2(t *testing.T, records ...journalRecordV2) []byte {
	t.Helper()
	var data []byte
	for _, record := range records {
		frame, err := encodeFrame(record, MaxJournalPayload)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, frame...)
	}
	return data
}

func TestJournalWriterV2DurableIntentReceiptAndReadOnlyEquivalence(t *testing.T) {
	file := newWriterV2File(t, nil)
	writer, err := openJournalWriterV2(file)
	if err != nil {
		t.Fatal(err)
	}
	created, intent, receipt := writerV2Records(t)
	for _, record := range []journalRecordV2{created, intent, receipt} {
		written, err := writer.append(record)
		if err != nil || written.RecordDigest != record.RecordDigest {
			t.Fatalf("append %s: %v", record.Kind, err)
		}
		data, err := os.ReadFile(file.Name())
		if err != nil {
			t.Fatal(err)
		}
		observed, err := DecodeSupervisorJournalReadOnly(journalFileNameV2, data)
		if err != nil || observed.Sequence != written.JournalSequence || observed.Head != written.RecordDigest || observed.PartialTail {
			t.Fatalf("durable observation: %+v %v", observed, err)
		}
		if written.Response != nil {
			written.Response.Payload[0] = '!'
			if writer.state.receipts[written.Request.CommandID].Response.Payload[0] == '!' {
				t.Fatal("returned receipt aliases writer state")
			}
		}
	}
	if writer.state.pending != nil || writer.state.commandSeq != 1 || writer.state.authorityHead != intent.Request.NextAuthorityHead {
		t.Fatal("bound receipt did not advance exact command state")
	}
	before, _ := os.ReadFile(file.Name())
	if _, err := writer.append(intent); err == nil {
		t.Fatal("duplicate command admitted")
	}
	after, _ := os.ReadFile(file.Name())
	if !bytes.Equal(before, after) {
		t.Fatal("rejected append changed bytes")
	}
	if err := writer.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.append(receipt); err == nil {
		t.Fatal("closed writer accepted append")
	}
}

func TestJournalWriterV2ReopenPreservesPendingAndNeverRetries(t *testing.T) {
	created, intent, receipt := writerV2Records(t)
	file := newWriterV2File(t, framesV2(t, created, intent))
	writer, err := openJournalWriterV2(file)
	if err != nil {
		t.Fatal(err)
	}
	if writer.state.pending == nil || writer.state.commandSeq != 0 || writer.state.sequence != 2 {
		t.Fatal("pending effect must not become committed")
	}
	if _, err := writer.append(intent); err == nil {
		t.Fatal("pending command was retried")
	}
	if _, err := writer.append(receipt); err != nil {
		t.Fatal(err)
	}
}

func TestJournalWriterV2ValidatesBeforeTailRepair(t *testing.T) {
	created, intent, _ := writerV2Records(t)
	base := framesV2(t, created)
	legalTail := framesV2(t, intent)
	for _, tail := range [][]byte{legalTail[:frameHeaderBytes+2], legalTail[:len(legalTail)-1]} {
		file := newWriterV2File(t, append(append([]byte(nil), base...), tail...))
		writer, err := openJournalWriterV2(file)
		if err != nil || writer.state.sequence != 1 || writer.state.pending != nil {
			t.Fatalf("legal tail: %v", err)
		}
		data, _ := os.ReadFile(file.Name())
		if !bytes.Equal(data, base) {
			t.Fatal("repair did not keep exactly the complete prefix")
		}
	}
	invalid := cloneJournalRecordV2(intent)
	invalid.Request.Sequence = 2
	invalid.RecordDigest, _ = invalid.detachedDigest()
	tail := framesV2(t, invalid)
	for _, suffix := range [][]byte{tail[:len(tail)-1], append(tail, legalTail[:frameHeaderBytes+2]...)} {
		before := append(append([]byte(nil), base...), suffix...)
		file := newWriterV2File(t, before)
		if _, err := openJournalWriterV2(file); err == nil {
			t.Fatal("invalid complete transition was repaired")
		}
		after, _ := os.ReadFile(file.Name())
		if !bytes.Equal(before, after) {
			t.Fatal("invalid history was mutated before semantic validation")
		}
	}
}

func TestJournalWriterV2RejectsLegacyAndForgedReceiptWithoutWriting(t *testing.T) {
	legacy, err := encodeFrame(validJournalRecordV1(t), MaxJournalPayload)
	if err != nil {
		t.Fatal(err)
	}
	file := newWriterV2File(t, legacy)
	if _, err := openJournalWriterV2(file); err == nil {
		t.Fatal("v1 bytes promoted into writable v2 history")
	}
	after, _ := os.ReadFile(file.Name())
	if !bytes.Equal(legacy, after) {
		t.Fatal("legacy history changed")
	}
	created, intent, receipt := writerV2Records(t)
	file = newWriterV2File(t, framesV2(t, created, intent))
	writer, err := openJournalWriterV2(file)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(file.Name())
	for _, mutate := range []func(*journalRecordV2){
		func(r *journalRecordV2) { r.ProtocolRevision = ProtocolRevision },
		func(r *journalRecordV2) { r.OwnerEpoch++ },
		func(r *journalRecordV2) { r.Response.SessionID = "different-session" },
		func(r *journalRecordV2) { r.Response.ReceiptDigest = digest("forged") },
	} {
		bad := cloneJournalRecordV2(receipt)
		mutate(&bad)
		if _, err := writer.append(bad); err == nil {
			t.Fatal("forged receipt accepted")
		}
		after, _ := os.ReadFile(file.Name())
		if !bytes.Equal(before, after) {
			t.Fatal("rejection changed bytes")
		}
	}
}
