package processsupervisor

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type attachVerifierFunc func(context.Context, AttachAuthority, func() error) error

func (fn attachVerifierFunc) WithCurrentAttachOwner(ctx context.Context, authority AttachAuthority, callback func() error) error {
	return fn(ctx, authority, callback)
}

func validAttachAuthority() AttachAuthority {
	bootstrap := validBootstrap()
	socket := ControlSocketIdentity{Device: 5, Inode: 6, FileType: "socket", UID: bootstrap.Core.UID, GID: bootstrap.Core.GID, Mode: 0o140600, LinkCount: 1}
	files := SessionControlFiles{
		Nonce:   ControlFileIdentity{Device: 5, Inode: 7, FileType: "regular", UID: bootstrap.Core.UID, GID: bootstrap.Core.GID, Mode: 0o100600, LinkCount: 1},
		Journal: ControlFileIdentity{Device: 5, Inode: 8, FileType: "regular", UID: bootstrap.Core.UID, GID: bootstrap.Core.GID, Mode: 0o100600, LinkCount: 1},
	}
	anchor := HandshakeAnchor{
		SessionID: bootstrap.SessionID, SessionNonceDigest: digest("9"), Authority: bootstrap.Authority,
		OwnerEpoch: bootstrap.OwnerEpoch, CurrentAuthorityHead: bootstrap.CurrentAuthorityHead,
		CommandSequence: 2, CommandHead: digest("8"), JournalSequence: 5, JournalHead: digest("7"),
		UID: bootstrap.Core.UID, GID: bootstrap.Core.GID, FixedBinary: bootstrap.Core.Binary, ControlSocket: socket, ControlFiles: files,
	}
	acquisition := AttachOwnerAcquisition{
		AuthorityNamespaceID: bootstrap.Authority.AuthorityNamespaceID, RepositoryIdentityDigest: digest("6"), OwnerEpoch: 2,
		OwnerUID: bootstrap.Core.UID, OwnerGID: bootstrap.Core.GID, OwnerProcess: bootstrap.Core.Process, OwnerBinary: bootstrap.Core.Binary,
		ObserverIdentity: "darwin-current-owner-v1", ObservedAt: time.Unix(bootstrap.Core.Process.BirthSeconds+1, 0).UTC().Format(time.RFC3339Nano),
		PreviousFactDigest: digest("5"), FactDigest: digest("4"),
	}
	bound := AttachOwnerBoundFact{
		Authority: bootstrap.Authority, PreviousAttemptRevision: 10, PreviousAttemptHead: digest("3"), AttemptRevision: 11, AttemptHead: digest("2"),
		ControlOwnerAcquiredFactDigest: acquisition.FactDigest, OwnerEpoch: acquisition.OwnerEpoch, FactDigest: digest("1"),
	}
	return AttachAuthority{PreviousSupervisor: anchor, Supervisor: ProcessIdentity{PID: 200, BirthSeconds: 2, BirthMicroseconds: 3, SessionID: 199, ProcessGroupID: 199}, CurrentAcquisition: acquisition, CurrentOwnerBoundFact: bound, Child: ProcessIdentity{PID: 300, BirthSeconds: 3, BirthMicroseconds: 4, SessionID: 299, ProcessGroupID: 299}, ChildObservationDigest: digest("0")}
}

func TestAttachAuthorityValidates(t *testing.T) {
	if err := validAttachAuthority().validate(); err != nil {
		t.Fatalf("valid attach authority: %v", err)
	}
}

func TestAttachRequestBindsEveryAuthorityEdge(t *testing.T) {
	authority := validAttachAuthority()
	request := attachRequest{SchemaVersion: AttachSchema, ProtocolRevision: ProtocolRevision, SessionNonce: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", Core: CoreIdentity{UID: authority.CurrentAcquisition.OwnerUID, GID: authority.CurrentAcquisition.OwnerGID, Process: authority.CurrentAcquisition.OwnerProcess, Binary: authority.CurrentAcquisition.OwnerBinary}, ControlDirectoryIdentity: ControlDirectoryIdentity{CanonicalPath: "/private/control", Device: 20, Inode: 21, FileType: "directory", UID: 501, GID: 20, Mode: 0o040700, LinkCount: 2}, Authority: authority}
	request.RequestDigest, _ = request.detachedDigest()
	if err := request.validate(); err != nil {
		t.Fatalf("valid attach request: %v", err)
	}
	mutations := []struct {
		name   string
		mutate func(*attachRequest)
	}{
		{"peer-birth", func(value *attachRequest) { value.Core.Process.BirthMicroseconds++ }},
		{"binary-sha", func(value *attachRequest) { value.Core.Binary.RawSHA256 = digest("a") }},
		{"source-head", func(value *attachRequest) { value.Core.Binary.SourceHead = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" }},
		{"session", func(value *attachRequest) { value.Authority.PreviousSupervisor.SessionID = "other-session" }},
		{"supervisor", func(value *attachRequest) { value.Authority.Supervisor.PID++ }},
		{"child", func(value *attachRequest) { value.Authority.Child.PID++ }},
		{"child-observation", func(value *attachRequest) { value.Authority.ChildObservationDigest = digest("a") }},
		{"journal-head", func(value *attachRequest) { value.Authority.PreviousSupervisor.JournalHead = digest("a") }},
		{"journal-sequence", func(value *attachRequest) { value.Authority.PreviousSupervisor.JournalSequence++ }},
		{"command-head", func(value *attachRequest) { value.Authority.PreviousSupervisor.CommandHead = digest("a") }},
		{"previous-authority-head", func(value *attachRequest) { value.Authority.PreviousSupervisor.CurrentAuthorityHead = digest("b") }},
		{"owner-acquisition", func(value *attachRequest) { value.Authority.CurrentAcquisition.FactDigest = digest("a") }},
		{"owner-bound-fact", func(value *attachRequest) { value.Authority.CurrentOwnerBoundFact.FactDigest = digest("a") }},
		{"control-directory", func(value *attachRequest) { value.ControlDirectoryIdentity.Inode++ }},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			changed := request
			test.mutate(&changed)
			if changed.validate() == nil {
				t.Fatal("mutated attach request retained detached digest authority")
			}
		})
	}
}

func TestAttachOwnerVerifierMustBorrowExactlyOnce(t *testing.T) {
	authority := validAttachAuthority()
	want := errors.New("callback result")
	err := withAttachOwner(context.Background(), attachVerifierFunc(func(_ context.Context, got AttachAuthority, callback func() error) error {
		if got != authority {
			t.Fatal("Attach authority drift")
		}
		return callback()
	}), authority, func() error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("callback error = %v", err)
	}
	for _, verifier := range []AttachOwnerVerifier{
		attachVerifierFunc(func(context.Context, AttachAuthority, func() error) error { return nil }),
		attachVerifierFunc(func(_ context.Context, _ AttachAuthority, callback func() error) error {
			_ = callback()
			return callback()
		}),
	} {
		if err := withAttachOwner(context.Background(), verifier, authority, func() error { return nil }); err == nil {
			t.Fatal("invalid owner verifier admitted Attach")
		}
	}
	release := make(chan struct{})
	asyncResult := make(chan error, 1)
	async := attachVerifierFunc(func(_ context.Context, _ AttachAuthority, callback func() error) error {
		go func() {
			<-release
			asyncResult <- callback()
		}()
		return nil
	})
	if err := withAttachOwner(context.Background(), async, authority, func() error { return nil }); err == nil {
		t.Fatal("asynchronous owner verifier admitted Attach")
	}
	close(release)
	if err := <-asyncResult; err == nil {
		t.Fatal("owner callback remained usable after verifier return")
	}
}

func TestAttachedSessionIsSingleUseAndCallbackScoped(t *testing.T) {
	observation := AttachObservation{SchemaVersion: AttachObservationSchema}
	var saved *AttachedSession
	if err := callAttachedBorrower(newAttachedSession(observation), func(session *AttachedSession) error {
		saved = session
		got, err := session.Observation()
		if err != nil || got != observation {
			t.Fatalf("Observation = %+v / %v", got, err)
		}
		return nil
	}); err != nil {
		t.Fatalf("valid borrowed Attach = %v", err)
	}
	if _, err := saved.Observation(); err == nil {
		t.Fatal("saved AttachedSession remained usable after callback")
	}

	if err := callAttachedBorrower(newAttachedSession(observation), func(*AttachedSession) error { return nil }); err == nil {
		t.Fatal("borrower that never consumed Observation succeeded")
	}
	if err := callAttachedBorrower(newAttachedSession(observation), func(session *AttachedSession) error {
		if _, err := session.Observation(); err != nil {
			return err
		}
		_, _ = session.Observation()
		return nil
	}); err == nil {
		t.Fatal("multiple AttachedSession uses succeeded")
	}

	release := make(chan struct{})
	asyncResult := make(chan error, 1)
	if err := callAttachedBorrower(newAttachedSession(observation), func(session *AttachedSession) error {
		go func() {
			<-release
			_, err := session.Observation()
			asyncResult <- err
		}()
		return nil
	}); err == nil {
		t.Fatal("asynchronous AttachedSession borrower succeeded")
	}
	close(release)
	if err := <-asyncResult; err == nil {
		t.Fatal("asynchronous AttachedSession use remained active")
	}
}

func TestAttachedSessionExposesOnlyReadOnlyObservation(t *testing.T) {
	typeOf := reflect.TypeOf((*AttachedSession)(nil))
	if typeOf.NumMethod() != 5 || typeOf.Method(0).Name != "ExecutePreparedBindAuthority" || typeOf.Method(1).Name != "ExecutePreparedClose" || typeOf.Method(2).Name != "ExecutePreparedCollect" || typeOf.Method(3).Name != "ExecutePreparedInspect" || typeOf.Method(4).Name != "Observation" {
		t.Fatalf("AttachedSession exported methods = %v", typeOf.NumMethod())
	}
}

// TestAttachedSessionRejectsGoroutineObservationDeterministically closes the
// narrow window left by the in-flight escape detector: a borrower that starts a
// goroutine which calls Observation and completes before the callback returns
// must still fail closed. The borrower goroutine identity is the proof, so the
// spawned goroutine is rejected even when it finishes first; synchronization is
// a channel, never a sleep.
func TestAttachedSessionRejectsGoroutineObservationDeterministically(t *testing.T) {
	observation := AttachObservation{SchemaVersion: AttachObservationSchema}
	goroutineDone := make(chan struct{})
	var goroutineErr error
	callbackErr := callAttachedBorrower(newAttachedSession(observation), func(session *AttachedSession) error {
		go func() {
			_, goroutineErr = session.Observation()
			close(goroutineDone)
		}()
		<-goroutineDone
		return nil
	})
	if !errors.Is(callbackErr, ErrConflict) {
		t.Fatalf("borrower that started a goroutine must fail closed: %v", callbackErr)
	}
	if !errors.Is(goroutineErr, ErrConflict) {
		t.Fatalf("cross-goroutine Observation must be rejected: %v", goroutineErr)
	}
}
