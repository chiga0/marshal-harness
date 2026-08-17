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

func authorizedMutation(ctx context.Context, input MergeInput, authorization authority.PublicationAuthorization, intent domain.SCMMergeIntent, runDir, operation string, mutate func() error) error {
	sequence, err := persistMergeDeliveryAttempt(runDir, intent.IntentDigest, operation, time.Now().UTC())
	if err != nil {
		return err
	}
	if err := recheckMergeAuthorization(input, authorization, intent, operation, time.Now().UTC()); err != nil {
		return port.Permanent(fmt.Errorf("%w: %v", port.ErrMergeIdentityMismatch, err))
	}
	err = mutate()
	if err == nil || port.IsPermanent(err) {
		return err
	}
	if sequence >= maxMergeDeliveryAttempts {
		return port.Permanent(fmt.Errorf("%w: %v", port.ErrMergeRetryExhausted, err))
	}
	return err
}

func persistMergeDeliveryAttempt(runDir, intentDigest, operation string, now time.Time) (int, error) {
	if operation != mergeDeliveryReady && operation != mergeDeliveryMerge {
		return 0, errors.New("merge delivery operation is outside the closed set")
	}
	directory := filepath.Join(runDir, "merge-delivery", strings.TrimPrefix(intentDigest, "sha256:"), operation)
	entries, err := os.ReadDir(directory)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	sequence := len(entries) + 1
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
	path := filepath.Join(directory, fmt.Sprintf("%03d.json", sequence))
	if _, err := os.Lstat(path); err == nil {
		return 0, errors.New("merge delivery attempt path already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	if err := atomicWrite(path, append(data, '\n')); err != nil {
		return 0, err
	}
	return sequence, nil
}
