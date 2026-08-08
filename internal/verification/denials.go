package verification

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/chiga0/marshal-harness/internal/adapter/denials"
	"github.com/chiga0/marshal-harness/internal/canonical"
)

// denialGateID is the fixed identifier of the ADR 0013 denial-summary gate.
const denialGateID = "denial-summary"

// denialAssessment carries the gate, the optional persisted evidence artifact
// and the graded counts that enter the VerificationReport summary.
type denialAssessment struct {
	Gate     Gate
	Artifact *Artifact
	Benign   int
	Fatal    int
	Present  bool
}

// assessDenialSummary implements the denial-summary gate: it reads the denial
// log of the newest attempt, reports benign/fatal counts, and enforces that a
// non-zero fatal count stays consistent with the adapter's fail-closed
// permissionDenied state. The gate is fail-closed: unreadable or malformed
// denial evidence is an error, never a pass.
func assessDenialSummary(runDirectory string, createdAt time.Time) (denialAssessment, error) {
	gate := Gate{ID: denialGateID, Category: "policy", Required: true, Status: "pass", Evidence: []string{}}
	attemptDir, attemptName, ok := latestAttemptDirectory(filepath.Join(runDirectory, "attempts"))
	if !ok {
		gate.Summary = "无 Attempt 记录，无 denial 证据"
		return denialAssessment{Gate: gate}, nil
	}
	outputDir := filepath.Join(attemptDir, "control", "output")
	logPath := filepath.Join(outputDir, denials.LogFileName)
	raw, err := os.ReadFile(logPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			gate.Summary = "无 permission 拒绝记录"
			return denialAssessment{Gate: gate}, nil
		}
		gate.Status = "error"
		gate.Summary = "读取 denial log 失败：" + err.Error()
		return denialAssessment{Gate: gate}, nil
	}
	records, err := denials.ParseLog(raw)
	if err != nil {
		gate.Status = "error"
		gate.Summary = "denial log 无效：" + err.Error()
		return denialAssessment{Gate: gate}, nil
	}
	summary := denials.Summarize(records)
	gate.Summary = fmt.Sprintf("denial-summary：benign=%d，fatal=%d", summary.Benign, summary.Fatal)
	assessment := denialAssessment{Gate: gate, Benign: summary.Benign, Fatal: summary.Fatal, Present: true}
	if consistent, detail := permissionStateConsistent(outputDir, summary.Fatal > 0); !consistent {
		assessment.Gate.Status = "fail"
		assessment.Gate.Summary = detail
		return assessment, nil
	}
	if summary.Fatal > 0 {
		assessment.Gate.Status = "fail"
		assessment.Gate.Summary = fmt.Sprintf("denial-summary：benign=%d，fatal=%d：FATAL 拒绝必须 fail-closed 终止 Attempt，不得进入 Verification", summary.Benign, summary.Fatal)
	}
	if err := atomicWrite(filepath.Join(runDirectory, denials.LogFileName), raw); err != nil {
		return assessment, err
	}
	assessment.Gate.Evidence = []string{"artifact://evidence:denial-log"}
	assessment.Artifact = &Artifact{
		ID: "evidence:denial-log", Kind: "denial-log", MediaType: "application/x-jsonlines", Producer: "system",
		Required: false, Status: "validated", PathRoot: "run", RelativePath: denials.LogFileName,
		ByteSize: int64(len(raw)), Digest: canonical.DigestBytes(raw), CreatedAt: createdAt,
		RelatedGates: []string{denialGateID}, Description: "Attempt " + attemptName + " 的 permission 拒绝分级证据",
	}
	return assessment, nil
}

// latestAttemptDirectory selects the attempt with the highest attemptNumber
// recorded in its worker-request.json; ties resolve lexicographically so the
// selection is deterministic. Worker denial evidence belongs to the attempt
// under verification, never to superseded attempts.
func latestAttemptDirectory(attemptsRoot string) (string, string, bool) {
	entries, err := os.ReadDir(attemptsRoot)
	if err != nil {
		return "", "", false
	}
	type candidate struct {
		name   string
		number int
	}
	candidates := make([]candidate, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		candidates = append(candidates, candidate{name: entry.Name(), number: attemptNumber(filepath.Join(attemptsRoot, entry.Name()))})
	}
	if len(candidates) == 0 {
		return "", "", false
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].number != candidates[j].number {
			return candidates[i].number < candidates[j].number
		}
		return candidates[i].name < candidates[j].name
	})
	latest := candidates[len(candidates)-1]
	return filepath.Join(attemptsRoot, latest.name), latest.name, true
}

func attemptNumber(attemptDir string) int {
	data, err := os.ReadFile(filepath.Join(attemptDir, "worker-request.json"))
	if err != nil {
		return 0
	}
	var request struct {
		AttemptNumber int `json:"attemptNumber"`
	}
	if json.Unmarshal(data, &request) != nil {
		return 0
	}
	return request.AttemptNumber
}

// permissionStateConsistent cross-checks the denial grading against the
// adapter's transcript metadata: fatal>0 must coincide with
// permissionDenied=true, and benign-only grading with permissionDenied=false.
// Adapters without a permissionDenied field are skipped.
func permissionStateConsistent(outputDir string, fatalObserved bool) (bool, string) {
	metas, err := filepath.Glob(filepath.Join(outputDir, "*-transcript-meta.json"))
	if err != nil {
		return false, "枚举 transcript 元数据失败：" + err.Error()
	}
	for _, metaPath := range metas {
		data, err := os.ReadFile(metaPath)
		if err != nil {
			return false, "读取 transcript 元数据失败：" + err.Error()
		}
		var meta struct {
			PermissionDenied *bool `json:"permissionDenied"`
		}
		if json.Unmarshal(data, &meta) != nil || meta.PermissionDenied == nil {
			continue
		}
		if *meta.PermissionDenied != fatalObserved {
			return false, fmt.Sprintf("denial 证据与 transcript permissionDenied=%v 状态不一致（fatal 观测=%v）", *meta.PermissionDenied, fatalObserved)
		}
	}
	return true, ""
}
