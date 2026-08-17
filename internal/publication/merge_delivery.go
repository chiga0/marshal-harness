package publication

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
)

const (
	mergeDeliveryReady       = "ready"
	mergeDeliveryMerge       = "merge"
	maxMergeDeliveryAttempts = 3
)

type mergeDeliveryAttempt struct {
	IntentDigest  string    `json:"intentDigest"`
	Operation     string    `json:"operation"`
	Sequence      int       `json:"sequence"`
	StartedAt     time.Time `json:"startedAt"`
	AttemptDigest string    `json:"attemptDigest"`
}

type mergeDeliveryResult struct {
	AttemptDigest string    `json:"attemptDigest"`
	Sequence      int       `json:"sequence"`
	Outcome       string    `json:"outcome"`
	FailureClass  string    `json:"failureClass"`
	CompletedAt   time.Time `json:"completedAt"`
	ResultDigest  string    `json:"resultDigest"`
}

type mergeDeliveryHead struct {
	IntentDigest  string `json:"intentDigest"`
	Operation     string `json:"operation"`
	Sequence      int    `json:"sequence"`
	AttemptDigest string `json:"attemptDigest"`
	ResultDigest  string `json:"resultDigest,omitempty"`
	HeadDigest    string `json:"headDigest"`
}

func (head mergeDeliveryHead) digest() (string, error) {
	detached := head
	detached.HeadDigest = ""
	data, err := json.Marshal(detached)
	if err != nil {
		return "", err
	}
	return canonical.DigestJSON(data)
}

func mergeDeliveryHeadPath(runDir, intentDigest, operation string) string {
	return filepath.Join(runDir, "merge-delivery-heads", strings.TrimPrefix(intentDigest, "sha256:"), operation+".json")
}

func persistMergeDeliveryHead(runDir string, head mergeDeliveryHead) error {
	var err error
	head.HeadDigest, err = head.digest()
	if err != nil {
		return err
	}
	data, err := json.Marshal(head)
	if err != nil {
		return err
	}
	return atomicWrite(mergeDeliveryHeadPath(runDir, head.IntentDigest, head.Operation), append(data, '\n'))
}

const (
	mergeOutcomeSucceeded  = "succeeded"
	mergeOutcomeTransient  = "transient-failure"
	mergeOutcomePermanent  = "permanent-failure"
	mergeFailureNone       = "none"
	mergeFailureTransient  = "transient"
	mergeFailurePermission = "permission-denied"
	mergeFailureTarget     = "not-mergeable"
	mergeFailureIdentity   = "identity-mismatch"
	mergeFailureExhausted  = "retry-exhausted"
)

func authorizedMutation(ctx context.Context, input MergeInput, authorization authority.PublicationAuthorization, intent domain.SCMMergeIntent, runDir, operation string, preflight func(context.Context) error, mutate func() error) error {
	sequence, err := persistMergeDeliveryAttempt(runDir, intent.IntentDigest, operation, time.Now().UTC())
	if err != nil {
		return err
	}
	if err := recheckMergeAuthorization(input, authorization, intent, operation, time.Now().UTC()); err != nil {
		wrapped := port.Permanent(fmt.Errorf("%w: %v", port.ErrMergeIdentityMismatch, err))
		if persistErr := persistMergeDeliveryResult(runDir, intent.IntentDigest, operation, sequence, mergeOutcomePermanent, mergeFailureIdentity, time.Now().UTC()); persistErr != nil {
			return errors.Join(wrapped, persistErr)
		}
		return wrapped
	}
	if preflight == nil {
		return port.Permanent(errors.New("merge mutation requires an immediate target fence"))
	}
	if err := preflight(ctx); err != nil {
		_ = persistMergeDeliveryResult(runDir, intent.IntentDigest, operation, sequence, mergeOutcomePermanent, mergeFailureTarget, time.Now().UTC())
		return err
	}
	err = mutate()
	if err == nil {
		if persistErr := persistMergeDeliveryResult(runDir, intent.IntentDigest, operation, sequence, mergeOutcomeSucceeded, mergeFailureNone, time.Now().UTC()); persistErr != nil {
			return persistErr
		}
		return nil
	}
	if port.IsPermanent(err) {
		if persistErr := persistMergeDeliveryResult(runDir, intent.IntentDigest, operation, sequence, mergeOutcomePermanent, classifyMergeFailure(err), time.Now().UTC()); persistErr != nil {
			return errors.Join(err, persistErr)
		}
		return err
	}
	if sequence >= maxMergeDeliveryAttempts {
		wrapped := port.Permanent(fmt.Errorf("%w: %v", port.ErrMergeRetryExhausted, err))
		if persistErr := persistMergeDeliveryResult(runDir, intent.IntentDigest, operation, sequence, mergeOutcomePermanent, mergeFailureExhausted, time.Now().UTC()); persistErr != nil {
			return errors.Join(wrapped, persistErr)
		}
		return wrapped
	}
	if persistErr := persistMergeDeliveryResult(runDir, intent.IntentDigest, operation, sequence, mergeOutcomeTransient, mergeFailureTransient, time.Now().UTC()); persistErr != nil {
		return errors.Join(err, persistErr)
	}
	return err
}

func persistMergeDeliveryAttempt(runDir, intentDigest, operation string, now time.Time) (int, error) {
	if operation != mergeDeliveryReady && operation != mergeDeliveryMerge {
		return 0, errors.New("merge delivery operation is outside the closed set")
	}
	directory := filepath.Join(runDir, "merge-delivery", strings.TrimPrefix(intentDigest, "sha256:"), operation)
	count, err := validateMergeDeliveryLedger(directory, intentDigest, operation)
	if err != nil {
		return 0, err
	}
	sequence := count + 1
	if sequence > maxMergeDeliveryAttempts {
		return 0, port.Permanent(port.ErrMergeRetryExhausted)
	}
	attempt := mergeDeliveryAttempt{IntentDigest: intentDigest, Operation: operation, Sequence: sequence, StartedAt: now}
	detached, err := json.Marshal(attempt)
	if err != nil {
		return 0, err
	}
	digest, err := canonical.DigestJSON(detached)
	if err != nil {
		return 0, err
	}
	attempt.AttemptDigest = digest
	data, err := json.Marshal(attempt)
	if err != nil {
		return 0, err
	}
	path := filepath.Join(directory, fmt.Sprintf("%03d-attempt.json", sequence))
	if _, err := os.Lstat(path); err == nil {
		return 0, errors.New("merge delivery attempt path already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	if err := putFileNoReplace(path, append(data, '\n')); err != nil {
		return 0, err
	}
	if err := persistMergeDeliveryHead(runDir, mergeDeliveryHead{IntentDigest: intentDigest, Operation: operation, Sequence: sequence, AttemptDigest: attempt.AttemptDigest}); err != nil {
		return 0, err
	}
	return sequence, nil
}

func persistMergeDeliveryResult(runDir, intentDigest, operation string, sequence int, outcome, failureClass string, now time.Time) error {
	directory := filepath.Join(runDir, "merge-delivery", strings.TrimPrefix(intentDigest, "sha256:"), operation)
	attemptData, err := os.ReadFile(filepath.Join(directory, fmt.Sprintf("%03d-attempt.json", sequence)))
	if err != nil {
		return err
	}
	var attempt mergeDeliveryAttempt
	if err := json.Unmarshal(attemptData, &attempt); err != nil {
		return err
	}
	if err := validateMergeDeliveryAttempt(attempt, intentDigest, operation, sequence); err != nil {
		return err
	}
	result := mergeDeliveryResult{AttemptDigest: attempt.AttemptDigest, Sequence: sequence, Outcome: outcome, FailureClass: failureClass, CompletedAt: now}
	detached, err := json.Marshal(result)
	if err != nil {
		return err
	}
	result.ResultDigest, err = canonical.DigestJSON(detached)
	if err != nil {
		return err
	}
	if err := validateMergeDeliveryResult(result, attempt); err != nil {
		return err
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	path := filepath.Join(directory, fmt.Sprintf("%03d-result.json", sequence))
	if _, err := os.Lstat(path); err == nil {
		return errors.New("merge delivery result path already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := putFileNoReplace(path, append(data, '\n')); err != nil {
		return err
	}
	return persistMergeDeliveryHead(runDir, mergeDeliveryHead{IntentDigest: intentDigest, Operation: operation, Sequence: sequence, AttemptDigest: attempt.AttemptDigest, ResultDigest: result.ResultDigest})
}

func validateMergeDeliveryLedger(directory, intentDigest, operation string) (int, error) {
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	entrySet := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || (!strings.HasSuffix(entry.Name(), "-attempt.json") && !strings.HasSuffix(entry.Name(), "-result.json")) {
			return 0, port.Permanent(errors.New("merge delivery ledger contains an invalid entry"))
		}
		entrySet[entry.Name()] = true
	}
	count := 0
	for sequence := 1; sequence <= maxMergeDeliveryAttempts; sequence++ {
		attemptName := fmt.Sprintf("%03d-attempt.json", sequence)
		if !entrySet[attemptName] {
			for name := range entrySet {
				if name > attemptName {
					return 0, port.Permanent(errors.New("merge delivery ledger sequence is not contiguous"))
				}
			}
			break
		}
		data, readErr := os.ReadFile(filepath.Join(directory, attemptName))
		if readErr != nil {
			return 0, readErr
		}
		var attempt mergeDeliveryAttempt
		if err := json.Unmarshal(data, &attempt); err != nil {
			return 0, port.Permanent(err)
		}
		if err := validateMergeDeliveryAttempt(attempt, intentDigest, operation, sequence); err != nil {
			return 0, port.Permanent(err)
		}
		resultName := fmt.Sprintf("%03d-result.json", sequence)
		if entrySet[resultName] {
			resultData, readErr := os.ReadFile(filepath.Join(directory, resultName))
			if readErr != nil {
				return 0, readErr
			}
			var result mergeDeliveryResult
			if err := json.Unmarshal(resultData, &result); err != nil {
				return 0, port.Permanent(err)
			}
			if err := validateMergeDeliveryResult(result, attempt); err != nil {
				return 0, port.Permanent(err)
			}
		}
		count++
	}
	for name := range entrySet {
		recognized := false
		for sequence := 1; sequence <= count; sequence++ {
			if name == fmt.Sprintf("%03d-attempt.json", sequence) || name == fmt.Sprintf("%03d-result.json", sequence) {
				recognized = true
				break
			}
		}
		if !recognized {
			return 0, port.Permanent(errors.New("merge delivery ledger contains a record outside the contiguous sequence"))
		}
	}
	if count > 0 {
		headData, readErr := os.ReadFile(mergeDeliveryHeadPath(filepath.Dir(filepath.Dir(filepath.Dir(directory))), intentDigest, operation))
		if readErr != nil {
			return 0, port.Permanent(errors.New("merge delivery ledger lacks its monotonic head"))
		}
		var head mergeDeliveryHead
		if err := json.Unmarshal(headData, &head); err != nil {
			return 0, port.Permanent(err)
		}
		recomputed, err := head.digest()
		if err != nil || recomputed != head.HeadDigest || head.IntentDigest != intentDigest || head.Operation != operation || head.Sequence != count {
			return 0, port.Permanent(errors.New("merge delivery head is missing, rolled back or divergent"))
		}
		lastAttemptData, err := os.ReadFile(filepath.Join(directory, fmt.Sprintf("%03d-attempt.json", count)))
		if err != nil {
			return 0, port.Permanent(err)
		}
		var lastAttempt mergeDeliveryAttempt
		if err := json.Unmarshal(lastAttemptData, &lastAttempt); err != nil || lastAttempt.AttemptDigest != head.AttemptDigest {
			return 0, port.Permanent(errors.New("merge delivery head does not bind the latest attempt"))
		}
		resultPath := filepath.Join(directory, fmt.Sprintf("%03d-result.json", count))
		resultData, resultErr := os.ReadFile(resultPath)
		if resultErr == nil {
			var result mergeDeliveryResult
			if err := json.Unmarshal(resultData, &result); err != nil || result.ResultDigest != head.ResultDigest {
				return 0, port.Permanent(errors.New("merge delivery head does not bind the latest result"))
			}
		} else if !errors.Is(resultErr, os.ErrNotExist) || head.ResultDigest != "" {
			return 0, port.Permanent(errors.New("merge delivery latest result was deleted or diverged"))
		}
	}
	return count, nil
}

func validateMergeDeliveryAttempt(attempt mergeDeliveryAttempt, intentDigest, operation string, sequence int) error {
	if attempt.IntentDigest != intentDigest || attempt.Operation != operation || attempt.Sequence != sequence || attempt.StartedAt.IsZero() {
		return errors.New("merge delivery attempt binding mismatch")
	}
	detached := attempt
	detached.AttemptDigest = ""
	data, err := json.Marshal(detached)
	if err != nil {
		return err
	}
	digest, err := canonical.DigestJSON(data)
	if err != nil || digest != attempt.AttemptDigest {
		return errors.New("merge delivery attempt digest mismatch")
	}
	return nil
}

func validateMergeDeliveryResult(result mergeDeliveryResult, attempt mergeDeliveryAttempt) error {
	if result.AttemptDigest != attempt.AttemptDigest || result.Sequence != attempt.Sequence || result.CompletedAt.IsZero() {
		return errors.New("merge delivery result binding mismatch")
	}
	closedFailureClass := result.FailureClass == mergeFailurePermission || result.FailureClass == mergeFailureTarget ||
		result.FailureClass == mergeFailureIdentity || result.FailureClass == mergeFailureExhausted
	valid := (result.Outcome == mergeOutcomeSucceeded && result.FailureClass == mergeFailureNone) ||
		(result.Outcome == mergeOutcomeTransient && result.FailureClass == mergeFailureTransient) ||
		(result.Outcome == mergeOutcomePermanent && closedFailureClass)
	if !valid {
		return errors.New("merge delivery result outcome or failure class is outside the closed set")
	}
	detached := result
	detached.ResultDigest = ""
	data, err := json.Marshal(detached)
	if err != nil {
		return err
	}
	digest, err := canonical.DigestJSON(data)
	if err != nil || digest != result.ResultDigest {
		return errors.New("merge delivery result digest mismatch")
	}
	return nil
}

func classifyMergeFailure(err error) string {
	switch {
	case errors.Is(err, port.ErrMergePermissionDenied):
		return mergeFailurePermission
	case errors.Is(err, port.ErrMergeNotMergeable):
		return mergeFailureTarget
	case errors.Is(err, port.ErrMergeIdentityMismatch):
		return mergeFailureIdentity
	case errors.Is(err, port.ErrMergeRetryExhausted):
		return mergeFailureExhausted
	default:
		return mergeFailureIdentity
	}
}
