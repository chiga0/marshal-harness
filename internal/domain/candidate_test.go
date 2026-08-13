package domain

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

// validWorkerCandidate builds a sealed chain-root Candidate: the detached
// candidateDigest is computed and back-filled so the record passes the
// fail-closed digest recomputation in Validate.
func validWorkerCandidate(t *testing.T) Candidate {
	t.Helper()
	candidate := Candidate{
		APIVersion:           APIVersionV1Alpha1,
		Kind:                 KindCandidate,
		TaskID:               "task-01",
		RunID:                "run-01",
		AttemptID:            "attempt-01",
		AuthorityNamespaceID: testDigestValue("a"),
		BaseSHA:              testSHA("0"),
		ContentDigest:        testDigestValue("d"),
		ProducerKind:         ProducerKindWorker,
		Producer:             "worker",
		CreatedAt:            time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC),
	}
	digest, err := candidate.Digest()
	if err != nil {
		t.Fatalf("Digest() error = %v", err)
	}
	candidate.CandidateDigest = digest
	return candidate
}

// validNormalizerCandidate builds a sealed normalizer Candidate whose
// predecessorCandidateDigest points at the worker chain root.
func validNormalizerCandidate(t *testing.T) Candidate {
	t.Helper()
	predecessor := validWorkerCandidate(t)
	candidate := Candidate{
		APIVersion:                 APIVersionV1Alpha1,
		Kind:                       KindCandidate,
		TaskID:                     predecessor.TaskID,
		RunID:                      predecessor.RunID,
		AttemptID:                  predecessor.AttemptID,
		AuthorityNamespaceID:       predecessor.AuthorityNamespaceID,
		BaseSHA:                    predecessor.BaseSHA,
		ContentDigest:              testDigestValue("e"),
		ProducerKind:               ProducerKindNormalizer,
		Producer:                   "verifier:format-normalize",
		PredecessorCandidateDigest: predecessor.CandidateDigest,
		CreatedAt:                  predecessor.CreatedAt.Add(time.Minute),
	}
	digest, err := candidate.Digest()
	if err != nil {
		t.Fatalf("Digest() error = %v", err)
	}
	candidate.CandidateDigest = digest
	return candidate
}

// resealCandidate recomputes the detached digest after a structural
// mutation, so the fail-closed digest gate stays satisfied and the test
// isolates the structural rule under test.
func resealCandidate(t *testing.T, candidate *Candidate) {
	t.Helper()
	digest, err := candidate.Digest()
	if err != nil {
		t.Fatalf("Digest() error = %v", err)
	}
	candidate.CandidateDigest = digest
}

func TestKindCandidateRegistered(t *testing.T) {
	t.Parallel()
	kind, err := ParseKind("Candidate")
	if err != nil || kind != KindCandidate {
		t.Fatalf("ParseKind(Candidate) = %q, err = %v", kind, err)
	}
	if !slices.Contains(Kinds(), KindCandidate) {
		t.Fatalf("Kinds() does not contain KindCandidate: %v", Kinds())
	}
}

func TestParseProducerKindClosedEnumeration(t *testing.T) {
	t.Parallel()
	for _, accepted := range []string{"worker", "normalizer"} {
		kind, err := ParseProducerKind(accepted)
		if err != nil || string(kind) != accepted {
			t.Fatalf("ParseProducerKind(%q) = %q, err = %v", accepted, kind, err)
		}
	}
	for _, rejected := range []string{"", "Worker", "NORMALIZER", "publisher", "verifier"} {
		if _, err := ParseProducerKind(rejected); err == nil {
			t.Fatalf("ParseProducerKind(%q) unexpectedly accepted", rejected)
		}
	}
}

func TestCandidateValidateAcceptsValidRecords(t *testing.T) {
	t.Parallel()
	if err := validWorkerCandidate(t).Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want valid worker chain root", err)
	}
	if err := validNormalizerCandidate(t).Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want valid normalizer successor", err)
	}
}

func TestCandidateValidateRejectsStructurallyInvalidRecords(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		normalizer bool
		mutate     func(*Candidate)
	}{
		{name: "wrong apiVersion", mutate: func(c *Candidate) { c.APIVersion = "marshal.dev/v2" }},
		{name: "wrong kind", mutate: func(c *Candidate) { c.Kind = KindWorkerResult }},
		{name: "empty taskId", mutate: func(c *Candidate) { c.TaskID = "" }},
		{name: "malformed taskId", mutate: func(c *Candidate) { c.TaskID = "not an id" }},
		{name: "empty runId", mutate: func(c *Candidate) { c.RunID = "" }},
		{name: "empty attemptId", mutate: func(c *Candidate) { c.AttemptID = "" }},
		{name: "empty authorityNamespaceId", mutate: func(c *Candidate) { c.AuthorityNamespaceID = "" }},
		{name: "empty producer", mutate: func(c *Candidate) { c.Producer = "" }},
		{name: "malformed producer", mutate: func(c *Candidate) { c.Producer = "the worker" }},
		{name: "short baseSha", mutate: func(c *Candidate) { c.BaseSHA = "abc" }},
		{name: "non-hex baseSha", mutate: func(c *Candidate) { c.BaseSHA = strings.Repeat("z", 40) }},
		{name: "uppercase baseSha", mutate: func(c *Candidate) { c.BaseSHA = strings.Repeat("A", 40) }},
		{name: "contentDigest without prefix", mutate: func(c *Candidate) { c.ContentDigest = strings.Repeat("d", 64) }},
		{name: "contentDigest wrong length", mutate: func(c *Candidate) { c.ContentDigest = "sha256:" + strings.Repeat("d", 63) }},
		{name: "contentDigest non-hex", mutate: func(c *Candidate) { c.ContentDigest = "sha256:" + strings.Repeat("w", 64) }},
		{name: "producerKind outside enumeration", mutate: func(c *Candidate) { c.ProducerKind = "publisher" }},
		{name: "producerKind case-mangled", mutate: func(c *Candidate) { c.ProducerKind = "Worker" }},
		{name: "worker carrying predecessor", mutate: func(c *Candidate) { c.PredecessorCandidateDigest = testDigestValue("f") }},
		{name: "zero createdAt", mutate: func(c *Candidate) { c.CreatedAt = time.Time{} }},
		{name: "malformed allocationId", mutate: func(c *Candidate) { c.AllocationID = "not an id" }},
		{name: "normalizer missing predecessor", normalizer: true, mutate: func(c *Candidate) { c.PredecessorCandidateDigest = "" }},
		{name: "normalizer malformed predecessor", normalizer: true, mutate: func(c *Candidate) { c.PredecessorCandidateDigest = "not-a-digest" }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var candidate Candidate
			if test.normalizer {
				candidate = validNormalizerCandidate(t)
			} else {
				candidate = validWorkerCandidate(t)
			}
			test.mutate(&candidate)
			// Reseal so the detached digest gate is satisfied and the
			// rejection is provably produced by the structural rule.
			resealCandidate(t, &candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("Validate() unexpectedly accepted a structurally invalid Candidate")
			}
		})
	}
}

// TestCandidatePredecessorConditionalSemantics pins the ADR 0027 T1
// predecessor condition in both directions: worker Candidates are chain
// roots that must not carry predecessorCandidateDigest, while normalizer
// Candidates must carry a well-formed predecessorCandidateDigest. Each case
// reseals the detached digest first, so the outcome is provably produced by
// the predecessor condition itself rather than the digest gate.
func TestCandidatePredecessorConditionalSemantics(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		normalizer bool
		mutate     func(*Candidate)
		valid      bool
	}{
		{name: "worker without predecessor is the chain root", valid: true, mutate: func(*Candidate) {}},
		{name: "worker carrying predecessor is rejected", mutate: func(c *Candidate) { c.PredecessorCandidateDigest = testDigestValue("f") }},
		{name: "normalizer carrying predecessor is the successor form", normalizer: true, valid: true, mutate: func(*Candidate) {}},
		{name: "normalizer missing predecessor is rejected", normalizer: true, mutate: func(c *Candidate) { c.PredecessorCandidateDigest = "" }},
		{name: "normalizer with malformed predecessor is rejected", normalizer: true, mutate: func(c *Candidate) { c.PredecessorCandidateDigest = "not-a-digest" }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var candidate Candidate
			if test.normalizer {
				candidate = validNormalizerCandidate(t)
			} else {
				candidate = validWorkerCandidate(t)
			}
			test.mutate(&candidate)
			resealCandidate(t, &candidate)
			err := candidate.Validate()
			if test.valid && err != nil {
				t.Fatalf("Validate() error = %v, want the predecessor condition satisfied", err)
			}
			if test.valid {
				return
			}
			if err == nil {
				t.Fatal("Validate() unexpectedly accepted a Candidate violating the predecessor condition")
			}
			if !strings.Contains(err.Error(), "predecessor") {
				t.Fatalf("Validate() error = %v, want the predecessor condition rejection", err)
			}
		})
	}
}

func TestCandidateValidateRejectsMalformedStoredDigest(t *testing.T) {
	t.Parallel()
	for _, stored := range []string{
		"",
		strings.Repeat("e", 64),
		"sha256:" + strings.Repeat("e", 63),
		"sha256:" + strings.Repeat("w", 64),
	} {
		candidate := validWorkerCandidate(t)
		candidate.CandidateDigest = stored
		if err := candidate.Validate(); err == nil {
			t.Fatalf("Validate() unexpectedly accepted stored candidateDigest %q", stored)
		}
	}
}

// TestCandidateDetachedDigestRecomputeNegativeCases pins the ADR 0027 T1
// gate: after tampering with any single field, the recomputed detached
// digest must differ from the stored candidateDigest and Validate must
// reject the record fail-closed.
func TestCandidateDetachedDigestRecomputeNegativeCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		normalizer bool
		mutate     func(*Candidate)
	}{
		{name: "taskId", mutate: func(c *Candidate) { c.TaskID = "task-02" }},
		{name: "runId", mutate: func(c *Candidate) { c.RunID = "run-02" }},
		{name: "attemptId", mutate: func(c *Candidate) { c.AttemptID = "attempt-02" }},
		{name: "authorityNamespaceId", mutate: func(c *Candidate) { c.AuthorityNamespaceID = testDigestValue("b") }},
		{name: "baseSha", mutate: func(c *Candidate) { c.BaseSHA = testSHA("9") }},
		{name: "contentDigest", mutate: func(c *Candidate) { c.ContentDigest = testDigestValue("c") }},
		{name: "producerKind", mutate: func(c *Candidate) { c.ProducerKind = ProducerKindNormalizer }},
		{name: "producer", mutate: func(c *Candidate) { c.Producer = "worker-2" }},
		{name: "createdAt", mutate: func(c *Candidate) { c.CreatedAt = c.CreatedAt.Add(time.Second) }},
		{name: "allocationId", mutate: func(c *Candidate) { c.AllocationID = "allocation-01" }},
		{name: "generation", mutate: func(c *Candidate) { c.Generation = 3 }},
		{name: "predecessorCandidateDigest", normalizer: true, mutate: func(c *Candidate) { c.PredecessorCandidateDigest = testDigestValue("f") }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var candidate Candidate
			if test.normalizer {
				candidate = validNormalizerCandidate(t)
			} else {
				candidate = validWorkerCandidate(t)
			}
			if err := candidate.Validate(); err != nil {
				t.Fatalf("baseline Validate() error = %v", err)
			}
			stored := candidate.CandidateDigest
			test.mutate(&candidate)
			recomputed, err := candidate.Digest()
			if err != nil {
				t.Fatalf("Digest() error = %v", err)
			}
			if recomputed == stored {
				t.Fatalf("tampering with %s must change the detached digest", test.name)
			}
			if err := candidate.Validate(); err == nil {
				t.Fatalf("Validate() unexpectedly accepted a Candidate tampered at %s", test.name)
			}
		})
	}
}

// TestCandidateValidateRejectsForeignStoredDigest pins the recomputation
// gate for the digest field itself: the detached digest is computed with
// candidateDigest stripped, so tampering with the stored digest leaves the
// recomputation unchanged — Validate must still reject because the stored
// value no longer matches the recomputed identity.
func TestCandidateValidateRejectsForeignStoredDigest(t *testing.T) {
	t.Parallel()
	candidate := validWorkerCandidate(t)
	candidate.CandidateDigest = testDigestValue("f")
	recomputed, err := candidate.Digest()
	if err != nil {
		t.Fatalf("Digest() error = %v", err)
	}
	if recomputed == candidate.CandidateDigest {
		t.Fatal("the detached digest must ignore the stored candidateDigest field")
	}
	if err := candidate.Validate(); err == nil {
		t.Fatal("Validate() unexpectedly accepted a foreign stored candidateDigest")
	}
}

func TestCandidateDigestIsDetached(t *testing.T) {
	t.Parallel()
	candidate := validWorkerCandidate(t)
	stored := candidate.CandidateDigest
	if !strings.HasPrefix(stored, "sha256:") || len(stored) != len("sha256:")+64 {
		t.Fatalf("Digest() = %q, want a sha256 digest", stored)
	}

	// The digest is computed with candidateDigest stripped, so arbitrary
	// stored values do not influence the recomputation.
	stripped := candidate
	stripped.CandidateDigest = ""
	digest, err := stripped.Digest()
	if err != nil || digest != stored {
		t.Fatalf("stripped Digest() = %q, err = %v, want %q", digest, err, stored)
	}
	stripped.CandidateDigest = testDigestValue("f")
	digest, err = stripped.Digest()
	if err != nil || digest != stored {
		t.Fatalf("Digest() must ignore the stored candidateDigest: %q, err = %v", digest, err)
	}

	// Recomputation is stable for the identical record.
	recomputed, err := candidate.Digest()
	if err != nil || recomputed != stored {
		t.Fatalf("detached digest is not stable: %q vs %q (err=%v)", recomputed, stored, err)
	}
}

// TestCandidateContentAndRecordIdentityIndependent pins the two-layer
// digest semantics of ADR 0027 §2.4: contentDigest is the content-addressed
// identity of the candidate bytes and survives record-level changes, while
// candidateDigest is the record identity and changes with any identity
// field. The two identities are never interchangeable.
func TestCandidateContentAndRecordIdentityIndependent(t *testing.T) {
	t.Parallel()
	candidate := validWorkerCandidate(t)

	// The same content admitted by a different attempt keeps its content
	// identity and gains a distinct record identity.
	twin := candidate
	twin.AttemptID = "attempt-02"
	twin.CandidateDigest = ""
	twinDigest, err := twin.Digest()
	if err != nil {
		t.Fatalf("Digest() error = %v", err)
	}
	if twin.ContentDigest != candidate.ContentDigest {
		t.Fatalf("content identity must survive record-level changes: %q vs %q", twin.ContentDigest, candidate.ContentDigest)
	}
	if twinDigest == candidate.CandidateDigest {
		t.Fatal("distinct records must carry distinct record identities even with identical contentDigest")
	}
	if twin.Equal(candidate) {
		t.Fatal("records with distinct identity must not be Equal even with identical contentDigest")
	}

	// Across the chain, the normalizer keeps the task/run/attempt/base
	// bindings of the worker while content and record identities differ.
	worker := validWorkerCandidate(t)
	normalizer := validNormalizerCandidate(t)
	if normalizer.TaskID != worker.TaskID || normalizer.RunID != worker.RunID || normalizer.AttemptID != worker.AttemptID || normalizer.BaseSHA != worker.BaseSHA {
		t.Fatal("chain records must agree on task/run/attempt/base bindings")
	}
	if normalizer.ContentDigest == worker.ContentDigest {
		t.Fatal("normalization output must carry its own content identity")
	}
	if normalizer.CandidateDigest == worker.CandidateDigest {
		t.Fatal("chain records must carry distinct record identities")
	}
}

// TestCandidatePredecessorChainLinkage pins the chain integrity assertions
// at the type level: the normalizer's predecessorCandidateDigest references
// the worker's candidateDigest (record identity), every sealed record
// recomputes to its stored digest, and tampering with the predecessor
// record changes its record identity so the chain reference can no longer
// resolve to a valid record (fail closed).
func TestCandidatePredecessorChainLinkage(t *testing.T) {
	t.Parallel()
	worker := validWorkerCandidate(t)
	normalizer := validNormalizerCandidate(t)

	if normalizer.PredecessorCandidateDigest != worker.CandidateDigest {
		t.Fatalf("predecessor link = %q, want the worker record identity %q", normalizer.PredecessorCandidateDigest, worker.CandidateDigest)
	}
	for _, candidate := range []Candidate{worker, normalizer} {
		if err := candidate.Validate(); err != nil {
			t.Fatalf("Validate() error = %v, want a sealed chain record", err)
		}
		recomputed, err := candidate.Digest()
		if err != nil {
			t.Fatalf("Digest() error = %v", err)
		}
		if recomputed != candidate.CandidateDigest {
			t.Fatalf("recomputed digest %q differs from stored %q", recomputed, candidate.CandidateDigest)
		}
	}

	tampered := worker
	tampered.ContentDigest = testDigestValue("c")
	if err := tampered.Validate(); err == nil {
		t.Fatal("Validate() unexpectedly accepted a tampered predecessor record")
	}
	recomputed, err := tampered.Digest()
	if err != nil {
		t.Fatalf("Digest() error = %v", err)
	}
	if recomputed == normalizer.PredecessorCandidateDigest {
		t.Fatal("tampering with the predecessor must change its record identity")
	}
}

// TestCandidateWireShapeMatchesSchemaFieldTable pins the verbatim alignment
// gate between the Go type and schemas/candidate-record.schema.json: sealed
// records serialize to exactly the frozen field table, every required field
// is always present, and optional fields stay absent until declared.
func TestCandidateWireShapeMatchesSchemaFieldTable(t *testing.T) {
	t.Parallel()
	required := []string{"apiVersion", "kind", "taskId", "runId", "attemptId", "authorityNamespaceId", "baseSha", "contentDigest", "producerKind", "producer", "createdAt", "candidateDigest"}

	worker := validWorkerCandidate(t)
	data, err := json.Marshal(worker)
	if err != nil {
		t.Fatalf("marshal worker: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode worker wire document: %v", err)
	}
	if decoded["apiVersion"] != "marshal.dev/v1alpha1" || decoded["kind"] != "Candidate" {
		t.Fatalf("worker wire envelope = %v/%v", decoded["apiVersion"], decoded["kind"])
	}
	for _, key := range required {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("worker wire document is missing required field %q", key)
		}
	}
	for _, optional := range []string{"predecessorCandidateDigest", "allocationId", "generation"} {
		if _, ok := decoded[optional]; ok {
			t.Fatalf("worker wire document must omit optional field %q", optional)
		}
	}
	if len(decoded) != len(required) {
		t.Fatalf("worker wire document carries %d fields, want exactly the %d required fields", len(decoded), len(required))
	}

	normalizer := validNormalizerCandidate(t)
	data, err = json.Marshal(normalizer)
	if err != nil {
		t.Fatalf("marshal normalizer: %v", err)
	}
	var normalizerDecoded map[string]any
	if err := json.Unmarshal(data, &normalizerDecoded); err != nil {
		t.Fatalf("decode normalizer wire document: %v", err)
	}
	if _, ok := normalizerDecoded["predecessorCandidateDigest"]; !ok {
		t.Fatal("normalizer wire document must carry predecessorCandidateDigest")
	}
	if len(normalizerDecoded) != len(required)+1 {
		t.Fatalf("normalizer wire document carries %d fields, want %d", len(normalizerDecoded), len(required)+1)
	}
}

// TestCandidateWireRoundTripPreservesDetachedDigest pins that the detached
// digest is stable across the wire form: a sealed record decoded from its
// JSON serialization still validates and recomputes to the stored record
// identity, which is exactly what digest-verified admission consumes.
func TestCandidateWireRoundTripPreservesDetachedDigest(t *testing.T) {
	t.Parallel()
	for _, candidate := range []Candidate{validWorkerCandidate(t), validNormalizerCandidate(t)} {
		data, err := json.Marshal(candidate)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var decoded Candidate
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if !decoded.Equal(candidate) {
			t.Fatalf("wire round trip changed the record: %+v", decoded)
		}
		if err := decoded.Validate(); err != nil {
			t.Fatalf("Validate() after round trip error = %v", err)
		}
		recomputed, err := decoded.Digest()
		if err != nil {
			t.Fatalf("Digest() after round trip error = %v", err)
		}
		if recomputed != candidate.CandidateDigest {
			t.Fatalf("round-trip digest %q differs from stored %q", recomputed, candidate.CandidateDigest)
		}
	}
}

func TestCandidateEqual(t *testing.T) {
	t.Parallel()
	candidate := validWorkerCandidate(t)
	identical := validWorkerCandidate(t)
	if !candidate.Equal(identical) {
		t.Fatal("sealed copies of the same Candidate must be Equal")
	}
	for name, mutate := range map[string]func(*Candidate){
		"taskId":                     func(c *Candidate) { c.TaskID = "task-02" },
		"contentDigest":              func(c *Candidate) { c.ContentDigest = testDigestValue("c") },
		"candidateDigest":            func(c *Candidate) { c.CandidateDigest = testDigestValue("f") },
		"predecessorCandidateDigest": func(c *Candidate) { c.PredecessorCandidateDigest = testDigestValue("f") },
		"createdAt":                  func(c *Candidate) { c.CreatedAt = c.CreatedAt.Add(time.Second) },
		"generation":                 func(c *Candidate) { c.Generation = 1 },
	} {
		mutated := candidate
		mutate(&mutated)
		if candidate.Equal(mutated) {
			t.Fatalf("Equal must detect a difference in %s", name)
		}
	}
}
