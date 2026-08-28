//go:build darwin

package processcontrol

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/launchidentity"
	"github.com/chiga0/marshal-harness/internal/sandboxlaunch"
	"golang.org/x/sys/unix"
)

func TestLaunchRequiresFreshAuthorizationBeforeStarting(t *testing.T) {
	authority := &fakeAuthority{launch: AppendResult{Appended: false, Revision: 2, HeadDigest: testDigest('a'), TransitionDigest: testDigest('b')}}
	system := &fakeDarwinSystem{}
	coordinator := mustFakeCoordinator(t, authority, system)
	if _, err := coordinator.launch(context.Background(), validLaunchRequest()); !errors.Is(err, ErrLaunchUncertain) {
		t.Fatalf("launch error = %v", err)
	}
	if system.starts != 0 {
		t.Fatalf("start count = %d", system.starts)
	}
}

func TestLaunchReleasesOnlyAfterFreshProcessStarted(t *testing.T) {
	unit := &fakeDarwinUnit{observed: validObservation(), inspectStates: []ProcessState{ProcessAbsent}}
	authority := freshAuthority()
	system := &fakeDarwinSystem{unit: unit}
	coordinator := mustFakeCoordinator(t, authority, system)
	process, err := coordinator.launch(context.Background(), validLaunchRequest())
	if err != nil {
		t.Fatal(err)
	}
	if process == nil || !unit.ready || !unit.released || unit.aborted {
		t.Fatalf("unit lifecycle = ready:%v released:%v aborted:%v", unit.ready, unit.released, unit.aborted)
	}
	if got, want := authority.calls, []string{"launch", "started"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("authority calls = %v, want %v", got, want)
	}
	if authority.startedRequest.CommandID != "command-1" || authority.startedRequest.ObservedAt == "" || authority.startedRequest.Observation.ObservationDigest == "" {
		t.Fatalf("process-started request = %+v", authority.startedRequest)
	}
}

func TestLaunchClosesGateWhenProcessStartedIsReplay(t *testing.T) {
	unit := &fakeDarwinUnit{observed: validObservation()}
	authority := freshAuthority()
	authority.started.Appended = false
	coordinator := mustFakeCoordinator(t, authority, &fakeDarwinSystem{unit: unit})
	process, err := coordinator.launch(context.Background(), validLaunchRequest())
	if !errors.Is(err, ErrAuthority) {
		t.Fatalf("launch error = %v", err)
	}
	if process == nil || !unit.ready || unit.released || unit.aborted {
		t.Fatalf("unit lifecycle = ready:%v released:%v aborted:%v", unit.ready, unit.released, unit.aborted)
	}
}

func TestEveryPostAuthorizationFailureIsLaunchUncertain(t *testing.T) {
	failure := errors.New("injected failure")
	tests := []struct {
		name  string
		setup func(*fakeAuthority, *fakeDarwinSystem, *fakeDarwinUnit)
	}{
		{name: "authorization response lost", setup: func(authority *fakeAuthority, _ *fakeDarwinSystem, _ *fakeDarwinUnit) { authority.launchErr = failure }},
		{name: "malformed launch revision", setup: func(authority *fakeAuthority, _ *fakeDarwinSystem, _ *fakeDarwinUnit) { authority.launch.Revision++ }},
		{name: "malformed launch digest", setup: func(authority *fakeAuthority, _ *fakeDarwinSystem, _ *fakeDarwinUnit) {
			authority.launch.TransitionDigest = "not-a-digest"
		}},
		{name: "process start", setup: func(_ *fakeAuthority, system *fakeDarwinSystem, _ *fakeDarwinUnit) { system.startErr = failure }},
		{name: "ready handshake", setup: func(_ *fakeAuthority, _ *fakeDarwinSystem, unit *fakeDarwinUnit) { unit.readyErr = failure }},
		{name: "observation before birth", setup: func(_ *fakeAuthority, _ *fakeDarwinSystem, unit *fakeDarwinUnit) {
			unit.observedTime = time.Unix(unit.observed.BirthSeconds, (unit.observed.BirthMicroseconds-1)*int64(time.Microsecond)).UTC()
		}},
		{name: "process-started response lost", setup: func(authority *fakeAuthority, _ *fakeDarwinSystem, _ *fakeDarwinUnit) { authority.startedErr = failure }},
		{name: "malformed process-started revision", setup: func(authority *fakeAuthority, _ *fakeDarwinSystem, _ *fakeDarwinUnit) { authority.started.Revision++ }},
		{name: "release handshake", setup: func(_ *fakeAuthority, _ *fakeDarwinSystem, unit *fakeDarwinUnit) { unit.releaseErr = failure }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			unit := &fakeDarwinUnit{observed: validObservation()}
			authority := freshAuthority()
			system := &fakeDarwinSystem{unit: unit}
			test.setup(authority, system, unit)
			if _, err := mustFakeCoordinator(t, authority, system).launch(context.Background(), validLaunchRequest()); !errors.Is(err, ErrLaunchUncertain) {
				t.Fatalf("launch error = %v", err)
			}
			if unit.released {
				t.Fatal("failed launch released workload")
			}
			starts := system.starts
			// RB1 exact replay returns Appended=false. Retrying the same Attempt
			// must therefore remain uncertain without another spawn.
			authority.launchErr = nil
			authority.launch.Appended = false
			if _, err := mustFakeCoordinator(t, authority, system).launch(context.Background(), validLaunchRequest()); !errors.Is(err, ErrLaunchUncertain) {
				t.Fatalf("replay error = %v", err)
			}
			if system.starts != starts {
				t.Fatalf("same Attempt respawned: %d -> %d", starts, system.starts)
			}
		})
	}
}

func TestMaterialsRemainFailClosedBeforeAuthority(t *testing.T) {
	authority := freshAuthority()
	system := &fakeDarwinSystem{}
	request := validLaunchRequest()
	request.Materials = []LaunchMaterial{{Role: "provider-bundle", CanonicalPath: "/fixed/provider/bundle.js", ExpectedSHA256: testDigest('f')}}
	if _, err := mustFakeCoordinator(t, authority, system).launch(context.Background(), request); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("materials launch error = %v", err)
	}
	if len(authority.calls) != 0 || system.starts != 0 {
		t.Fatalf("materials crossed authority boundary: calls=%v starts=%d", authority.calls, system.starts)
	}
}

func TestAuthorityRefRejectsNonRB1GenerationAndDigest(t *testing.T) {
	for name, mutate := range map[string]func(*AuthorityRef){
		"generation above int64": func(ref *AuthorityRef) { ref.DispatchGeneration = 1 << 63 },
		"lease digest":           func(ref *AuthorityRef) { ref.LeaseDigest = "not-a-digest" },
		"fencing digest":         func(ref *AuthorityRef) { ref.FencingTokenDigest = "not-a-digest" },
		"run authority":          func(ref *AuthorityRef) { ref.RunAuthorityDigest = "not-a-digest" },
		"orchestrator":           func(ref *AuthorityRef) { ref.OrchestratorID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			ref := validAuthorityRef()
			mutate(&ref)
			if err := ref.validate(); !errors.Is(err, ErrAuthority) {
				t.Fatalf("authority validation error = %v", err)
			}
		})
	}
}

func TestTerminateReauthorizesTermThenKillUnderCleanupBinding(t *testing.T) {
	unit := &fakeDarwinUnit{
		observed:      validObservation(),
		inspectStates: []ProcessState{ProcessLive, ProcessLive, ProcessAbsent, ProcessAbsent},
	}
	authority := freshAuthority()
	process := &darwinProcess{authority: authority, ref: validAuthorityRef(), unit: unit, observed: unit.observed}
	inspection, err := process.terminate(context.Background(), validCleanupRef(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.State != ProcessAbsent {
		t.Fatalf("state = %s", inspection.State)
	}
	if got, want := unit.signals, []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL}; !reflect.DeepEqual(got, want) {
		t.Fatalf("signals = %v, want %v", got, want)
	}
	if got, want := authority.operations, []ControlOperation{
		OperationCleanupInspect, OperationSignalTERM, OperationCleanupInspect, OperationSignalKILL, OperationCleanupInspect,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("operations = %v, want %v", got, want)
	}
	for index, cleanup := range authority.cleanups {
		if cleanup != validCleanupRef() {
			t.Fatalf("cleanup[%d] = %+v", index, cleanup)
		}
	}
}

func TestLaunchFDTableBindsRuntimeExactlyOnce(t *testing.T) {
	newFile := func(name string) *os.File {
		t.Helper()
		file, err := os.CreateTemp(t.TempDir(), name)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = file.Close() })
		return file
	}
	spec, ready, release := newFile("spec"), newFile("ready"), newFile("release")
	cwd, runtime, marshal := newFile("cwd"), newFile("runtime"), newFile("marshal")
	root, material := newFile("root"), newFile("material")
	held := &launchidentity.HeldClosure{Runtime: runtime, Roots: []*os.File{root}, Materials: []*os.File{material}}
	table, err := launchFDTable(spec, ready, release, cwd, marshal, held)
	if err != nil {
		t.Fatal(err)
	}
	if table[sandboxlaunch.ExecutableFD-3] != runtime {
		t.Fatalf("ExecutableFD points to %v, want the held runtime", table[sandboxlaunch.ExecutableFD-3])
	}
	count := 0
	for _, file := range table {
		if file == runtime {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("runtime FD cardinality = %d", count)
	}
	held.Materials = append(held.Materials, runtime)
	if _, err := launchFDTable(spec, ready, release, cwd, marshal, held); !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("duplicate runtime role error = %v", err)
	}
}

func TestWaitDoesNotBlockConcurrentTerminate(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	unit := &fakeDarwinUnit{
		observed: validObservation(), inspectEntered: entered, inspectRelease: release,
		inspectStates: []ProcessState{ProcessLive, ProcessLive, ProcessAbsent, ProcessAbsent, ProcessAbsent, ProcessAbsent},
	}
	authority := freshAuthority()
	process := &darwinProcess{authority: authority, ref: validAuthorityRef(), unit: unit, observed: unit.observed}
	waitDone := make(chan error, 1)
	go func() { _, err := process.wait(context.Background()); waitDone <- err }()
	<-entered
	close(release)
	terminateDone := make(chan error, 1)
	go func() { _, err := process.terminate(context.Background(), validCleanupRef(), 0); terminateDone <- err }()
	select {
	case err := <-terminateDone:
		if err != nil {
			t.Fatalf("terminate error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Wait blocked concurrent Terminate")
	}
	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("wait error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Wait did not observe terminal state")
	}
}

func TestVnodeOrABATaintPreventsAnySignal(t *testing.T) {
	unit := &fakeDarwinUnit{observed: validObservation(), inspectStates: []ProcessState{ProcessIdentityConflict}}
	authority := freshAuthority()
	process := &darwinProcess{authority: authority, ref: validAuthorityRef(), unit: unit, observed: unit.observed}
	inspection, err := process.terminate(context.Background(), validCleanupRef(), time.Second)
	if !errors.Is(err, ErrIdentityConflict) || inspection.State != ProcessIdentityConflict {
		t.Fatalf("terminate = %+v, %v", inspection, err)
	}
	if len(unit.signals) != 0 {
		t.Fatalf("signals after identity conflict = %v", unit.signals)
	}
}

func TestPIDOrBirthMismatchIsIdentityConflict(t *testing.T) {
	observation := validObservation()
	info := &unix.KinfoProc{}
	info.Proc.P_pid = int32(observation.PID)
	info.Eproc.Pgid = int32(observation.PGID)
	info.Proc.P_starttime.Sec = observation.BirthSeconds + 1
	info.Proc.P_starttime.Usec = int32(observation.BirthMicroseconds)
	state, err := classifyProcess(observation, info, testSID, testSID, nil, nil)
	if !errors.Is(err, ErrIdentityConflict) || state != ProcessIdentityConflict {
		t.Fatalf("mismatched birth state = %s, %v", state, err)
	}
}

func TestLeaderESRCHStillProbesButRefusesUnanchoredGroup(t *testing.T) {
	observation := validObservation()
	state, err := classifyProcess(observation, nil, testSID, 0, unix.ESRCH, nil)
	if !errors.Is(err, ErrIdentityConflict) || state != ProcessIdentityConflict {
		t.Fatalf("unanchored group state = %s, %v", state, err)
	}
	state, err = classifyProcess(observation, nil, testSID, 0, unix.ESRCH, unix.ESRCH)
	if err != nil || state != ProcessAbsent {
		t.Fatalf("absent group state = %s, %v", state, err)
	}
}

func TestCWDContentChangesRemainControllableButIdentityDriftDoesNot(t *testing.T) {
	expected := validCWDObservation()
	contentChanged := expected
	contentChanged.Size++
	contentChanged.Nlink++
	if !sameObservedObject(contentChanged, expected, false) {
		t.Fatal("cwd content change was treated as identity drift")
	}
	if sameObservedObject(contentChanged, expected, true) {
		t.Fatal("content-sensitive object ignored content change")
	}
	if vnodeFlags(false)&(unix.NOTE_WRITE|unix.NOTE_EXTEND|unix.NOTE_LINK) != 0 {
		t.Fatal("cwd guard subscribes to content events")
	}
	if vnodeFlags(true)&(unix.NOTE_WRITE|unix.NOTE_EXTEND|unix.NOTE_LINK) == 0 {
		t.Fatal("executable guard omitted content events")
	}
	for name, mutate := range map[string]func(*ObjectObservation){
		"rename or swap": func(observation *ObjectObservation) { observation.Inode++ },
		"mode drift":     func(observation *ObjectObservation) { observation.Mode ^= 0o100 },
		"owner drift":    func(observation *ObjectObservation) { observation.UID++ },
	} {
		t.Run(name, func(t *testing.T) {
			actual := expected
			mutate(&actual)
			if sameObservedObject(actual, expected, false) {
				t.Fatal("cwd identity drift was accepted")
			}
		})
	}
}

func TestLeaderReuseAndSessionEscapeAreIdentityConflicts(t *testing.T) {
	observation := validObservation()
	leader := unix.KinfoProc{}
	leader.Proc.P_pid = int32(observation.PID)
	leader.Eproc.Pgid = int32(observation.PGID)
	leader.Proc.P_starttime.Sec = observation.BirthSeconds
	leader.Proc.P_starttime.Usec = int32(observation.BirthMicroseconds)
	leader.Proc.P_stat = darwinProcessZombie
	descendant := unix.KinfoProc{}
	descendant.Proc.P_pid = int32(observation.PID + 1)
	descendant.Eproc.Pgid = int32(observation.PGID)
	sameSession := func(int) (int, error) { return testSID, nil }

	state, err := classifyGroupMembers(observation, []unix.KinfoProc{leader, descendant}, testSID, true, sameSession)
	if err != nil || state != ProcessLive {
		t.Fatalf("anchored descendants = %s, %v", state, err)
	}
	reusedLeader := leader
	reusedLeader.Proc.P_starttime.Sec++
	state, err = classifyGroupMembers(observation, []unix.KinfoProc{reusedLeader}, testSID, false, sameSession)
	if !errors.Is(err, ErrIdentityConflict) || state != ProcessIdentityConflict {
		t.Fatalf("reused leader = %s, %v", state, err)
	}
	state, err = classifyGroupMembers(observation, []unix.KinfoProc{descendant}, testSID, false, sameSession)
	if !errors.Is(err, ErrIdentityConflict) || state != ProcessIdentityConflict {
		t.Fatalf("unanchored group = %s, %v", state, err)
	}
	escapedSession := func(pid int) (int, error) {
		if pid == int(descendant.Proc.P_pid) {
			return testSID + 1, nil
		}
		return testSID, nil
	}
	state, err = classifyGroupMembers(observation, []unix.KinfoProc{leader, descendant}, testSID, true, escapedSession)
	if !errors.Is(err, ErrIdentityConflict) || state != ProcessIdentityConflict {
		t.Fatalf("session escape = %s, %v", state, err)
	}
}

func TestSamePIDReappearanceAfterExitIsConflict(t *testing.T) {
	observation := validObservation()
	reused := &unix.KinfoProc{}
	reused.Proc.P_pid = int32(observation.PID)
	reused.Eproc.Pgid = int32(observation.PGID + 1)
	reused.Proc.P_starttime.Sec = observation.BirthSeconds + 1
	reused.Proc.P_starttime.Usec = int32(observation.BirthMicroseconds)
	anchored, err := classifyExitedLeader(observation, reused, testSID, testSID, nil)
	if anchored || !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("reused PID anchor = %v, %v", anchored, err)
	}
}

func TestPreWorkloadWaitTimeoutRetainsOwnershipUntilExit(t *testing.T) {
	done := make(chan struct{})
	timeout := make(chan time.Time, 1)
	timeout <- time.Now()
	cleaned := make(chan struct{})
	if boundedOwnedWait(done, timeout, func() { close(cleaned) }) {
		t.Fatal("blocked helper was reported exited")
	}
	select {
	case <-cleaned:
		t.Fatal("ownership was released before helper exit")
	default:
	}
	close(done)
	select {
	case <-cleaned:
	case <-time.After(time.Second):
		t.Fatal("owned wait did not clean up after helper exit")
	}
}

func TestLeaderSessionDriftFailsClosed(t *testing.T) {
	observation := validObservation()
	info := &unix.KinfoProc{}
	info.Proc.P_pid = int32(observation.PID)
	info.Eproc.Pgid = int32(observation.PGID)
	info.Proc.P_starttime.Sec = observation.BirthSeconds
	info.Proc.P_starttime.Usec = int32(observation.BirthMicroseconds)
	state, err := classifyProcess(observation, info, testSID, testSID+1, nil, nil)
	if !errors.Is(err, ErrIdentityConflict) || state != ProcessIdentityConflict {
		t.Fatalf("session drift = %s, %v", state, err)
	}
}

func TestForkEventAllowsCooperativeChildAndExitRemainsTerminal(t *testing.T) {
	exited, err := classifyProcessEvent(unix.NOTE_FORK)
	if exited || err != nil {
		t.Fatalf("fork event = exited:%v err:%v", exited, err)
	}
	exited, err = classifyProcessEvent(unix.NOTE_EXIT)
	if err != nil || !exited {
		t.Fatalf("exit event = exited:%v err:%v", exited, err)
	}
}

func TestTrackedDescendantsAllowSameGroupButRejectVisibleEscape(t *testing.T) {
	observation := validObservation()
	leader := processFixture(observation.PID, 1, observation.PGID, observation.BirthSeconds, observation.BirthMicroseconds)
	child := processFixture(observation.PID+1, observation.PID, observation.PGID, observation.BirthSeconds+1, observation.BirthMicroseconds)
	sameSession := func(int) (int, error) { return testSID, nil }

	tracked, err := reconcileDescendantObservations(observation, testSID, []unix.KinfoProc{leader, child}, nil, sameSession)
	if err != nil || len(tracked) != 1 {
		t.Fatalf("cooperative child = tracked:%v err:%v", tracked, err)
	}
	if tracked[observation.PID+1].ParentPID != observation.PID {
		t.Fatalf("tracked child = %+v", tracked[observation.PID+1])
	}

	for name, test := range map[string]struct {
		mutate  func(*unix.KinfoProc)
		session func(int) (int, error)
	}{
		"new session": {
			mutate: func(*unix.KinfoProc) {},
			session: func(pid int) (int, error) {
				if pid == observation.PID+1 {
					return testSID + 1, nil
				}
				return testSID, nil
			},
		},
		"left group": {mutate: func(process *unix.KinfoProc) { process.Eproc.Pgid++ }, session: sameSession},
		"reparent":   {mutate: func(process *unix.KinfoProc) { process.Eproc.Ppid = 1 }, session: sameSession},
		"pid reuse":  {mutate: func(process *unix.KinfoProc) { process.Proc.P_starttime.Usec++ }, session: sameSession},
	} {
		t.Run(name, func(t *testing.T) {
			drifted := child
			test.mutate(&drifted)
			if _, err := reconcileDescendantObservations(observation, testSID, []unix.KinfoProc{leader, drifted}, tracked, test.session); !errors.Is(err, ErrIdentityConflict) {
				t.Fatalf("visible descendant escape error = %v", err)
			}
		})
	}
}

func processFixture(pid, ppid, pgid int, seconds, microseconds int64) unix.KinfoProc {
	process := unix.KinfoProc{}
	process.Proc.P_pid = int32(pid)
	process.Eproc.Ppid = int32(ppid)
	process.Eproc.Pgid = int32(pgid)
	process.Proc.P_starttime.Sec = seconds
	process.Proc.P_starttime.Usec = int32(microseconds)
	return process
}

func TestObservedAtRejectsEarlierMicrosecondInSameSecond(t *testing.T) {
	observation := validObservation()
	tooEarly := time.Unix(observation.BirthSeconds, (observation.BirthMicroseconds-1)*int64(time.Microsecond)).UTC()
	if err := validateObservedAt(observation, tooEarly); !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("same-second earlier observation error = %v", err)
	}
	exact := time.Unix(observation.BirthSeconds, observation.BirthMicroseconds*int64(time.Microsecond)).UTC()
	if err := validateObservedAt(observation, exact); err != nil {
		t.Fatalf("exact observation error = %v", err)
	}
}

func TestAuthorityCallbackIsSynchronousAndExactlyOnce(t *testing.T) {
	tests := []struct {
		name       string
		invoke     func(*func() error) func(func() error) error
		wantEffect int
		wantAuth   bool
	}{
		{name: "once", invoke: func(_ *func() error) func(func() error) error {
			return func(callback func() error) error { return callback() }
		}, wantEffect: 1},
		{name: "zero", invoke: func(_ *func() error) func(func() error) error {
			return func(func() error) error { return nil }
		}, wantAuth: true},
		{name: "double", invoke: func(_ *func() error) func(func() error) error {
			return func(callback func() error) error { _ = callback(); _ = callback(); return nil }
		}, wantEffect: 1, wantAuth: true},
		{name: "verifier error", invoke: func(_ *func() error) func(func() error) error {
			return func(callback func() error) error { _ = callback(); return errors.New("verifier denied") }
		}, wantEffect: 1, wantAuth: true},
		{name: "deferred", invoke: func(saved *func() error) func(func() error) error {
			return func(callback func() error) error { *saved = callback; return nil }
		}, wantAuth: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var saved func() error
			effectCount := 0
			err := guardedAuthorityEffect(OperationInspect, test.invoke(&saved), func() error {
				effectCount++
				return nil
			})
			if errors.Is(err, ErrAuthority) != test.wantAuth || effectCount != test.wantEffect {
				t.Fatalf("guard = effect:%d err:%v", effectCount, err)
			}
			if saved != nil {
				if err := saved(); !errors.Is(err, ErrAuthority) {
					t.Fatalf("deferred callback error = %v", err)
				}
				if effectCount != 0 {
					t.Fatalf("deferred effect count = %d", effectCount)
				}
			}
		})
	}
}

func TestAuthorityEffectErrorIsNotReclassifiedAsVerifierDenial(t *testing.T) {
	effectFailure := errors.New("effect failed")
	err := guardedAuthorityEffect(OperationInspect, func(callback func() error) error { return callback() }, func() error { return effectFailure })
	if !errors.Is(err, effectFailure) || errors.Is(err, ErrAuthority) {
		t.Fatalf("effect error = %v", err)
	}
}

func TestVerifierErrorTakesPriorityOverEffectFailure(t *testing.T) {
	effectFailure := errors.New("effect failed")
	verifierFailure := errors.New("verifier denied")
	err := guardedAuthorityEffect(OperationInspect, func(callback func() error) error {
		return errors.Join(callback(), verifierFailure)
	}, func() error { return effectFailure })
	if !errors.Is(err, ErrAuthority) || errors.Is(err, effectFailure) {
		t.Fatalf("combined verifier error = %v", err)
	}
}

func TestFrozenMarshalAndPersistedObjectsRejectDriftAndABA(t *testing.T) {
	marshal := ObjectObservation{Path: "/fixed/bin/marshal", Device: 1, Inode: 9, Mode: 0o100700, UID: 501, GID: 20, Size: 99, Nlink: 1, SHA256: testDigest('b')}
	for name, mutate := range map[string]func(*ObjectObservation){
		"path swap":   func(value *ObjectObservation) { value.Path = "/fixed/bin/other" },
		"inode ABA":   func(value *ObjectObservation) { value.Inode++ },
		"content":     func(value *ObjectObservation) { value.SHA256 = testDigest('c') },
		"mode":        func(value *ObjectObservation) { value.Mode ^= 0o100 },
		"owner":       func(value *ObjectObservation) { value.UID++ },
		"hardlink":    func(value *ObjectObservation) { value.Nlink++ },
		"size change": func(value *ObjectObservation) { value.Size++ },
	} {
		t.Run("marshal "+name, func(t *testing.T) {
			current := marshal
			mutate(&current)
			if sameObservedObject(current, marshal, true) {
				t.Fatal("fixed Marshal drift was accepted")
			}
		})
	}

	observation := validObservation()
	working := ObjectObservation{Path: observation.WorkingDirectory, Device: observation.WorkingDirectoryDevice, Inode: observation.WorkingDirectoryInode, Mode: observation.WorkingDirectoryMode, UID: observation.WorkingDirectoryOwner}
	if !samePersistedWorkingDirectory(working, observation) {
		t.Fatal("exact persisted cwd was rejected")
	}
	working.Inode++
	if samePersistedWorkingDirectory(working, observation) {
		t.Fatal("restart cwd ABA was accepted")
	}
	executable := ObjectObservation{Path: observation.ExecutablePath, Device: observation.ExecutableDevice, Inode: observation.ExecutableInode, Mode: observation.ExecutableMode, UID: observation.ExecutableOwner, GID: observation.ExecutableGroup, Size: observation.ExecutableSize, Nlink: observation.ExecutableLinkCount, SHA256: observation.ExecutableSHA256}
	if !samePersistedExecutable(executable, observation) {
		t.Fatal("exact persisted executable was rejected")
	}
	executable.Inode++
	if samePersistedExecutable(executable, observation) {
		t.Fatal("restart executable ABA was accepted")
	}
}

func TestRestartReconcileNeverReturnsKillCapableProcess(t *testing.T) {
	authority := freshAuthority()
	system := &fakeDarwinSystem{reconcileStates: []ProcessState{ProcessLive}}
	coordinator := mustFakeCoordinator(t, authority, system)
	inspection, err := coordinator.reconcile(context.Background(), validAuthorityRef(), validObservation(), CleanupRef{})
	if !errors.Is(err, ErrLaunchUncertain) || inspection.State != ProcessLaunchUncertain {
		t.Fatalf("live reconcile = %+v, %v", inspection, err)
	}
	if system.starts != 0 {
		t.Fatalf("restart spawned %d processes", system.starts)
	}

	authority.operations = nil
	system.reconcileStates = []ProcessState{ProcessAbsent, ProcessAbsent}
	inspection, err = coordinator.reconcile(context.Background(), validAuthorityRef(), validObservation(), CleanupRef{})
	if err != nil || inspection.State != ProcessAbsent {
		t.Fatalf("absent reconcile = %+v, %v", inspection, err)
	}
	if got, want := authority.operations, []ControlOperation{OperationReconcile}; !reflect.DeepEqual(got, want) {
		t.Fatalf("operations = %v, want %v", got, want)
	}

	authority.operations, authority.cleanups = nil, nil
	system.reconcileStates = []ProcessState{ProcessAbsent, ProcessAbsent}
	cleanup := validCleanupRef()
	inspection, err = coordinator.reconcile(context.Background(), validAuthorityRef(), validObservation(), cleanup)
	if err != nil || inspection.State != ProcessAbsent {
		t.Fatalf("cleanup reconcile = %+v, %v", inspection, err)
	}
	if got, want := authority.operations, []ControlOperation{OperationCleanupReconcile}; !reflect.DeepEqual(got, want) || len(authority.cleanups) != 1 || authority.cleanups[0] != cleanup {
		t.Fatalf("cleanup operations=%v refs=%+v", got, authority.cleanups)
	}
}

type fakeAuthority struct {
	mu             sync.Mutex
	launch         AppendResult
	started        AppendResult
	calls          []string
	operations     []ControlOperation
	cleanups       []CleanupRef
	startedRequest ProcessStartedAuthorityRequest
	launchErr      error
	startedErr     error
	controlErr     error
}

func freshAuthority() *fakeAuthority {
	return &fakeAuthority{
		launch:  AppendResult{Appended: true, Revision: 2, HeadDigest: testDigest('b'), TransitionDigest: testDigest('c')},
		started: AppendResult{Appended: true, Revision: 3, HeadDigest: testDigest('d'), TransitionDigest: testDigest('e')},
	}
}

func validCleanupRef() CleanupRef {
	return CleanupRef{TerminalizationID: "terminalization-1", TerminalGeneration: 1, CleanupBindingDigest: testDigest('f')}
}

func (authority *fakeAuthority) AuthorizeLaunch(context.Context, LaunchAuthorityRequest) (AppendResult, error) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.calls = append(authority.calls, "launch")
	return authority.launch, authority.launchErr
}

func (authority *fakeAuthority) RecordProcessStarted(_ context.Context, request ProcessStartedAuthorityRequest) (AppendResult, error) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.calls = append(authority.calls, "started")
	authority.startedRequest = request
	return authority.started, authority.startedErr
}

func (authority *fakeAuthority) WithCurrentAuthority(_ context.Context, request ControlAuthorization, effect func() error) error {
	authority.mu.Lock()
	authority.operations = append(authority.operations, request.Operation)
	authority.cleanups = append(authority.cleanups, request.Cleanup)
	authority.mu.Unlock()
	if authority.controlErr != nil {
		return authority.controlErr
	}
	return effect()
}

type fakeDarwinSystem struct {
	unit            darwinUnit
	starts          int
	startErr        error
	reconcileStates []ProcessState
}

func (*fakeDarwinSystem) validateFixed(path string) (ObjectObservation, error) {
	digest := "sha256:" + "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	return ObjectObservation{Path: path, Device: 1, Inode: 9, Mode: 0o100700, UID: 501, GID: 20, Size: 99, Nlink: 1, SHA256: digest}, nil
}

func (system *fakeDarwinSystem) start(context.Context, string, ObjectObservation, LaunchRequest) (darwinUnit, error) {
	system.starts++
	return system.unit, system.startErr
}

func (system *fakeDarwinSystem) reconcile(ProcessObservation) (ProcessState, error) {
	if len(system.reconcileStates) == 0 {
		return ProcessIdentityConflict, ErrIdentityConflict
	}
	state := system.reconcileStates[0]
	system.reconcileStates = system.reconcileStates[1:]
	return state, stateError(state)
}

type fakeDarwinUnit struct {
	mu             sync.Mutex
	observed       ProcessObservation
	observedTime   time.Time
	inspectStates  []ProcessState
	signals        []syscall.Signal
	ready          bool
	released       bool
	aborted        bool
	closed         bool
	inspectCalls   int
	inspectEntered chan struct{}
	inspectRelease <-chan struct{}
	readyErr       error
	releaseErr     error
}

func (unit *fakeDarwinUnit) awaitReady(context.Context) error {
	unit.ready = true
	return unit.readyErr
}
func (unit *fakeDarwinUnit) release() error {
	if unit.releaseErr == nil {
		unit.released = true
	}
	return unit.releaseErr
}
func (unit *fakeDarwinUnit) abort()                          { unit.aborted = true }
func (unit *fakeDarwinUnit) observation() ProcessObservation { return unit.observed }
func (unit *fakeDarwinUnit) observedAt() time.Time {
	if !unit.observedTime.IsZero() {
		return unit.observedTime
	}
	return time.Unix(unit.observed.BirthSeconds, unit.observed.BirthMicroseconds*int64(time.Microsecond)).UTC()
}
func (unit *fakeDarwinUnit) close() error                 { unit.closed = true; return nil }
func (unit *fakeDarwinUnit) result() (int, string, error) { return 0, "", nil }
func (unit *fakeDarwinUnit) inspect() (ProcessState, error) {
	unit.mu.Lock()
	unit.inspectCalls++
	first := unit.inspectCalls == 1
	entered, release := unit.inspectEntered, unit.inspectRelease
	unit.mu.Unlock()
	if first && entered != nil {
		close(entered)
		<-release
	}
	unit.mu.Lock()
	defer unit.mu.Unlock()
	if len(unit.inspectStates) == 0 {
		return ProcessIdentityConflict, ErrIdentityConflict
	}
	state := unit.inspectStates[0]
	unit.inspectStates = unit.inspectStates[1:]
	return state, stateError(state)
}
func (unit *fakeDarwinUnit) signal(signal syscall.Signal) (ProcessState, error) {
	unit.mu.Lock()
	defer unit.mu.Unlock()
	unit.signals = append(unit.signals, signal)
	return ProcessLive, nil
}

func mustFakeCoordinator(t *testing.T, authority AttemptAuthority, system darwinSystem) *darwinCoordinator {
	t.Helper()
	coordinator, err := newDarwinCoordinator(authority, "/fixed/bin/marshal", system)
	if err != nil {
		t.Fatal(err)
	}
	return coordinator
}

func validLaunchRequest() LaunchRequest {
	request := LaunchRequest{
		Authority: validAuthorityRef(), ExpectedRevision: 1, ExpectedHead: testDigest('a'), LaunchID: "launch-1",
		CommandID: "command-1", Arguments: []string{"/fixed/workload", "argument"}, Environment: []string{"LANG=C"}, WorkingDirectory: "/fixed/worktree",
		ExecutablePath: "/fixed/workload", ExpectedExecutableSHA256: testDigest('a'), Materials: []LaunchMaterial{},
	}
	input := launchidentity.SpecInput{RuntimeExecutable: launchidentity.ObjectV1{CanonicalPath: "/fixed/workload", Device: 1, Inode: 3, FileType: 0o100000, Mode: 0o100700, UID: 501, GID: 20, Size: 42, LinkCount: 1, RawSHA256: testDigest('a')}, ClosureProfileID: launchidentity.NativeProfile, MaterialRoots: []launchidentity.MaterialRootV1{}, LaunchMaterials: []launchidentity.LaunchMaterialV1{}, Arguments: request.Arguments, Environment: request.Environment, WorkingDirectory: request.WorkingDirectory}
	closure, err := launchidentity.Seal(input)
	if err != nil {
		panic(err)
	}
	request.Closure = closure
	return request
}

func validAuthorityRef() AuthorityRef {
	return AuthorityRef{
		AuthorityNamespaceID:  authority.AuthorityNamespaceId{TenantNamespace: "tenant-1", ControlPlaneId: "control-1", AuthorityScopeId: "scope-1"},
		AuthorityNamespaceRef: "namespace-ref-1",
		AttemptKey:            "attempt-key",
		TaskID:                "task-1",
		RunID:                 "run-1",
		AttemptID:             "attempt-1",
		AllocationID:          "allocation-1",
		LeaseID:               "lease-1",
		LeaseDigest:           testDigest('6'),
		DispatchGeneration:    1,
		FencingTokenDigest:    testDigest('7'),
		OrchestratorID:        "orchestrator-1",
		RunAuthorityDigest:    testDigest('8'),
	}
}

func validObservation() ProcessObservation {
	digest := "sha256:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	observation, err := (ProcessObservation{
		PID: 1234, PGID: 1234, BirthSeconds: 100, BirthMicroseconds: 200,
		WorkingDirectory: "/fixed/worktree", WorkingDirectoryDevice: 1, WorkingDirectoryInode: 2,
		WorkingDirectoryType: 0o040000, WorkingDirectoryOwner: 501, WorkingDirectoryMode: 0o040700,
		ExecutablePath: "/fixed/workload", ExecutableDevice: 1, ExecutableInode: 3, ExecutableSize: 42,
		ExecutableType: 0o100000, ExecutableOwner: 501, ExecutableGroup: 20, ExecutableMode: 0o100700, ExecutableLinkCount: 1,
		ExecutableSHA256: digest, ObserverIdentity: processObserver,
	}).sealed()
	if err != nil {
		panic(err)
	}
	return observation
}

func validCWDObservation() ObjectObservation {
	return ObjectObservation{Path: "/fixed/worktree", Device: 1, Inode: 2, Mode: 0o040700, UID: 501, GID: 20, Size: 64, Nlink: 2}
}

const testSID = 999

func testDigest(character byte) string {
	return "sha256:" + strings.Repeat(string(character), 64)
}
