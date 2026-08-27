package candidateid

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

func digestOf(parts ...string) string {
	return canonical.DigestBytes([]byte(strings.Join(parts, "|")))
}

const (
	testAttemptID  = "attempt-0001"
	testAttemptIDB = "attempt-0002"
)

func testContentDigest() string { return digestOf("candidate-content", "v1") }
func testRecordDigest() string  { return digestOf("candidate-record", "v1") }
func testEvidenceDigest() string {
	return digestOf("conformance-evidence", "run-1")
}

func mustIdentity(t *testing.T) CandidateIdentity {
	t.Helper()
	id, err := NewCandidateIdentity(testContentDigest(), testRecordDigest(), testAttemptID)
	if err != nil {
		t.Fatalf("NewCandidateIdentity: %v", err)
	}
	return id
}

func requireErrIs(t *testing.T, err error, sentinel error) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error wrapping %v, got nil", sentinel)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected error wrapping %v, got: %v", sentinel, err)
	}
	if !strings.HasPrefix(err.Error(), "candidateid: ") {
		t.Fatalf("error message must carry the candidateid: prefix, got: %q", err.Error())
	}
}

// ── 构造与 Validate ─────────────────────────────────────────────────────────

func TestNewCandidateIdentity(t *testing.T) {
	validContent := testContentDigest()
	validRecord := testRecordDigest()

	cases := []struct {
		name          string
		contentDigest string
		recordDigest  string
		originAttempt string
		wantErr       error
	}{
		{name: "empty content digest", contentDigest: "", recordDigest: validRecord, originAttempt: testAttemptID, wantErr: ErrMalformedDigest},
		{name: "content digest whitespace", contentDigest: "   ", recordDigest: validRecord, originAttempt: testAttemptID, wantErr: ErrMalformedDigest},
		{name: "content digest missing prefix", contentDigest: strings.Repeat("a", 64), recordDigest: validRecord, originAttempt: testAttemptID, wantErr: ErrMalformedDigest},
		{name: "content digest short", contentDigest: "sha256:abcd", recordDigest: validRecord, originAttempt: testAttemptID, wantErr: ErrMalformedDigest},
		{name: "content digest uppercase hex", contentDigest: "sha256:" + strings.Repeat("A", 64), recordDigest: validRecord, originAttempt: testAttemptID, wantErr: ErrMalformedDigest},
		{name: "content digest non-hex", contentDigest: "sha256:" + strings.Repeat("g", 64), recordDigest: validRecord, originAttempt: testAttemptID, wantErr: ErrMalformedDigest},
		{name: "empty record digest", contentDigest: validContent, recordDigest: "", originAttempt: testAttemptID, wantErr: ErrMalformedDigest},
		{name: "record digest missing prefix", contentDigest: validContent, recordDigest: strings.Repeat("b", 64), originAttempt: testAttemptID, wantErr: ErrMalformedDigest},
		{name: "record digest short", contentDigest: validContent, recordDigest: "sha256:00", originAttempt: testAttemptID, wantErr: ErrMalformedDigest},
		{name: "record digest uppercase hex", contentDigest: validContent, recordDigest: "sha256:" + strings.Repeat("B", 64), originAttempt: testAttemptID, wantErr: ErrMalformedDigest},
		{name: "empty origin attempt", contentDigest: validContent, recordDigest: validRecord, originAttempt: "", wantErr: ErrEmptyOriginAttempt},
		{name: "whitespace origin attempt", contentDigest: validContent, recordDigest: validRecord, originAttempt: "  ", wantErr: ErrEmptyOriginAttempt},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewCandidateIdentity(tc.contentDigest, tc.recordDigest, tc.originAttempt)
			requireErrIs(t, err, tc.wantErr)
		})
	}
}

func TestNewCandidateIdentity_DerivedFields(t *testing.T) {
	id := mustIdentity(t)

	if !strings.HasPrefix(id.CandidateID, "candidate:") {
		t.Fatalf("CandidateID must carry candidate: prefix, got %q", id.CandidateID)
	}
	if hexPart := strings.TrimPrefix(id.CandidateID, "candidate:"); hexPart != strings.TrimPrefix(id.IdentityDigest, "sha256:") {
		t.Fatalf("CandidateID hex must equal IdentityDigest hex: %q vs %q", id.CandidateID, id.IdentityDigest)
	}
	// 确定性：同输入同派生。
	again, err := NewCandidateIdentity(testContentDigest(), testRecordDigest(), testAttemptID)
	if err != nil {
		t.Fatalf("NewCandidateIdentity again: %v", err)
	}
	if again.CandidateID != id.CandidateID || again.IdentityDigest != id.IdentityDigest {
		t.Fatalf("derivation must be deterministic: %+v vs %+v", id, again)
	}
}

func TestNewCandidateIdentity_OriginAttemptNotPartOfIdentity(t *testing.T) {
	a, err := NewCandidateIdentity(testContentDigest(), testRecordDigest(), testAttemptID)
	if err != nil {
		t.Fatalf("origin A: %v", err)
	}
	b, err := NewCandidateIdentity(testContentDigest(), testRecordDigest(), testAttemptIDB)
	if err != nil {
		t.Fatalf("origin B: %v", err)
	}
	if a.CandidateID != b.CandidateID || a.IdentityDigest != b.IdentityDigest {
		t.Fatalf("same content+record with different OriginAttemptID must yield the same CandidateID: %q vs %q",
			a.CandidateID, b.CandidateID)
	}
}

func TestValidate(t *testing.T) {
	otherDigest := digestOf("other-content")

	cases := []struct {
		name    string
		mutate  func(*CandidateIdentity)
		wantErr error // nil means still valid
	}{
		{name: "unchanged", mutate: func(*CandidateIdentity) {}, wantErr: nil},
		{name: "origin attempt changed is still valid", mutate: func(id *CandidateIdentity) { id.OriginAttemptID = testAttemptIDB }, wantErr: nil},
		{name: "content digest swapped", mutate: func(id *CandidateIdentity) { id.ContentDigest = otherDigest }, wantErr: ErrIdentityTampered},
		{name: "record digest swapped", mutate: func(id *CandidateIdentity) { id.RecordDigest = otherDigest }, wantErr: ErrIdentityTampered},
		{name: "identity digest swapped", mutate: func(id *CandidateIdentity) { id.IdentityDigest = otherDigest }, wantErr: ErrIdentityTampered},
		{name: "candidate id swapped", mutate: func(id *CandidateIdentity) { id.CandidateID = "candidate:" + strings.Repeat("0", 64) }, wantErr: ErrIdentityTampered},
		{name: "content digest malformed", mutate: func(id *CandidateIdentity) { id.ContentDigest = "nope" }, wantErr: ErrMalformedDigest},
		{name: "record digest malformed", mutate: func(id *CandidateIdentity) { id.RecordDigest = "" }, wantErr: ErrMalformedDigest},
		{name: "identity digest malformed", mutate: func(id *CandidateIdentity) { id.IdentityDigest = "sha256:zz" }, wantErr: ErrMalformedDigest},
		{name: "candidate id malformed", mutate: func(id *CandidateIdentity) { id.CandidateID = "attempt:beef" }, wantErr: ErrMalformedCandidateID},
		{name: "origin attempt emptied", mutate: func(id *CandidateIdentity) { id.OriginAttemptID = "" }, wantErr: ErrEmptyOriginAttempt},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := mustIdentity(t)
			tc.mutate(&id)
			err := id.Validate()
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("expected valid, got: %v", err)
				}
				return
			}
			requireErrIs(t, err, tc.wantErr)
		})
	}
}

// ── Ledger：注册与解析 ──────────────────────────────────────────────────────

func TestLedger_RegisterIdempotent(t *testing.T) {
	l := NewIdentityLedger()
	id := mustIdentity(t)

	if err := l.Register(id); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := l.Register(id); err != nil {
		t.Fatalf("repeat Register must be idempotent: %v", err)
	}

	// provenance-set 语义：同内容不同 OriginAttemptID 幂等。
	other, err := NewCandidateIdentity(testContentDigest(), testRecordDigest(), testAttemptIDB)
	if err != nil {
		t.Fatalf("other origin: %v", err)
	}
	if err := l.Register(other); err != nil {
		t.Fatalf("Register with different OriginAttemptID must be idempotent: %v", err)
	}

	resolved, err := l.Resolve(id.CandidateID)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.OriginAttemptID != testAttemptID {
		t.Fatalf("ledger must retain the first registered OriginAttemptID, got %q", resolved.OriginAttemptID)
	}

	// 无效身份必须先过 Validate。
	bad := id
	bad.IdentityDigest = testContentDigest()
	requireErrIs(t, l.Register(bad), ErrIdentityTampered)
}

func TestLedger_RegisterConflictFailsClosed(t *testing.T) {
	l := NewIdentityLedger()
	id := mustIdentity(t)

	// 理论上不可达（CandidateID 由内容派生），实践中直接写账本内部构造冲突。
	l.mu.Lock()
	forged := id
	forged.ContentDigest = digestOf("forged-content")
	l.identities[id.CandidateID] = forged
	l.mu.Unlock()

	err := l.Register(id)
	requireErrIs(t, err, ErrIdentityConflict)
}

func TestLedger_RegisterConcurrentIdempotent(t *testing.T) {
	l := NewIdentityLedger()
	id := mustIdentity(t)

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- l.Register(id)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Register must be idempotent: %v", err)
		}
	}
}

func TestLedger_ResolveNegatives(t *testing.T) {
	l := NewIdentityLedger()

	cases := []struct {
		name        string
		candidateID string
		wantErr     error
	}{
		{name: "empty", candidateID: "", wantErr: ErrMalformedCandidateID},
		{name: "wrong prefix", candidateID: "attempt:" + strings.Repeat("a", 64), wantErr: ErrMalformedCandidateID},
		{name: "short hex", candidateID: "candidate:abcd", wantErr: ErrMalformedCandidateID},
		{name: "uppercase hex", candidateID: "candidate:" + strings.Repeat("A", 64), wantErr: ErrMalformedCandidateID},
		{name: "non-hex", candidateID: "candidate:" + strings.Repeat("g", 64), wantErr: ErrMalformedCandidateID},
		{name: "well-formed but unknown", candidateID: "candidate:" + strings.Repeat("0", 64), wantErr: ErrUnknownCandidate},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := l.Resolve(tc.candidateID)
			requireErrIs(t, err, tc.wantErr)
		})
	}
}

// ── 证据绑定 ────────────────────────────────────────────────────────────────

func TestBindEvidence(t *testing.T) {
	l := NewIdentityLedger()
	id := mustIdentity(t)
	evidence := testEvidenceDigest()

	// 未注册身份不得先行绑定。
	requireErrIs(t, BindEvidence(l, evidence, id.CandidateID), ErrUnknownCandidate)

	// nil 账本 fail closed。
	requireErrIs(t, BindEvidence(nil, evidence, id.CandidateID), ErrNilLedger)

	if err := l.Register(id); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// 首次绑定成功，且可反查。
	if err := BindEvidence(l, evidence, id.CandidateID); err != nil {
		t.Fatalf("BindEvidence: %v", err)
	}
	got, err := l.CandidateForEvidence(evidence)
	if err != nil {
		t.Fatalf("CandidateForEvidence: %v", err)
	}
	if got != id.CandidateID {
		t.Fatalf("bound candidate mismatch: %q vs %q", got, id.CandidateID)
	}

	// 同一对重复绑定幂等。
	if err := BindEvidence(l, evidence, id.CandidateID); err != nil {
		t.Fatalf("repeat BindEvidence must be idempotent: %v", err)
	}

	// 换绑另一 CandidateID fail closed。
	other, err := NewCandidateIdentity(digestOf("other-content"), testRecordDigest(), testAttemptID)
	if err != nil {
		t.Fatalf("other identity: %v", err)
	}
	if err := l.Register(other); err != nil {
		t.Fatalf("Register other: %v", err)
	}
	requireErrIs(t, BindEvidence(l, evidence, other.CandidateID), ErrEvidenceRebound)

	// 原绑定不被换绑尝试污染。
	got, err = l.CandidateForEvidence(evidence)
	if err != nil {
		t.Fatalf("CandidateForEvidence after rebound attempt: %v", err)
	}
	if got != id.CandidateID {
		t.Fatalf("rebound attempt must not mutate the binding: %q", got)
	}
}

func TestBindEvidence_MalformedInputs(t *testing.T) {
	l := NewIdentityLedger()
	id := mustIdentity(t)
	if err := l.Register(id); err != nil {
		t.Fatalf("Register: %v", err)
	}

	cases := []struct {
		name        string
		evidence    string
		candidateID string
		wantErr     error
	}{
		{name: "empty evidence", evidence: "", candidateID: id.CandidateID, wantErr: ErrMalformedDigest},
		{name: "evidence missing prefix", evidence: strings.Repeat("a", 64), candidateID: id.CandidateID, wantErr: ErrMalformedDigest},
		{name: "candidate id malformed", evidence: testEvidenceDigest(), candidateID: "candidate:xyz", wantErr: ErrMalformedCandidateID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireErrIs(t, BindEvidence(l, tc.evidence, tc.candidateID), tc.wantErr)
		})
	}
}

func TestCandidateForEvidence_Negatives(t *testing.T) {
	l := NewIdentityLedger()

	if _, err := l.CandidateForEvidence("bad-digest"); true {
		requireErrIs(t, err, ErrMalformedDigest)
	}
	if _, err := l.CandidateForEvidence(digestOf("never-bound")); true {
		requireErrIs(t, err, ErrUnknownEvidence)
	}
}

// ── 单向兼容迁移 ────────────────────────────────────────────────────────────

func TestMigrateLegacyReference_ConvergesAcrossAttempts(t *testing.T) {
	l := NewIdentityLedger()

	idA, err := MigrateLegacyReference(l, "task-1", "run-1", testAttemptID, testContentDigest(), testRecordDigest())
	if err != nil {
		t.Fatalf("migrate attempt A: %v", err)
	}
	idB, err := MigrateLegacyReference(l, "task-2", "run-2", testAttemptIDB, testContentDigest(), testRecordDigest())
	if err != nil {
		t.Fatalf("migrate attempt B: %v", err)
	}

	// 核心证明：两个不同 Attempt、相同 content+record digest 收敛到同一
	// CandidateID，Attempt→Candidate 1:1 不被固化。
	if idA.CandidateID != idB.CandidateID {
		t.Fatalf("attempts with same content+record must converge to the same CandidateID: %q vs %q",
			idA.CandidateID, idB.CandidateID)
	}

	// 幂等重放。
	replay, err := MigrateLegacyReference(l, "task-1", "run-1", testAttemptID, testContentDigest(), testRecordDigest())
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replay != idA {
		t.Fatalf("replay must return the converged identity: %+v vs %+v", replay, idA)
	}

	// provenance first-wins：保留首个迁移者的三元组。
	p, err := l.Provenance(idA.CandidateID)
	if err != nil {
		t.Fatalf("Provenance: %v", err)
	}
	want := LegacyProvenance{CandidateID: idA.CandidateID, TaskID: "task-1", RunID: "run-1", AttemptID: testAttemptID}
	if p != want {
		t.Fatalf("legacy provenance must be recorded first-wins: %+v vs %+v", p, want)
	}

	// 迁移后证据可绑定到收敛身份。
	if err := BindEvidence(l, testEvidenceDigest(), idA.CandidateID); err != nil {
		t.Fatalf("BindEvidence on migrated identity: %v", err)
	}
}

func TestMigrateLegacyReference_FailClosed(t *testing.T) {
	l := NewIdentityLedger()
	content := testContentDigest()
	record := testRecordDigest()

	cases := []struct {
		name          string
		ledger        *IdentityLedger
		taskID        string
		runID         string
		attemptID     string
		contentDigest string
		recordDigest  string
		wantErr       error
	}{
		{name: "nil ledger", ledger: nil, taskID: "task", runID: "run", attemptID: testAttemptID, contentDigest: content, recordDigest: record, wantErr: ErrNilLedger},
		{name: "empty taskID", ledger: l, taskID: "", runID: "run", attemptID: testAttemptID, contentDigest: content, recordDigest: record, wantErr: ErrEmptyLegacyProvenance},
		{name: "whitespace runID", ledger: l, taskID: "task", runID: "  ", attemptID: testAttemptID, contentDigest: content, recordDigest: record, wantErr: ErrEmptyLegacyProvenance},
		{name: "empty attemptID", ledger: l, taskID: "task", runID: "run", attemptID: "", contentDigest: content, recordDigest: record, wantErr: ErrEmptyOriginAttempt},
		{name: "malformed content digest", ledger: l, taskID: "task", runID: "run", attemptID: testAttemptID, contentDigest: "nope", recordDigest: record, wantErr: ErrMalformedDigest},
		{name: "malformed record digest", ledger: l, taskID: "task", runID: "run", attemptID: testAttemptID, contentDigest: content, recordDigest: "", wantErr: ErrMalformedDigest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := MigrateLegacyReference(tc.ledger, tc.taskID, tc.runID, tc.attemptID, tc.contentDigest, tc.recordDigest)
			requireErrIs(t, err, tc.wantErr)
		})
	}

	// 失败迁移不得在账本留下任何痕迹。
	if _, err := l.Resolve(mustIdentity(t).CandidateID); !errors.Is(err, ErrUnknownCandidate) {
		t.Fatalf("failed migrations must not register identities, got: %v", err)
	}
}

// ── 错误前缀合同 ────────────────────────────────────────────────────────────

func TestErrorPrefixContract(t *testing.T) {
	l := NewIdentityLedger()
	id := mustIdentity(t)
	if err := l.Register(id); err != nil {
		t.Fatalf("Register: %v", err)
	}
	unknownCandidateID := "candidate:" + strings.Repeat("0", 64)

	tampered := id
	tampered.RecordDigest = testEvidenceDigest()

	errs := []error{}
	if _, err := NewCandidateIdentity("bad", testRecordDigest(), testAttemptID); true {
		errs = append(errs, err)
	}
	if _, err := NewCandidateIdentity(testContentDigest(), testRecordDigest(), ""); true {
		errs = append(errs, err)
	}
	errs = append(errs,
		tampered.Validate(),
		l.Register(tampered),
		mustResolveErr(l, "bogus"),
		mustResolveErr(l, unknownCandidateID),
		BindEvidence(nil, testEvidenceDigest(), id.CandidateID),
		BindEvidence(l, "bad-digest", id.CandidateID),
		BindEvidence(l, testEvidenceDigest(), unknownCandidateID),
		mustCandidateForEvidenceErr(l, "bad-digest"),
		mustCandidateForEvidenceErr(l, digestOf("never-bound")),
		mustProvenanceErr(l, "bogus"),
		mustProvenanceErr(l, unknownCandidateID),
		mustMigrateErr(nil, "task", "run", testAttemptID, testContentDigest(), testRecordDigest()),
		mustMigrateErr(l, "", "run", testAttemptID, testContentDigest(), testRecordDigest()),
	)

	// 换绑错误也纳入前缀合同。
	if err := BindEvidence(l, testEvidenceDigest(), id.CandidateID); err != nil {
		t.Fatalf("BindEvidence: %v", err)
	}
	other, err := NewCandidateIdentity(digestOf("other-content"), testRecordDigest(), testAttemptID)
	if err != nil {
		t.Fatalf("other identity: %v", err)
	}
	if err := l.Register(other); err != nil {
		t.Fatalf("Register other: %v", err)
	}
	errs = append(errs, BindEvidence(l, testEvidenceDigest(), other.CandidateID))

	for i, err := range errs {
		if err == nil {
			t.Fatalf("case %d: expected error, got nil", i)
		}
		if !strings.HasPrefix(err.Error(), "candidateid: ") {
			t.Fatalf("case %d: error must carry the candidateid: prefix, got %q", i, err.Error())
		}
	}
}

func mustResolveErr(l *IdentityLedger, candidateID string) error {
	if _, err := l.Resolve(candidateID); err != nil {
		return err
	}
	return nil
}

func mustCandidateForEvidenceErr(l *IdentityLedger, evidenceDigest string) error {
	if _, err := l.CandidateForEvidence(evidenceDigest); err != nil {
		return err
	}
	return nil
}

func mustProvenanceErr(l *IdentityLedger, candidateID string) error {
	if _, err := l.Provenance(candidateID); err != nil {
		return err
	}
	return nil
}

func mustMigrateErr(l *IdentityLedger, taskID, runID, attemptID, contentDigest, recordDigest string) error {
	if _, err := MigrateLegacyReference(l, taskID, runID, attemptID, contentDigest, recordDigest); err != nil {
		return err
	}
	return nil
}
