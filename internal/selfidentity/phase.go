package selfidentity

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/evidencebinding"
	"golang.org/x/sys/unix"
)

const (
	VerificationBindingSchema = "marshal.local-self-identity-verification-binding.v2"
	ReviewBindingSchema       = "marshal.local-self-identity-review-binding.v2"
	phaseObservationByteLimit = 64 << 10
)

// LocalVerificationBindingV2 freezes the Core-owned observations that make
// verifier evidence applicable to one local Attempt. WorkerResult is not an
// input to this record.
type LocalVerificationBindingV2 = evidencebinding.VerificationIdentityBindingV2

// LocalReviewBindingV2 extends the frozen verification lineage with the
// Core-owned observation made before a ReviewPacket is produced.
type LocalReviewBindingV2 = evidencebinding.ReviewIdentityBindingV2

func BuildVerificationBinding(attemptID string, applicability LocalApplicabilityV2, dispatch, ingress, verification LocalSelfIdentityObservationV2) (LocalVerificationBindingV2, error) {
	for _, observation := range []LocalSelfIdentityObservationV2{dispatch, ingress, verification} {
		if err := ValidateObservation(observation); err != nil {
			return LocalVerificationBindingV2{}, err
		}
		if err := SameSubject(dispatch, observation); err != nil {
			return LocalVerificationBindingV2{}, err
		}
	}
	if attemptID == "" {
		return LocalVerificationBindingV2{}, errors.New("local verification binding requires attempt identity")
	}
	if err := ValidateApplicability(applicability, dispatch); err != nil {
		return LocalVerificationBindingV2{}, err
	}
	return LocalVerificationBindingV2{
		SchemaVersion: VerificationBindingSchema, SelfProfile: LocalProfile,
		ActivationDigest: dispatch.ActivationDigest, IdentitySubjectDigest: dispatch.IdentitySubjectDigest,
		AttemptID: attemptID, DispatchObservationDigest: dispatch.ObservationDigest,
		IngressObservationDigest: ingress.ObservationDigest, VerificationObservationDigest: verification.ObservationDigest,
		Applicability: applicability,
	}, nil
}

func ValidateVerificationBinding(binding LocalVerificationBindingV2, attemptID string, applicability LocalApplicabilityV2, dispatch, ingress, verification LocalSelfIdentityObservationV2) error {
	expected, err := BuildVerificationBinding(attemptID, applicability, dispatch, ingress, verification)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(binding, expected) {
		return errors.New("local verification binding does not match Core observations")
	}
	return nil
}

func DigestVerificationBinding(binding LocalVerificationBindingV2) (string, error) {
	raw, err := json.Marshal(binding)
	if err != nil {
		return "", err
	}
	return canonical.DigestJSON(raw)
}

func BuildReviewBinding(attemptID string, round uint, verificationBinding LocalVerificationBindingV2, review LocalSelfIdentityObservationV2) (LocalReviewBindingV2, error) {
	if round == 0 || attemptID == "" || verificationBinding.AttemptID != attemptID {
		return LocalReviewBindingV2{}, errors.New("local review binding identity is invalid")
	}
	if err := ValidateObservation(review); err != nil {
		return LocalReviewBindingV2{}, err
	}
	if review.SelfProfile != verificationBinding.SelfProfile || review.ActivationDigest != verificationBinding.ActivationDigest || review.IdentitySubjectDigest != verificationBinding.IdentitySubjectDigest {
		return LocalReviewBindingV2{}, errors.New("local review observation crossed verification identity")
	}
	verificationDigest, err := DigestVerificationBinding(verificationBinding)
	if err != nil {
		return LocalReviewBindingV2{}, err
	}
	return LocalReviewBindingV2{
		SchemaVersion: ReviewBindingSchema, SelfProfile: LocalProfile,
		ActivationDigest: review.ActivationDigest, IdentitySubjectDigest: review.IdentitySubjectDigest,
		AttemptID: attemptID, ReviewRound: round, VerificationBindingDigest: verificationDigest,
		VerificationObservationDigest: verificationBinding.VerificationObservationDigest,
		ReviewObservationDigest:       review.ObservationDigest,
		Applicability:                 verificationBinding.Applicability,
	}, nil
}

// ValidateReviewBindingProjection is the final consumer check for a review
// binding. It recomputes the verification-binding digest and rejects a paired
// but forged attempt, round, profile or applicability projection.
func ValidateReviewBindingProjection(binding LocalReviewBindingV2, attemptID string, round uint, verificationBinding LocalVerificationBindingV2) error {
	verificationDigest, err := DigestVerificationBinding(verificationBinding)
	if err != nil {
		return err
	}
	if binding.SchemaVersion != ReviewBindingSchema || binding.SelfProfile != LocalProfile ||
		binding.AttemptID != attemptID || binding.ReviewRound != round ||
		binding.ActivationDigest != verificationBinding.ActivationDigest ||
		binding.IdentitySubjectDigest != verificationBinding.IdentitySubjectDigest ||
		binding.VerificationBindingDigest != verificationDigest ||
		binding.VerificationObservationDigest != verificationBinding.VerificationObservationDigest ||
		SameApplicability(binding.Applicability, verificationBinding.Applicability) != nil {
		return errors.New("local review binding projection is invalid")
	}
	return nil
}

func DigestReviewBinding(binding LocalReviewBindingV2) (string, error) {
	raw, err := json.Marshal(binding)
	if err != nil {
		return "", err
	}
	return canonical.DigestJSON(raw)
}

// PersistPhaseObservation installs one immutable canonical observation. A
// replay reuses only an exact existing observation; a different record never
// replaces it.
func PersistPhaseObservation(directory, name string, observation LocalSelfIdentityObservationV2) (LocalSelfIdentityObservationV2, error) {
	return persistPhaseObservation(directory, name, observation, nil, nil)
}

// StableDirectory is implemented by the descriptor chain derived from a held
// Run lease. Phase evidence uses its descriptor and recheck, never a pathname.
type StableDirectory interface {
	File() *os.File
	Recheck() error
}

func PersistPhaseObservationIn(directory StableDirectory, name string, observation LocalSelfIdentityObservationV2) (LocalSelfIdentityObservationV2, error) {
	if directory == nil || directory.File() == nil || directory.Recheck() != nil {
		return LocalSelfIdentityObservationV2{}, errors.New("local phase observation lacks stable run authority")
	}
	return persistPhaseObservationAt(int(directory.File().Fd()), directory.Recheck, name, observation, nil)
}

func ReadPhaseObservationIn(directory StableDirectory, name string) (LocalSelfIdentityObservationV2, error) {
	if directory == nil || directory.File() == nil || directory.Recheck() != nil {
		return LocalSelfIdentityObservationV2{}, errors.New("local phase observation lacks stable run authority")
	}
	observation, err := readPhaseObservationAt(int(directory.File().Fd()), name, nil)
	if err != nil {
		return LocalSelfIdentityObservationV2{}, err
	}
	if err := directory.Recheck(); err != nil {
		return LocalSelfIdentityObservationV2{}, err
	}
	return observation, nil
}

func VersionedPhaseObservationName(phase, observationDigest string) (string, error) {
	value := strings.TrimPrefix(observationDigest, "sha256:")
	if filepath.Base(phase) != phase || phase == "" || len(value) != 64 {
		return "", errors.New("local versioned phase identity is invalid")
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return "", errors.New("local versioned phase digest is invalid")
		}
	}
	return fmt.Sprintf("local-self-identity-%s-%s.json", phase, value), nil
}

func PersistVersionedPhaseObservation(directory StableDirectory, phase string, observation LocalSelfIdentityObservationV2) (LocalSelfIdentityObservationV2, error) {
	name, err := VersionedPhaseObservationName(phase, observation.ObservationDigest)
	if err != nil {
		return LocalSelfIdentityObservationV2{}, err
	}
	return PersistPhaseObservationIn(directory, name, observation)
}

func ReadVersionedPhaseObservation(directory StableDirectory, phase, observationDigest string) (LocalSelfIdentityObservationV2, error) {
	name, err := VersionedPhaseObservationName(phase, observationDigest)
	if err != nil {
		return LocalSelfIdentityObservationV2{}, err
	}
	return ReadPhaseObservationIn(directory, name)
}

func persistPhaseObservation(directory, name string, observation LocalSelfIdentityObservationV2, afterParentOpen func(), beforeLink func() error) (LocalSelfIdentityObservationV2, error) {
	if err := ValidateObservation(observation); err != nil {
		return LocalSelfIdentityObservationV2{}, err
	}
	if filepath.Base(name) != name || name == "." || name == "" {
		return LocalSelfIdentityObservationV2{}, errors.New("local phase observation name is invalid")
	}
	raw, err := json.Marshal(observation)
	if err != nil {
		return LocalSelfIdentityObservationV2{}, err
	}
	raw, err = canonical.JSON(raw)
	if err != nil || len(raw) > phaseObservationByteLimit {
		return LocalSelfIdentityObservationV2{}, errors.New("local phase observation is not bounded canonical JSON")
	}
	directoryFD, directoryIdentity, err := openStablePhaseDirectory(directory)
	if err != nil {
		return LocalSelfIdentityObservationV2{}, err
	}
	defer unix.Close(directoryFD)
	if afterParentOpen != nil {
		afterParentOpen()
	}
	return persistPhaseObservationAt(directoryFD, func() error {
		return recheckStablePhaseDirectory(directory, directoryFD, directoryIdentity)
	}, name, observation, beforeLink)
}

func persistPhaseObservationAt(directoryFD int, recheck func() error, name string, observation LocalSelfIdentityObservationV2, beforeLink func() error) (LocalSelfIdentityObservationV2, error) {
	if err := ValidateObservation(observation); err != nil {
		return LocalSelfIdentityObservationV2{}, err
	}
	if filepath.Base(name) != name || name == "." || name == "" {
		return LocalSelfIdentityObservationV2{}, errors.New("local phase observation name is invalid")
	}
	raw, err := json.Marshal(observation)
	if err != nil {
		return LocalSelfIdentityObservationV2{}, err
	}
	raw, err = canonical.JSON(raw)
	if err != nil || len(raw) > phaseObservationByteLimit {
		return LocalSelfIdentityObservationV2{}, errors.New("local phase observation is not bounded canonical JSON")
	}
	if recheck != nil {
		if err := recheck(); err != nil {
			return LocalSelfIdentityObservationV2{}, err
		}
	}
	pending, err := phasePendingName(name)
	if err != nil {
		return LocalSelfIdentityObservationV2{}, err
	}
	fd, err := unix.Openat(directoryFD, pending, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o400)
	if err != nil {
		return LocalSelfIdentityObservationV2{}, err
	}
	linked := false
	defer func() {
		if !linked {
			_ = unix.Unlinkat(directoryFD, pending, 0)
		}
	}()
	file := os.NewFile(uintptr(fd), pending)
	_, writeErr := file.Write(raw)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if closeErr := file.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		return LocalSelfIdentityObservationV2{}, writeErr
	}
	if beforeLink != nil {
		if err := beforeLink(); err != nil {
			return LocalSelfIdentityObservationV2{}, err
		}
	}
	linkErr := unix.Linkat(directoryFD, pending, directoryFD, name, 0)
	if linkErr != nil && !errors.Is(linkErr, unix.EEXIST) {
		return LocalSelfIdentityObservationV2{}, linkErr
	}
	if err := unix.Unlinkat(directoryFD, pending, 0); err != nil {
		return LocalSelfIdentityObservationV2{}, err
	}
	linked = true
	if syncErr := unix.Fsync(directoryFD); syncErr != nil {
		return LocalSelfIdentityObservationV2{}, syncErr
	}
	if recheck != nil {
		if err := recheck(); err != nil {
			return LocalSelfIdentityObservationV2{}, err
		}
	}
	stored, err := readPhaseObservationAt(directoryFD, name, nil)
	if err != nil {
		return LocalSelfIdentityObservationV2{}, err
	}
	if !reflect.DeepEqual(stored, observation) {
		return LocalSelfIdentityObservationV2{}, errors.New("local phase observation replay does not match frozen bytes")
	}
	return stored, nil
}

func phasePendingName(name string) (string, error) {
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf(".%s.pending-%x", name, nonce[:]), nil
}

func ReadPhaseObservation(path string) (LocalSelfIdentityObservationV2, error) {
	directory := filepath.Dir(path)
	name := filepath.Base(path)
	directoryFD, directoryIdentity, err := openStablePhaseDirectory(directory)
	if err != nil {
		return LocalSelfIdentityObservationV2{}, err
	}
	defer unix.Close(directoryFD)
	observation, err := readPhaseObservationAt(directoryFD, name, nil)
	if err != nil {
		return LocalSelfIdentityObservationV2{}, err
	}
	if err := recheckStablePhaseDirectory(directory, directoryFD, directoryIdentity); err != nil {
		return LocalSelfIdentityObservationV2{}, err
	}
	return observation, nil
}

func readPhaseObservationAt(directoryFD int, name string, afterRead func()) (LocalSelfIdentityObservationV2, error) {
	fd, err := unix.Openat(directoryFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return LocalSelfIdentityObservationV2{}, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	var before, after, named unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil {
		return LocalSelfIdentityObservationV2{}, err
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG || before.Nlink != 1 || before.Size < 0 || before.Size > phaseObservationByteLimit {
		return LocalSelfIdentityObservationV2{}, errors.New("local phase observation is not a bounded regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, phaseObservationByteLimit+1))
	if err != nil || len(raw) > phaseObservationByteLimit {
		return LocalSelfIdentityObservationV2{}, errors.New("local phase observation exceeds its bound")
	}
	if afterRead != nil {
		afterRead()
	}
	if err := unix.Fstat(fd, &after); err != nil {
		return LocalSelfIdentityObservationV2{}, err
	}
	if err := unix.Fstatat(directoryFD, name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return LocalSelfIdentityObservationV2{}, err
	}
	if before.Dev != after.Dev || before.Ino != after.Ino || before.Size != after.Size || before.Mode != after.Mode || before.Nlink != after.Nlink ||
		before.Dev != named.Dev || before.Ino != named.Ino || before.Size != named.Size || before.Mode != named.Mode || named.Nlink != 1 {
		return LocalSelfIdentityObservationV2{}, errors.New("local phase observation changed while reading")
	}
	canonicalRaw, err := canonical.JSON(raw)
	if err != nil || !bytes.Equal(raw, canonicalRaw) {
		return LocalSelfIdentityObservationV2{}, errors.New("local phase observation is not exact canonical JSON")
	}
	observation, err := DecodeObservation(raw)
	if err != nil {
		return LocalSelfIdentityObservationV2{}, fmt.Errorf("decode local phase observation: %w", err)
	}
	return observation, nil
}

type phaseDirectoryIdentity struct {
	dev, ino uint64
	mode     uint32
}

func openStablePhaseDirectory(path string) (int, phaseDirectoryIdentity, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
	if err != nil {
		return -1, phaseDirectoryIdentity{}, err
	}
	var held, named unix.Stat_t
	if err := unix.Fstat(fd, &held); err != nil {
		unix.Close(fd)
		return -1, phaseDirectoryIdentity{}, err
	}
	if err := unix.Lstat(path, &named); err != nil || held.Mode&unix.S_IFMT != unix.S_IFDIR ||
		held.Dev != named.Dev || held.Ino != named.Ino || held.Mode != named.Mode {
		unix.Close(fd)
		return -1, phaseDirectoryIdentity{}, errors.New("local phase observation parent is not a stable directory")
	}
	return fd, phaseDirectoryIdentity{dev: uint64(held.Dev), ino: uint64(held.Ino), mode: uint32(held.Mode)}, nil
}

func recheckStablePhaseDirectory(path string, fd int, expected phaseDirectoryIdentity) error {
	var held, named unix.Stat_t
	if err := unix.Fstat(fd, &held); err != nil {
		return err
	}
	if err := unix.Lstat(path, &named); err != nil || uint64(held.Dev) != expected.dev || uint64(held.Ino) != expected.ino || uint32(held.Mode) != expected.mode ||
		held.Dev != named.Dev || held.Ino != named.Ino || held.Mode != named.Mode {
		return errors.New("local phase observation parent changed while in use")
	}
	return nil
}
