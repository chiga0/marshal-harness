package productionruntime

import (
	"errors"
	"os"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/runstore"
	"github.com/chiga0/marshal-harness/internal/selfidentity"
)

const (
	localDispatchObservationName = "local-self-identity-dispatch.json"
	localIngressObservationName  = "local-self-identity-ingress.json"
)

func (l *CompositionLedger) freshLocalSelfIdentity() (selfidentity.LocalSelfIdentityObservationV1, bool, error) {
	if l.entryLocalSelfIdentity == nil && l.observeLocalSelfIdentity == nil {
		return selfidentity.LocalSelfIdentityObservationV1{}, false, nil
	}
	if l.entryLocalSelfIdentity == nil || l.observeLocalSelfIdentity == nil {
		return selfidentity.LocalSelfIdentityObservationV1{}, false, application.NewError("local-self-identity", application.ReasonAuthorityConflict)
	}
	fresh, err := l.observeLocalSelfIdentity()
	if err != nil || selfidentity.SameSubject(*l.entryLocalSelfIdentity, fresh) != nil {
		return selfidentity.LocalSelfIdentityObservationV1{}, false, application.NewError("local-self-identity", application.ReasonAuthorityConflict)
	}
	return fresh, true, nil
}

func readOptionalLocalObservation(directory *runstore.BoundDirectory, name string) (selfidentity.LocalSelfIdentityObservationV1, bool, error) {
	observation, err := selfidentity.ReadPhaseObservationIn(directory, name)
	if err == nil {
		return observation, true, nil
	}
	if errors.Is(err, os.ErrNotExist) || os.IsNotExist(err) {
		return selfidentity.LocalSelfIdentityObservationV1{}, false, nil
	}
	return selfidentity.LocalSelfIdentityObservationV1{}, false, err
}

func persistOrReuseLocalObservation(directory *runstore.BoundDirectory, name string, fresh selfidentity.LocalSelfIdentityObservationV1) (selfidentity.LocalSelfIdentityObservationV1, error) {
	stored, found, err := readOptionalLocalObservation(directory, name)
	if err != nil {
		return selfidentity.LocalSelfIdentityObservationV1{}, err
	}
	if found {
		if selfidentity.SameSubject(stored, fresh) != nil {
			return selfidentity.LocalSelfIdentityObservationV1{}, application.NewError("local-self-identity", application.ReasonAuthorityConflict)
		}
		return stored, nil
	}
	persisted, err := selfidentity.PersistPhaseObservationIn(directory, name, fresh)
	if err != nil {
		return selfidentity.LocalSelfIdentityObservationV1{}, err
	}
	return persisted, nil
}

// persistLocalDispatchObservation freezes the exact Core observation after
// the durable Attempt identity exists and before the prepared process may be
// started. Replays reuse the immutable first observation after proving it is
// still the same activation/executable subject.
func (l *CompositionLedger) persistLocalDispatchObservation(attemptID string) error {
	fresh, configured, err := l.freshLocalSelfIdentity()
	if err != nil || !configured {
		return err
	}
	directory, err := runstore.OpenOrCreateDirectoryUnderLease(l.runLease, "attempts", attemptID)
	if err != nil {
		return err
	}
	defer directory.Close()
	stored, err := persistOrReuseLocalObservation(directory, localDispatchObservationName, fresh)
	if err != nil || selfidentity.SameSubject(*l.entryLocalSelfIdentity, stored) != nil {
		return application.NewError("local-self-identity-dispatch", application.ReasonAuthorityConflict)
	}
	return directory.Recheck()
}

// localDispatchObservationDigest rechecks the descriptor-bound dispatch
// record immediately before the Run-start CAS. The runstore then reads the
// same immutable record again while holding its exclusive mutation guard,
// closing the pathname/TOCTOU gap.
func (l *CompositionLedger) localDispatchObservationDigest(attemptID string) (string, error) {
	fresh, configured, err := l.freshLocalSelfIdentity()
	if err != nil || !configured {
		return "", err
	}
	directory, err := runstore.OpenDirectoryUnderLease(l.runLease, "attempts", attemptID)
	if err != nil {
		return "", err
	}
	defer directory.Close()
	dispatch, err := selfidentity.ReadPhaseObservationIn(directory, localDispatchObservationName)
	if err != nil || selfidentity.SameSubject(*l.entryLocalSelfIdentity, dispatch) != nil || selfidentity.SameSubject(dispatch, fresh) != nil {
		return "", application.NewError("local-self-identity-dispatch", application.ReasonAuthorityConflict)
	}
	return dispatch.ObservationDigest, nil
}

// localIngressObservationDigests records/reuses the Core observation made
// before ResultIngress. A committed result may only replay an already
// persisted ingress observation; it cannot be rebound to recovery-time bytes.
func (l *CompositionLedger) localIngressObservationDigests(directory *runstore.BoundDirectory, committed bool) (string, string, error) {
	fresh, configured, err := l.freshLocalSelfIdentity()
	if err != nil || !configured {
		return "", "", err
	}
	dispatch, err := selfidentity.ReadPhaseObservationIn(directory, localDispatchObservationName)
	if err != nil || selfidentity.SameSubject(*l.entryLocalSelfIdentity, dispatch) != nil || selfidentity.SameSubject(dispatch, fresh) != nil {
		return "", "", application.NewError("local-self-identity-ingress", application.ReasonAuthorityConflict)
	}
	ingress, found, err := readOptionalLocalObservation(directory, localIngressObservationName)
	if err != nil {
		return "", "", err
	}
	if committed && !found {
		return "", "", application.NewError("local-self-identity-ingress", application.ReasonAuthorityConflict)
	}
	if !found {
		ingress, err = selfidentity.PersistPhaseObservationIn(directory, localIngressObservationName, fresh)
		if err != nil {
			return "", "", err
		}
	}
	if selfidentity.SameSubject(dispatch, ingress) != nil || selfidentity.SameSubject(ingress, fresh) != nil {
		return "", "", application.NewError("local-self-identity-ingress", application.ReasonAuthorityConflict)
	}
	return dispatch.ObservationDigest, ingress.ObservationDigest, directory.Recheck()
}
