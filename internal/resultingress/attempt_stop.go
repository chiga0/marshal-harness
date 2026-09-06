package resultingress

import (
	"errors"
	"math"
	"strings"
	"time"
)

var ErrStopTooLate = errors.New("resultingress: result admission already committed before stop")

// Stop intents are authority inputs, never provider observations. The caller
// must load and authenticate their sources while holding current Run authority.
// This contract is part of the ADR 0081 vertical implementation, not a public
// cancellation entry point on its own.
type AttemptStopCategory string

const (
	StopOperatorRequest AttemptStopCategory = "operator-request"
	StopAttemptDeadline AttemptStopCategory = "attempt-deadline-exceeded"
	StopRunDeadline     AttemptStopCategory = "run-deadline-exceeded"
)

type BusinessDeadlineWitness struct {
	SpecDigest               string `json:"specDigest"`
	CreationEventDigest      string `json:"creationEventDigest"`
	ProcessStartedFactDigest string `json:"processStartedFactDigest"`
	RunCreatedAt             string `json:"runCreatedAt"`
	ProcessStartedAt         string `json:"processStartedAt"`
	RunTimeoutSeconds        int64  `json:"runTimeoutSeconds"`
	AttemptTimeoutSeconds    int64  `json:"attemptTimeoutSeconds"`
	RunDeadline              string `json:"runDeadline"`
	AttemptDeadline          string `json:"attemptDeadline"`
}

type AttemptStopIntent struct {
	SchemaRevision        string                  `json:"schemaRevision"`
	RequestID             string                  `json:"requestId"`
	ExpectedSequence      uint64                  `json:"expectedSequence"`
	ExpectedAuthorityHead string                  `json:"expectedAuthorityHead"`
	OperatorUID           uint32                  `json:"operatorUid"`
	ObservedAt            string                  `json:"observedAt"`
	Category              AttemptStopCategory     `json:"category"`
	Deadline              BusinessDeadlineWitness `json:"deadline,omitempty,omitzero"`
	RequestDigest         string                  `json:"requestDigest"`
	IntentDigest          string                  `json:"intentDigest"`
}

func stopTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.UTC().Format(time.RFC3339Nano) != value {
		return time.Time{}, ErrAttemptAuthorityConflict
	}
	return parsed, nil
}

func businessDeadline(start string, seconds int64) (time.Time, error) {
	parsed, err := stopTime(start)
	if err != nil || seconds <= 0 || seconds > math.MaxInt64/int64(time.Second) {
		return time.Time{}, ErrAttemptAuthorityConflict
	}
	deadline := parsed.Add(time.Duration(seconds) * time.Second)
	if deadline.Year() > 9999 || !deadline.After(parsed) {
		return time.Time{}, ErrAttemptAuthorityConflict
	}
	return deadline, nil
}

// SealBusinessDeadline derives deadline bytes once from immutable sources.
// It does not prove those sources belong to a Run; production composition must
// verify specDigest/created event/process-started before calling it.
func SealBusinessDeadline(w BusinessDeadlineWitness) (BusinessDeadlineWitness, error) {
	if requireDigest("specDigest", w.SpecDigest) != nil || requireDigest("creationEventDigest", w.CreationEventDigest) != nil || requireDigest("processStartedFactDigest", w.ProcessStartedFactDigest) != nil {
		return BusinessDeadlineWitness{}, ErrAttemptAuthorityConflict
	}
	run, err := businessDeadline(w.RunCreatedAt, w.RunTimeoutSeconds)
	if err != nil {
		return BusinessDeadlineWitness{}, err
	}
	attempt, err := businessDeadline(w.ProcessStartedAt, w.AttemptTimeoutSeconds)
	if err != nil {
		return BusinessDeadlineWitness{}, err
	}
	created, _ := stopTime(w.RunCreatedAt)
	started, _ := stopTime(w.ProcessStartedAt)
	if started.Before(created) {
		return BusinessDeadlineWitness{}, ErrAttemptAuthorityConflict
	}
	w.RunDeadline = run.Format(time.RFC3339Nano)
	w.AttemptDeadline = attempt.Format(time.RFC3339Nano)
	return w, nil
}

func (w BusinessDeadlineWitness) Effective() (time.Time, AttemptStopCategory, error) {
	sealed, err := SealBusinessDeadline(w)
	if err != nil || sealed != w {
		return time.Time{}, "", ErrAttemptAuthorityConflict
	}
	run, _ := stopTime(w.RunDeadline)
	attempt, _ := stopTime(w.AttemptDeadline)
	if !run.After(attempt) {
		return run, StopRunDeadline, nil // Run wins exact ties.
	}
	return attempt, StopAttemptDeadline, nil
}

func (s AttemptStopIntent) requestDigest(identity AttemptIdentity) (string, error) {
	return canonicalDigest(struct {
		RunID                 string `json:"runId"`
		AttemptID             string `json:"attemptId"`
		ExpectedSequence      uint64 `json:"expectedSequence"`
		ExpectedAuthorityHead string `json:"expectedAuthorityHead"`
		RequestID             string `json:"requestId"`
	}{identity.RunID, identity.AttemptID, s.ExpectedSequence, s.ExpectedAuthorityHead, s.RequestID})
}

func (s AttemptStopIntent) intentDigest(identity AttemptIdentity) (string, error) {
	s.IntentDigest = ""
	return canonicalDigest(struct {
		Identity AttemptIdentity   `json:"identity"`
		Intent   AttemptStopIntent `json:"intent"`
	}{identity, s})
}

func SealAttemptStopIntent(identity AttemptIdentity, s AttemptStopIntent) (AttemptStopIntent, error) {
	s.SchemaRevision = "attempt-stop/v1"
	var err error
	s.RequestDigest, err = s.requestDigest(identity)
	if err != nil {
		return AttemptStopIntent{}, err
	}
	s.IntentDigest, err = s.intentDigest(identity)
	if err != nil {
		return AttemptStopIntent{}, err
	}
	if err := s.Validate(identity); err != nil {
		return AttemptStopIntent{}, err
	}
	return s, nil
}

func (s AttemptStopIntent) Validate(identity AttemptIdentity) error {
	if identity.Validate() != nil || s.SchemaRevision != "attempt-stop/v1" || s.RequestID == "" || len(s.RequestID) > 128 || strings.TrimSpace(s.RequestID) != s.RequestID || strings.ContainsAny(s.RequestID, "\x00\r\n") || s.ExpectedSequence == 0 || s.ExpectedSequence > 1<<53-1 || requireDigest("expectedAuthorityHead", s.ExpectedAuthorityHead) != nil {
		return ErrAttemptAuthorityConflict
	}
	observed, err := stopTime(s.ObservedAt)
	if err != nil {
		return err
	}
	switch s.Category {
	case StopOperatorRequest:
		if s.Deadline != (BusinessDeadlineWitness{}) {
			return ErrAttemptAuthorityConflict
		}
	case StopAttemptDeadline, StopRunDeadline:
		deadline, category, err := s.Deadline.Effective()
		if err != nil || category != s.Category || observed.Before(deadline) {
			return ErrAttemptAuthorityConflict
		}
	default:
		return ErrAttemptAuthorityConflict
	}
	request, err := s.requestDigest(identity)
	if err != nil || request != s.RequestDigest {
		return ErrAttemptAuthorityConflict
	}
	digest, err := s.intentDigest(identity)
	if err != nil || digest != s.IntentDigest {
		return ErrAttemptAuthorityConflict
	}
	return nil
}

func (s AttemptStopIntent) Eligibility() EligibilityTerminal {
	switch s.Category {
	case StopOperatorRequest:
		return EligibilityTerminal{Kind: EligibilityTerminalCompleted, CompletionReason: TerminalAttemptAborted}
	case StopAttemptDeadline, StopRunDeadline:
		return EligibilityTerminal{Kind: EligibilityTerminalCancelled, CancelReason: EligibilityCancelDeadlineExceeded}
	default:
		return EligibilityTerminal{}
	}
}
