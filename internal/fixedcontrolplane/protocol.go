package fixedcontrolplane

import (
	"errors"
	"regexp"
	"time"
)

const (
	ProtocolRevision   = "darwin-fixed-control-endpoint/v1"
	maxApplicationTime = 10 * time.Minute
)

var (
	ErrInvalid     = errors.New("fixedcontrolplane: invalid")
	ErrConflict    = errors.New("fixedcontrolplane: conflict")
	ErrUnavailable = errors.New("fixedcontrolplane: unavailable")
	// ErrAttemptStillRunning is a current observation, not a success receipt
	// or proof that the durable delivery pending has had no effects. A caller
	// may retry only the identical collect request within its original deadline.
	ErrAttemptStillRunning = errors.New("fixedcontrolplane: attempt is still running")
	digestPattern          = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// RequestBinding is the secret-free request identity authenticated before S3
// is allowed to append a delivery pending or call PublicApplicationPort.
type RequestBinding struct {
	RequestKeyDigest string `json:"requestKeyDigest"`
	RequestDigest    string `json:"requestDigest"`
	IntentDigest     string `json:"intentDigest"`
	Deadline         string `json:"deadline"`
}

func (binding RequestBinding) Validate(now time.Time) error {
	deadline, err := time.Parse(time.RFC3339Nano, binding.Deadline)
	if err != nil || deadline.Location() != time.UTC || !deadline.After(now) || deadline.After(now.Add(maxApplicationTime)) || !digestPattern.MatchString(binding.RequestKeyDigest) || !digestPattern.MatchString(binding.RequestDigest) || !digestPattern.MatchString(binding.IntentDigest) {
		return ErrInvalid
	}
	return nil
}
