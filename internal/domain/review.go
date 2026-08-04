package domain

import "time"

type ReviewPacket struct {
	APIVersion               APIVersion        `json:"apiVersion"`
	Kind                     Kind              `json:"kind"`
	TaskID                   string            `json:"taskId"`
	RunID                    string            `json:"runId"`
	ReviewRound              uint              `json:"reviewRound"`
	SpecDigest               string            `json:"specDigest"`
	BaseSHA                  string            `json:"baseSha"`
	SnapshotDigest           string            `json:"snapshotDigest"`
	DiffDigest               string            `json:"diffDigest"`
	VerificationDigest       string            `json:"verificationDigest"`
	ArtifactManifestDigest   string            `json:"artifactManifestDigest"`
	WorkerResultDigests      []string          `json:"workerResultDigests"`
	EvidenceDigest           string            `json:"evidenceDigest"`
	Inputs                   PacketInputs      `json:"inputs"`
	PreviousBlockingFindings []PreviousFinding `json:"previousBlockingFindings"`
	GeneratedAt              time.Time         `json:"generatedAt"`
}

type PacketInputs struct {
	TaskSpec           string   `json:"taskSpec"`
	Patch              string   `json:"patch"`
	VerificationReport string   `json:"verificationReport"`
	ArtifactManifest   string   `json:"artifactManifest"`
	WorkerResults      []string `json:"workerResults"`
}

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

type Reviewer struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Model string `json:"model,omitempty"`
}

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

type PreviousFinding struct {
	Finding
	EvidenceDigest     string `json:"evidenceDigest"`
	SnapshotDigest     string `json:"snapshotDigest"`
	VerificationDigest string `json:"verificationDigest"`
}

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
