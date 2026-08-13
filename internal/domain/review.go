package domain

import "time"

// ReviewPacket bundles the exact evidence a Reviewer evaluates for one
// Review Round. Its digests bind the ReviewDecision to that evidence.
type ReviewPacket struct {
	APIVersion             APIVersion `json:"apiVersion"`
	Kind                   Kind       `json:"kind"`
	TaskID                 string     `json:"taskId"`
	RunID                  string     `json:"runId"`
	ReviewRound            uint       `json:"reviewRound"`
	SpecDigest             string     `json:"specDigest"`
	BaseSHA                string     `json:"baseSha"`
	SnapshotDigest         string     `json:"snapshotDigest"`
	DiffDigest             string     `json:"diffDigest"`
	VerificationDigest     string     `json:"verificationDigest"`
	ArtifactManifestDigest string     `json:"artifactManifestDigest"`
	WorkerResultDigests    []string   `json:"workerResultDigests"`
	EvidenceDigest         string     `json:"evidenceDigest"`
	// WorkerCandidateDigest is the ADR 0027 worker Candidate record identity
	// (the chain root recording the Worker's raw observed patch bytes).
	// Optional: Runs verified before Candidate adoption leave it empty, and
	// the field is omitted from the wire bytes so archived packets remain
	// byte-identical.
	WorkerCandidateDigest string `json:"workerCandidateDigest,omitempty"`
	// CandidateDigest is the ADR 0027 head Candidate record identity the
	// packet's evidence binds: the normalizer Candidate when normalization
	// changed bytes, otherwise the worker Candidate. Optional with the same
	// legacy omission semantics as WorkerCandidateDigest.
	CandidateDigest          string            `json:"candidateDigest,omitempty"`
	Inputs                   PacketInputs      `json:"inputs"`
	PreviousBlockingFindings []PreviousFinding `json:"previousBlockingFindings"`
	GeneratedAt              time.Time         `json:"generatedAt"`
}

// PacketInputs holds the raw input documents referenced by a ReviewPacket.
type PacketInputs struct {
	TaskSpec           string   `json:"taskSpec"`
	Patch              string   `json:"patch"`
	VerificationReport string   `json:"verificationReport"`
	ArtifactManifest   string   `json:"artifactManifest"`
	WorkerResults      []string `json:"workerResults"`
}

// ReviewDecision is a Reviewer's binding verdict for one Review Round. It
// must be bound to the exact evidence digests of the reviewed ReviewPacket.
type ReviewDecision struct {
	APIVersion                APIVersion `json:"apiVersion"`
	Kind                      Kind       `json:"kind"`
	TaskID                    string     `json:"taskId"`
	RunID                     string     `json:"runId"`
	ReviewRound               uint       `json:"reviewRound"`
	Reviewer                  Reviewer   `json:"reviewer"`
	SpecDigest                string     `json:"specDigest"`
	ReviewPacketDigest        string     `json:"reviewPacketDigest"`
	VerificationDigest        string     `json:"verificationDigest"`
	ArtifactManifestDigest    string     `json:"artifactManifestDigest"`
	EvidenceDigest            string     `json:"evidenceDigest"`
	Verdict                   string     `json:"verdict"`
	Summary                   string     `json:"summary"`
	BlockingFindings          []Finding  `json:"blockingFindings"`
	NonBlockingFindings       []Finding  `json:"nonBlockingFindings"`
	PublicationRecommendation string     `json:"publicationRecommendation"`
	MergeRecommendation       string     `json:"mergeRecommendation"`
	BlockerOwner              string     `json:"blockerOwner,omitempty"`
	DecidedAt                 time.Time  `json:"decidedAt"`
}

// Reviewer identifies who produced a ReviewDecision.
type Reviewer struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Model string `json:"model,omitempty"`
}

// Finding is a single review finding with severity and traceability back to
// a file, gate or artifact.
type Finding struct {
	ID              string `json:"id"`
	Severity        string `json:"severity"`
	Title           string `json:"title"`
	Description     string `json:"description"`
	RequiredOutcome string `json:"requiredOutcome,omitempty"`
	File            string `json:"file,omitempty"`
	Line            int    `json:"line,omitempty"`
	GateID          string `json:"gateId,omitempty"`
	ArtifactID      string `json:"artifactId,omitempty"`
}

// PreviousFinding is a blocking finding from an earlier Review Round,
// extended with the evidence digests required to detect stale fixes.
type PreviousFinding struct {
	Finding
	EvidenceDigest     string `json:"evidenceDigest"`
	SnapshotDigest     string `json:"snapshotDigest"`
	VerificationDigest string `json:"verificationDigest"`
	// CandidateDigest is the ADR 0027 head Candidate record identity of the
	// Review Round that raised the finding. Optional: findings raised by
	// pre-adoption (legacy) rounds leave it empty, which switches the
	// stale-fix judgement back to the SnapshotDigest+VerificationDigest
	// comparison. The field is omitted from the wire bytes when empty, so
	// archived findings keep their exact legacy serialization.
	CandidateDigest string `json:"candidateDigest,omitempty"`
}

// OutcomeBundle is the final, tamper-evident evidence record preserved for
// a Run that reached a terminal state, including blocked or failed Runs.
type OutcomeBundle struct {
	APIVersion          APIVersion `json:"apiVersion"`
	Kind                Kind       `json:"kind"`
	TaskID              string     `json:"taskId"`
	RunID               string     `json:"runId"`
	TerminalState       State      `json:"terminalState"`
	Verdict             string     `json:"verdict"`
	FinalReviewRound    uint       `json:"finalReviewRound"`
	FinalReviewDigest   string     `json:"finalReviewDigest"`
	FinalEvidenceDigest string     `json:"finalEvidenceDigest"`
	Summary             string     `json:"summary"`
	FindingCount        uint       `json:"findingCount"`
	RetentionPolicy     string     `json:"retentionPolicy"`
	GeneratedAt         time.Time  `json:"generatedAt"`
}
