package review

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/selfidentity"
)

const ObjectiveReviewerID = "marshal-order-quote-v1"

// ObjectivePolicy is a process-local admission supplied only after the current
// owner proves a new Task's exact draft, approval and creating obligation. It
// is not serialized in a request or inferred from a Decision's reviewer tag.
type ObjectivePolicy struct {
	NodeID       string
	OracleDigest string
	Command      domain.TaskCommand
	// Digests come from the original system/marshal-verifier journal event,
	// not from a report that merely declares status=pass.
	VerificationDigest     string
	ArtifactManifestDigest string
	ReadEvidence           func(int64, ...string) ([]byte, error)
}

func validateObjective(input DecisionInput, packet domain.ReviewPacket) error {
	fail := errors.New("review: objective evidence or current Task admission required")
	p := input.Objective
	if p == nil || p.ReadEvidence == nil || input.Task.Publication.Required || input.Task.Acceptance.AllowNoChange || len(input.Task.Acceptance.Commands) != 1 ||
		!reflect.DeepEqual(input.Task.Acceptance.Commands[0], p.Command) || !p.Command.Required || p.Command.BaselinePolicy != "none" ||
		p.Command.ID != "quote-team-"+p.NodeID || len(p.Command.Argv) < 7 || p.OracleDigest != "sha256:"+p.Command.Argv[6] ||
		input.Report.Status != "pass" || input.Report.CandidateDigest == "" || input.Report.Observed.ChangedFileCount == 0 || len(packet.PreviousBlockingFindings) != 0 {
		return fail
	}
	checks, scope := 0, ""
	switch p.NodeID {
	case "service":
		checks, scope = 33, "api"
	case "client":
		checks, scope = 10, "client"
	case "integration":
		checks, scope = 34, "integration"
	default:
		return fail
	}
	rawReport, _ := json.Marshal(input.Report)
	rawManifest, _ := json.Marshal(input.Manifest)
	reportDigest, re := canonical.DigestJSON(rawReport)
	manifestDigest, me := canonical.DigestJSON(rawManifest)
	if re != nil || me != nil || p.VerificationDigest != reportDigest || p.ArtifactManifestDigest != manifestDigest || packet.VerificationDigest != reportDigest || packet.ArtifactManifestDigest != manifestDigest ||
		packet.CandidateDigest != input.Report.CandidateDigest || packet.DiffDigest != input.Report.Observed.DiffDigest || packet.SnapshotDigest != input.Report.Observed.SnapshotDigest ||
		validateCandidateBinding(input.Report, input.Manifest) != nil {
		return fail
	}
	want := map[string]bool{"repository:integrity": true, "diff:observe": true, "scope:changed-paths": true, "format:normalize": true, "command:" + p.Command.ID: true}
	for _, d := range input.Task.Deliverables {
		if !d.Required {
			return fail
		}
		want["artifact:"+d.ID] = true
	}
	seen := map[string]bool{}
	commandEvidence := []string(nil)
	for _, g := range input.Report.Gates {
		if !want[g.ID] || seen[g.ID] || (!g.Required && g.ID != "format:normalize") || g.Status != "pass" {
			return fail
		}
		seen[g.ID] = true
		if g.ID == "command:"+p.Command.ID {
			c := g.Command
			if c == nil || !slices.Equal(c.Argv, p.Command.Argv) || c.CWD != p.Command.CWD || !filepath.IsAbs(c.Executable) || c.ExitCode == nil || *c.ExitCode != 0 || c.Signal != nil || c.Truncated || c.BaselineStatus != "not-run" || c.StartedAt.IsZero() || c.CompletedAt.Before(c.StartedAt) {
				return fail
			}
			commandEvidence = g.Evidence
		} else if g.Command != nil {
			return fail
		}
	}
	if len(seen) != len(want) || len(commandEvidence) != 2 {
		return fail
	}
	logs := map[string][]byte{}
	for _, a := range input.Manifest.Artifacts {
		if a.Required && a.Status != "validated" {
			return fail
		}
		if a.Kind != "command-log" || !slices.Contains(a.RelatedGates, "command:"+p.Command.ID) {
			continue
		}
		if a.Producer != "verifier" || a.Status != "validated" || a.PathRoot != "run" || a.Truncated || a.Redacted || a.ByteSize < 0 || a.ByteSize > p.Command.MaxLogBytes || !slices.Contains(commandEvidence, "artifact://"+a.ID) {
			return fail
		}
		name := ""
		for _, stream := range []string{"stdout", "stderr"} {
			if a.RelativePath == "logs/"+p.Command.ID+"."+stream+".log" {
				name = stream
			}
		}
		_, duplicate := logs[name]
		if name == "" || duplicate {
			return fail
		}
		data, err := p.ReadEvidence(p.Command.MaxLogBytes+1, strings.Split(path.Clean(a.RelativePath), "/")...)
		if err != nil || int64(len(data)) != a.ByteSize || canonical.DigestBytes(data) != a.Digest {
			return fail
		}
		logs[name] = data
	}
	stdout, outOK := logs["stdout"]
	stderr, errOK := logs["stderr"]
	var observed struct {
		Checks int    `json:"checks"`
		Scope  string `json:"scope"`
	}
	decoder := json.NewDecoder(bytes.NewReader(stdout))
	decoder.DisallowUnknownFields()
	if !outOK || !errOK || len(stderr) != 0 || decoder.Decode(&observed) != nil || observed.Checks != checks || observed.Scope != scope {
		return fail
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return fail
	}
	return nil
}

// BuildObjectiveDecision does not grant authority or persist anything. The
// caller must still import it against the current Run and exact owner facts.
func BuildObjectiveDecision(input DecisionInput, packet domain.ReviewPacket, packetData []byte, now time.Time) ([]byte, error) {
	if err := validateObjective(input, packet); err != nil {
		return nil, err
	}
	digest, err := canonical.DigestJSON(packetData)
	if err != nil {
		return nil, err
	}
	decision := domain.ReviewDecision{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindReviewDecision, TaskID: input.TaskID, RunID: input.RunID, ReviewRound: input.ReviewRound, SpecDigest: input.SpecDigest, ReviewPacketDigest: digest, VerificationDigest: packet.VerificationDigest, ArtifactManifestDigest: packet.ArtifactManifestDigest, EvidenceDigest: packet.EvidenceDigest, Reviewer: domain.Reviewer{Type: "system", ID: ObjectiveReviewerID}, Verdict: "accept", Summary: "冻结业务 oracle 与全部必需证据独立验收通过", BlockingFindings: []domain.Finding{}, PublicationRecommendation: "do-not-publish", MergeRecommendation: "do-not-merge", DecidedAt: now.UTC()}
	decision.NonBlockingFindings = []domain.Finding{}
	if packet.LocalSelfIdentityBinding != nil {
		decision.LocalSelfIdentityBindingDigest, err = selfidentity.DigestReviewBinding(*packet.LocalSelfIdentityBinding)
		if err != nil {
			return nil, err
		}
	}
	return renderDecisionRecord(decision)
}
