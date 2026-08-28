package resultingress

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/allocationcontrol"
	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
)

type allocationProvisionVerifier struct {
	identity       AttemptIdentity
	allow          bool
	calls          *int
	before         func()
	providerDomain authority.SecurityDomainId
}

func (v allocationProvisionVerifier) WithCurrentAllocationProvision(_ context.Context, check AllocationAuthorityCheck, fn func(authority.SecurityDomainId) error) error {
	if v.calls != nil {
		*v.calls++
	}
	if !v.allow || check.Phase != EffectPhaseAllocationProvision || check.Identity != v.identity || check.CurrentRunAuthority != runAuthorityBindingFor(v.identity) || check.Now == "" || check.TerminalizationID != "" || check.TerminalGeneration != 0 || check.CleanupBindingDigest != "" || check.ProcessTerminalFactDigest != "" {
		return errors.New("provision authority rejected")
	}
	if v.before != nil {
		v.before()
	}
	providerDomain := v.providerDomain
	if providerDomain == (authority.SecurityDomainId{}) {
		providerDomain = allocationTestProviderDomain(v.identity)
	}
	return fn(providerDomain)
}

type allocationCleanupVerifier struct {
	identity AttemptIdentity
	allow    bool
	want     *AllocationAuthorityCheck
	calls    *int
}

type allocationPermissiveCleanupVerifier struct{}

func (allocationPermissiveCleanupVerifier) WithCurrentAllocationCleanup(_ context.Context, check AllocationAuthorityCheck, fn func(authority.SecurityDomainId) error) error {
	return fn(allocationTestProviderDomain(check.Identity))
}

func (v allocationCleanupVerifier) WithCurrentAllocationCleanup(_ context.Context, check AllocationAuthorityCheck, fn func(authority.SecurityDomainId) error) error {
	if v.calls != nil {
		*v.calls++
	}
	if !v.allow || check.Phase != EffectPhaseAllocationTerminate || check.Identity != v.identity || check.CurrentRunAuthority != runAuthorityBindingFor(v.identity) || check.Now != "" || check.TerminalizationID == "" || check.TerminalGeneration < 1 || check.CleanupBindingDigest == "" || check.ProcessTerminalFactDigest == "" {
		return errors.New("cleanup authority rejected")
	}
	if v.want != nil && check != *v.want {
		return errors.New("cleanup tuple drift")
	}
	return fn(allocationTestProviderDomain(v.identity))
}

func allocationTestProviderDomain(id AttemptIdentity) authority.SecurityDomainId {
	return authority.SecurityDomainId{
		TenantNamespace:   id.AuthorityNamespaceID.TenantNamespace,
		TrustDomainKind:   authority.TrustDomainKindExecution,
		IsolationDomainId: allocationLocalIsolationDomain,
	}
}

func allocationTestAuthority(t *testing.T, store *DurableStore, id AttemptIdentity, provision, cleanup bool) *AllocationAuthority {
	t.Helper()
	value, err := NewAllocationAuthority(store, allocationProvisionVerifier{identity: id, allow: provision}, allocationCleanupVerifier{identity: id, allow: cleanup})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestDeriveAllocationEffectIdentityDeterministicDomainSeparatedAndTupleExact(t *testing.T) {
	id := attemptTestIdentity()
	head := attemptTestDigest("allocation-identity-head")
	base, err := DeriveAllocationEffectIdentity(id, EffectPhaseAllocationProvision, "effect-1", head)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := DeriveAllocationEffectIdentity(id, EffectPhaseAllocationProvision, "effect-1", head)
	if err != nil || replay != base {
		t.Fatalf("nondeterministic identity replay=%#v err=%v", replay, err)
	}
	if base.CommandID == "" || base.IdempotencyKey == "" || base.MarkerNonceDigest == "" ||
		strings.TrimPrefix(base.CommandID, "allocation-command-") == strings.TrimPrefix(base.IdempotencyKey, "allocation-idempotency-") ||
		strings.TrimPrefix(base.CommandID, "allocation-command-") == strings.TrimPrefix(base.MarkerNonceDigest, "sha256:") ||
		strings.TrimPrefix(base.IdempotencyKey, "allocation-idempotency-") == strings.TrimPrefix(base.MarkerNonceDigest, "sha256:") {
		t.Fatalf("identity domains are not separated: %#v", base)
	}
	mutations := []struct {
		name     string
		identity AttemptIdentity
		phase    EffectPhase
		effect   string
		head     string
	}{
		{name: "authority-namespace", identity: func() AttemptIdentity {
			value := id
			value.AuthorityNamespaceID.TenantNamespace = "tenant-2"
			return value
		}(), phase: EffectPhaseAllocationProvision, effect: "effect-1", head: head},
		{name: "task", identity: func() AttemptIdentity { value := id; value.TaskID = "task-2"; return value }(), phase: EffectPhaseAllocationProvision, effect: "effect-1", head: head},
		{name: "run", identity: func() AttemptIdentity { value := id; value.RunID = "run-2"; return value }(), phase: EffectPhaseAllocationProvision, effect: "effect-1", head: head},
		{name: "attempt", identity: func() AttemptIdentity { value := id; value.AttemptID = "attempt-2"; return value }(), phase: EffectPhaseAllocationProvision, effect: "effect-1", head: head},
		{name: "allocation", identity: func() AttemptIdentity { value := id; value.AllocationID = "allocation-2"; return value }(), phase: EffectPhaseAllocationProvision, effect: "effect-1", head: head},
		{name: "lease", identity: func() AttemptIdentity { value := id; value.LeaseID = "lease-2"; return value }(), phase: EffectPhaseAllocationProvision, effect: "effect-1", head: head},
		{name: "generation", identity: func() AttemptIdentity { value := id; value.DispatchGeneration++; return value }(), phase: EffectPhaseAllocationProvision, effect: "effect-1", head: head},
		{name: "phase", identity: id, phase: EffectPhaseAllocationTerminate, effect: "effect-1", head: head},
		{name: "effect", identity: id, phase: EffectPhaseAllocationProvision, effect: "effect-2", head: head},
		{name: "head", identity: id, phase: EffectPhaseAllocationProvision, effect: "effect-1", head: attemptTestDigest("allocation-other-head")},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			derived, err := DeriveAllocationEffectIdentity(mutation.identity, mutation.phase, mutation.effect, mutation.head)
			if err != nil || derived == base || derived.CommandID == base.CommandID || derived.IdempotencyKey == base.IdempotencyKey || derived.MarkerNonceDigest == base.MarkerNonceDigest {
				t.Fatalf("tuple field did not bind all identities: %#v err=%v", derived, err)
			}
		})
	}
}

func TestAllocationAuthorityRejectsCallerSelectedIdentityAndPhaseBeforeAppend(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*authority.SideEffectIntent, *allocationcontrol.AllocationProvisionIntentV1)
	}{
		{name: "command", mutate: func(generic *authority.SideEffectIntent, typed *allocationcontrol.AllocationProvisionIntentV1) {
			typed.Binding.CommandID = "caller-selected-command"
			if err := typed.Seal(); err != nil {
				t.Fatal(err)
			}
			generic.CommandId, generic.RequestDigest = typed.Binding.CommandID, typed.RequestDigest
		}},
		{name: "idempotency", mutate: func(generic *authority.SideEffectIntent, typed *allocationcontrol.AllocationProvisionIntentV1) {
			typed.Binding.IdempotencyKey = "caller-selected-idempotency"
			if err := typed.Seal(); err != nil {
				t.Fatal(err)
			}
			generic.IdempotencyKey, generic.RequestDigest = typed.Binding.IdempotencyKey, typed.RequestDigest
		}},
		{name: "marker-nonce", mutate: func(generic *authority.SideEffectIntent, typed *allocationcontrol.AllocationProvisionIntentV1) {
			typed.MarkerNonceDigest = attemptTestDigest("caller-selected-marker")
			if err := typed.Seal(); err != nil {
				t.Fatal(err)
			}
			generic.TargetDigest, generic.RequestDigest = typed.MarkerNonceDigest, typed.RequestDigest
		}},
		{name: "phase", mutate: func(generic *authority.SideEffectIntent, _ *allocationcontrol.AllocationProvisionIntentV1) {
			generic.Operation = string(EffectPhaseAllocationTerminate)
			generic.DispositionClass = authority.DispositionClassSandboxTerminate
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			store, err := OpenResultIngressStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			id := attemptTestIdentity()
			opened, err := appendAuthorizedAttempt(t, store, 0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: id})
			if err != nil {
				t.Fatal(err)
			}
			generic, typed := allocationTestProvisionIntent(t, opened.State, "caller-selected-effect")
			test.mutate(&generic, &typed)
			before := allocationLedgerBytes(t, dir)
			authorityPort := allocationTestAuthority(t, store, id, true, false)
			if _, err := authorityPort.CompareAndAppendAllocationProvisionIntent(context.Background(), id, generic, typed); err == nil || !bytes.Equal(before, allocationLedgerBytes(t, dir)) {
				t.Fatalf("caller-selected identity mutated authority err=%v", err)
			}
		})
	}
}

func TestAllocationAuthorityRechecksDeadlineInsideHeldVerifier(t *testing.T) {
	clock := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	store, err := openResultIngressStoreWithClock(dir, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	id := attemptTestIdentity()
	opened, err := appendAuthorizedAttempt(t, store, 0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: id})
	if err != nil {
		t.Fatal(err)
	}
	generic, typed := allocationTestProvisionIntent(t, opened.State, "deadline-race-effect")
	generic.Deadline = "2026-08-29T00:00:01Z"
	authorityPort, err := NewAllocationAuthority(store, allocationProvisionVerifier{
		identity: id, allow: true, before: func() { clock = clock.Add(2 * time.Second) },
	}, allocationCleanupVerifier{identity: id})
	if err != nil {
		t.Fatal(err)
	}
	before := allocationLedgerBytes(t, dir)
	if _, err := authorityPort.CompareAndAppendAllocationProvisionIntent(context.Background(), id, generic, typed); !errors.Is(err, ErrEffectAuthorityExpired) || !bytes.Equal(before, allocationLedgerBytes(t, dir)) {
		t.Fatalf("deadline race mutated authority err=%v", err)
	}
}

func TestAllocationAuthorityProvisionExactReplayAfterDeadlineReopensRecoveryWithoutAppend(t *testing.T) {
	clock := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	store, err := openResultIngressStoreWithClock(dir, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	id := attemptTestIdentity()
	opened, err := appendAuthorizedAttempt(t, store, 0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: id})
	if err != nil {
		t.Fatal(err)
	}
	generic, typed := allocationTestProvisionIntent(t, opened.State, "provision-deadline-replay")
	generic.Deadline = "2026-08-29T00:00:01Z"
	authorityPort := allocationTestAuthority(t, store, id, true, false)
	first, err := authorityPort.CompareAndAppendAllocationProvisionIntent(context.Background(), id, generic, typed)
	if err != nil || !first.Appended {
		t.Fatalf("fresh append=%#v err=%v", first, err)
	}

	clock = clock.Add(2 * time.Second)
	before := allocationLedgerBytes(t, dir)
	reopened, err := openResultIngressStoreWithClock(dir, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	restartedAuthority := allocationTestAuthority(t, reopened, id, true, false)
	replay, err := restartedAuthority.CompareAndAppendAllocationProvisionIntent(context.Background(), id, generic, typed)
	if err != nil || replay.Appended || replay.EffectKey != first.EffectKey || replay.EffectFactDigest != first.EffectFactDigest || replay.AllocationDigest != first.AllocationDigest || !bytes.Equal(before, allocationLedgerBytes(t, dir)) {
		t.Fatalf("expired exact replay=%#v err=%v", replay, err)
	}
	recoveryEntered := false
	if err := restartedAuthority.WithCurrentAllocation(context.Background(), replay.EffectKey, func(session allocationcontrol.AuthoritySession) error {
		recoveryEntered = true
		snapshot, err := session.Snapshot()
		if err != nil || snapshot.Validate() != nil || snapshot.ProvisionIntentFactDigest != first.AllocationDigest {
			return ErrAllocationAuthorityConflict
		}
		return nil
	}); err != nil || !recoveryEntered {
		t.Fatalf("expired exact replay did not enter recovery: entered=%v err=%v", recoveryEntered, err)
	}
}

func TestAllocationAuthorityTerminateExactReplayAfterDeadlineReopensRecoveryWithoutAppend(t *testing.T) {
	clock := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	store, err := openResultIngressStoreWithClock(dir, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	provisioned, _, provision, _ := allocationCompleteProvision(t, store)
	terminal := allocationAdvanceToProcessTerminal(t, store, provisioned)
	generic, typed := allocationTestTerminateIntent(t, terminal, provision)
	generic.Deadline = "2026-08-29T00:00:01Z"
	authorityPort := allocationTestAuthority(t, store, terminal.Identity, false, true)
	first, err := authorityPort.CompareAndAppendAllocationTerminateIntent(context.Background(), terminal.Identity, generic, typed)
	if err != nil || !first.Appended {
		t.Fatalf("fresh append=%#v err=%v", first, err)
	}

	clock = clock.Add(2 * time.Second)
	before := allocationLedgerBytes(t, dir)
	reopened, err := openResultIngressStoreWithClock(dir, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	restartedAuthority := allocationTestAuthority(t, reopened, terminal.Identity, false, true)
	replay, err := restartedAuthority.CompareAndAppendAllocationTerminateIntent(context.Background(), terminal.Identity, generic, typed)
	if err != nil || replay.Appended || replay.EffectKey != first.EffectKey || replay.EffectFactDigest != first.EffectFactDigest || replay.AllocationDigest != first.AllocationDigest || !bytes.Equal(before, allocationLedgerBytes(t, dir)) {
		t.Fatalf("expired exact replay=%#v err=%v", replay, err)
	}
	recoveryEntered := false
	if err := restartedAuthority.WithCurrentAllocation(context.Background(), replay.EffectKey, func(session allocationcontrol.AuthoritySession) error {
		recoveryEntered = true
		snapshot, err := session.Snapshot()
		if err != nil || snapshot.Validate() != nil || snapshot.TerminateIntentFactDigest != first.AllocationDigest {
			return ErrAllocationAuthorityConflict
		}
		return nil
	}); err != nil || !recoveryEntered {
		t.Fatalf("expired exact replay did not enter recovery: entered=%v err=%v", recoveryEntered, err)
	}
}

func TestAllocationAuthorityFreshTerminateDeadlineStillRejectsWithoutAppend(t *testing.T) {
	clock := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	store, err := openResultIngressStoreWithClock(dir, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	provisioned, _, provision, _ := allocationCompleteProvision(t, store)
	terminal := allocationAdvanceToProcessTerminal(t, store, provisioned)
	generic, typed := allocationTestTerminateIntent(t, terminal, provision)
	generic.Deadline = "2026-08-29T00:00:01Z"
	clock = clock.Add(2 * time.Second)
	before := allocationLedgerBytes(t, dir)
	authorityPort := allocationTestAuthority(t, store, terminal.Identity, false, true)
	if _, err := authorityPort.CompareAndAppendAllocationTerminateIntent(context.Background(), terminal.Identity, generic, typed); !errors.Is(err, ErrEffectAuthorityExpired) || !bytes.Equal(before, allocationLedgerBytes(t, dir)) {
		t.Fatalf("expired fresh terminate mutated authority err=%v", err)
	}
}

func TestAllocationAuthorityRejectsProviderDomainDriftBeforeAppend(t *testing.T) {
	id := attemptTestIdentity()
	exact := allocationTestProviderDomain(id)
	cases := []struct {
		name   string
		domain authority.SecurityDomainId
	}{
		{name: "tenant", domain: func() authority.SecurityDomainId { value := exact; value.TenantNamespace = "tenant-2"; return value }()},
		{name: "trust", domain: func() authority.SecurityDomainId {
			value := exact
			value.TrustDomainKind = authority.TrustDomainKindPublication
			return value
		}()},
		{name: "isolation", domain: func() authority.SecurityDomainId {
			value := exact
			value.IsolationDomainId = "other-host"
			return value
		}()},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			store, err := OpenResultIngressStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			opened, err := appendAuthorizedAttempt(t, store, 0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: id})
			if err != nil {
				t.Fatal(err)
			}
			generic, typed := allocationTestProvisionIntent(t, opened.State, "provider-domain-"+test.name)
			authorityPort, err := NewAllocationAuthority(store, allocationProvisionVerifier{identity: id, allow: true, providerDomain: test.domain}, allocationCleanupVerifier{identity: id})
			if err != nil {
				t.Fatal(err)
			}
			before := allocationLedgerBytes(t, dir)
			if _, err := authorityPort.CompareAndAppendAllocationProvisionIntent(context.Background(), id, generic, typed); !errors.Is(err, ErrAllocationAuthorityConflict) || !bytes.Equal(before, allocationLedgerBytes(t, dir)) {
				t.Fatalf("provider domain drift mutated authority err=%v", err)
			}
		})
	}
}

func allocationTestProvisionIntent(t *testing.T, state AttemptAuthorityState, effectID string) (authority.SideEffectIntent, allocationcontrol.AllocationProvisionIntentV1) {
	t.Helper()
	namespaceDigest, err := state.Identity.AuthorityNamespaceID.Digest()
	if err != nil {
		t.Fatal(err)
	}
	staging, live, _, marker, err := allocationcontrol.DeriveRelativeNames(state.Identity.AllocationID)
	if err != nil {
		t.Fatal(err)
	}
	derived, err := DeriveAllocationEffectIdentity(state.Identity, EffectPhaseAllocationProvision, effectID, state.HeadDigest)
	if err != nil {
		t.Fatal(err)
	}
	typed := allocationcontrol.AllocationProvisionIntentV1{
		SchemaVersion: allocationcontrol.ProvisionSchema, ProtocolRevision: allocationcontrol.ProtocolRevision,
		Binding: allocationcontrol.AllocationBindingV1{
			AuthorityNamespaceID: namespaceDigest, TaskID: state.Identity.TaskID, RunID: state.Identity.RunID,
			AttemptID: state.Identity.AttemptID, AllocationID: state.Identity.AllocationID, LeaseID: state.Identity.LeaseID,
			Generation: state.Identity.DispatchGeneration, FencingTokenDigest: state.Identity.FencingTokenDigest,
			CommandID: derived.CommandID, IdempotencyKey: derived.IdempotencyKey,
		},
		Requirements:    allocationcontrol.SandboxRequirementsV1{AccessMode: "workspace-write", MinimumAssuranceLevel: "workspace-write"},
		AllowedStoreIDs: []string{}, WorkDirAllowlist: []string{"/tmp/work"}, EnvironmentAllowlist: []string{"PATH"},
		ExpectedOwnerUID: uint32(os.Geteuid()), ExpectedDirectoryMode: 0o700, ExpectedMarkerMode: 0o600,
		StagingRelativeName: staging, LiveRelativeName: live, MarkerRelativeName: marker,
		MarkerNonceDigest: derived.MarkerNonceDigest, ExpectedAttemptSequence: state.Revision + 1, AttemptAuthorityFactDigest: state.HeadDigest,
	}
	if err := typed.Seal(); err != nil {
		t.Fatal(err)
	}
	generic := allocationTestGenericIntent(state.Identity, EffectPhaseAllocationProvision, effectID, derived.CommandID, derived.IdempotencyKey, typed.RequestDigest, derived.MarkerNonceDigest, state.HeadDigest)
	return generic, typed
}

func allocationTestGenericIntent(id AttemptIdentity, phase EffectPhase, effectID, commandID, idempotencyKey, requestDigest, markerDigest, authorityDigest string) authority.SideEffectIntent {
	class := authority.DispositionClassSandboxProvision
	if phase == EffectPhaseAllocationTerminate {
		class = authority.DispositionClassSandboxTerminate
	}
	return authority.SideEffectIntent{
		AuthorityNamespaceId: id.AuthorityNamespaceID, EffectId: effectID, OwnerIdentity: id.OrchestratorID,
		Port: "sandbox", Operation: string(phase), TargetRef: id.AllocationID, TargetDigest: markerDigest,
		RequestDigest: requestDigest, CommandId: commandID, IdempotencyKey: idempotencyKey,
		PolicyDigest: attemptTestDigest("allocation-policy"), AuthorizationDigest: authorityDigest,
		Purpose: "durable local allocation", DispositionClass: class, Deadline: "2099-08-29T00:00:00Z",
	}
}

func allocationTestObjects() (allocationcontrol.ObjectIdentityV1, allocationcontrol.ObjectIdentityV1) {
	directory := allocationcontrol.ObjectIdentityV1{Device: "1", Inode: "2", Mode: 0o040700, UID: uint32(os.Geteuid()), GID: uint32(os.Getegid()), Nlink: 2, Type: allocationcontrol.ObjectTypeDirectory}
	marker := allocationcontrol.ObjectIdentityV1{Device: "1", Inode: "3", Mode: 0o100600, UID: uint32(os.Geteuid()), GID: uint32(os.Getegid()), Size: 100, Nlink: 1, Type: allocationcontrol.ObjectTypeRegular}
	return directory, marker
}

func allocationTestPrepared(t *testing.T, snapshot allocationcontrol.AuthoritySnapshot) allocationcontrol.AllocationStagingPreparedV1 {
	t.Helper()
	intent := *snapshot.ProvisionIntent
	directory, markerIdentity := allocationTestObjects()
	marker := intent.Marker()
	markerBytes, err := marker.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	prepared := allocationcontrol.AllocationStagingPreparedV1{
		SchemaVersion: allocationcontrol.PreparedSchema, ProtocolRevision: allocationcontrol.ProtocolRevision,
		Binding: intent.Binding, IntentFactDigest: snapshot.ProvisionIntentFactDigest, RequestDigest: intent.RequestDigest,
		StagingRelativeName: intent.StagingRelativeName, LiveRelativeName: intent.LiveRelativeName, MarkerRelativeName: intent.MarkerRelativeName,
		StagingIdentity: directory, MarkerIdentity: markerIdentity, Marker: marker, MarkerDigest: canonical.DigestBytes(markerBytes),
	}
	if err := prepared.Seal(); err != nil || prepared.Validate(intent) != nil {
		t.Fatalf("seal prepared err=%v", err)
	}
	return prepared
}

func allocationTestProvisionReceipt(t *testing.T, snapshot allocationcontrol.AuthoritySnapshot) allocationcontrol.AllocationProvisionReceiptV1 {
	t.Helper()
	prepared := *snapshot.ProvisionPrepared
	receipt := allocationcontrol.AllocationProvisionReceiptV1{
		SchemaVersion: allocationcontrol.ProvisionReceiptSchema, ProtocolRevision: allocationcontrol.ProtocolRevision,
		Binding: prepared.Binding, IntentFactDigest: snapshot.ProvisionIntentFactDigest, PreparedFactDigest: snapshot.ProvisionPreparedFactDigest,
		RequestDigest: prepared.RequestDigest, LiveRelativeName: prepared.LiveRelativeName, LiveIdentity: prepared.StagingIdentity,
		MarkerRelativeName: prepared.MarkerRelativeName, MarkerIdentity: prepared.MarkerIdentity, Marker: prepared.Marker,
		MarkerDigest: prepared.MarkerDigest, Disposition: allocationcontrol.DispositionApplied,
	}
	if err := receipt.Seal(); err != nil || receipt.Validate(*snapshot.ProvisionIntent, prepared) != nil {
		t.Fatalf("seal provision receipt err=%v", err)
	}
	return receipt
}

func allocationCompleteProvision(t *testing.T, store *DurableStore) (AttemptAuthorityState, *AllocationAuthority, allocationcontrol.AuthoritySnapshot, string) {
	t.Helper()
	id := attemptTestIdentity()
	opened, err := appendAuthorizedAttempt(t, store, 0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: id})
	if err != nil {
		t.Fatal(err)
	}
	authorityPort := allocationTestAuthority(t, store, id, true, false)
	generic, typed := allocationTestProvisionIntent(t, opened.State, "allocation-provision-1")
	appended, err := authorityPort.CompareAndAppendAllocationProvisionIntent(context.Background(), id, generic, typed)
	if err != nil || !appended.Appended || appended.EffectFactDigest != appended.AllocationDigest || len(appended.Snapshot.Facts) != 1 {
		t.Fatalf("append provision=%#v err=%v", appended, err)
	}
	var final allocationcontrol.AuthoritySnapshot
	err = authorityPort.WithCurrentAllocation(context.Background(), appended.EffectKey, func(session allocationcontrol.AuthoritySession) error {
		first, err := session.Snapshot()
		if err != nil {
			return err
		}
		prepared := allocationTestPrepared(t, first)
		second, err := session.AppendProvisionPrepared(context.Background(), prepared)
		if err != nil {
			return err
		}
		receipt := allocationTestProvisionReceipt(t, second)
		if final, err = session.AppendProvisionReceipt(context.Background(), receipt); err != nil {
			return err
		}
		final, err = session.ProjectAndReconcile(context.Background(), func(allocationcontrol.AuthoritySnapshot) error { return nil })
		return err
	})
	if err != nil || final.Validate() != nil || len(final.Facts) != 3 || final.ProvisionReceipt == nil {
		t.Fatalf("complete provision snapshot=%#v err=%v", final, err)
	}
	current, _, err := store.AttemptState(id)
	if err != nil || current.AllocationProvisionEffectDigest == "" || current.PendingEffectID != "" {
		t.Fatalf("provision attempt=%#v err=%v", current, err)
	}
	effect, ok, err := store.EffectState(id.AuthorityNamespaceID, generic.EffectId)
	if err != nil || !ok || effect.ReconcileFactDigest == "" || effect.Receipt.ObservedDigest != final.ProvisionReceipt.ReceiptDigest {
		t.Fatalf("provision effect=%#v ok=%v err=%v", effect, ok, err)
	}
	return current, authorityPort, final, appended.EffectKey
}

func allocationAdvanceToProcessTerminal(t *testing.T, store *DurableStore, state AttemptAuthorityState) AttemptAuthorityState {
	t.Helper()
	authorized, err := appendAuthorizedAttempt(t, store, state.Revision, state.HeadDigest, AttemptTransition{Kind: AttemptTransitionLaunchAuthorized, Identity: state.Identity, LaunchAuthorizationID: "allocation-launch"})
	if err != nil {
		t.Fatal(err)
	}
	authorized.State = appendTestSupervisorStarted(t, store, authorized.State)
	started, err := appendAuthorizedAttempt(t, store, authorized.State.Revision, authorized.State.HeadDigest, AttemptTransition{Kind: AttemptTransitionProcessStarted, Identity: state.Identity, CommandID: "allocation-agent-command", ObservedAt: "2026-08-29T00:00:00Z", Process: attemptTestProcess(t)})
	if err != nil {
		t.Fatal(err)
	}
	barrier := appendTestBarrier(t, store, started.State, "allocation-terminalization-1", TerminalAttemptCompleted).State
	run := attemptTestRunAuthority(state.Identity)
	request := CleanupAuthorizationRequest{Identity: state.Identity, CurrentRunAuthority: run, TerminalizationID: barrier.TerminalizationID, TerminalGeneration: barrier.TerminalGeneration, CleanupBindingDigest: barrier.CleanupBindingDigest, Operation: CleanupReconcile}
	terminal, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: run}, barrier.Revision, barrier.HeadDigest, request, AttemptTransition{Kind: AttemptTransitionProcessTerminal, Identity: state.Identity, TerminalizationID: barrier.TerminalizationID, ProcessTerminalKind: ProcessAbsent, ObservationDigest: attemptTestDigest("allocation-process-absent")})
	if err != nil {
		t.Fatal(err)
	}
	return terminal.State
}

func allocationTestTerminateIntent(t *testing.T, terminal AttemptAuthorityState, provision allocationcontrol.AuthoritySnapshot) (authority.SideEffectIntent, allocationcontrol.AllocationTerminateIntentV1) {
	return allocationTestTerminateIntentForEffect(t, terminal, provision, "allocation-terminate-1")
}

func allocationTestTerminateIntentForEffect(t *testing.T, terminal AttemptAuthorityState, provision allocationcontrol.AuthoritySnapshot, effectID string) (authority.SideEffectIntent, allocationcontrol.AllocationTerminateIntentV1) {
	t.Helper()
	provisionReceipt := *provision.ProvisionReceipt
	_, live, tombstone, markerName, err := allocationcontrol.DeriveRelativeNames(terminal.Identity.AllocationID)
	if err != nil {
		t.Fatal(err)
	}
	binding := provisionReceipt.Binding
	derived, err := DeriveAllocationEffectIdentity(terminal.Identity, EffectPhaseAllocationTerminate, effectID, terminal.HeadDigest)
	if err != nil {
		t.Fatal(err)
	}
	binding.CommandID, binding.IdempotencyKey = derived.CommandID, derived.IdempotencyKey
	request := allocationcontrol.TerminateRequestV1{
		SchemaVersion: allocationcontrol.TerminateRequestSchema, ProtocolRevision: allocationcontrol.ProtocolRevision,
		Binding: binding, TerminalizationID: terminal.TerminalizationID, CleanupBindingDigest: terminal.CleanupBindingDigest,
		ProcessTerminalFactDigest: terminal.ProcessTerminalDigest, OrchestratorID: terminal.Identity.OrchestratorID,
		ExpectedAttemptSequence: terminal.Revision + 1, AttemptAuthorityFactDigest: terminal.HeadDigest,
		LiveRelativeName: live, TombstoneRelativeName: tombstone,
	}
	if err := request.Seal(); err != nil {
		t.Fatal(err)
	}
	typed := allocationcontrol.AllocationTerminateIntentV1{
		SchemaVersion: allocationcontrol.TerminateSchema, ProtocolRevision: allocationcontrol.ProtocolRevision,
		Binding: binding, TerminalizationID: request.TerminalizationID, CleanupBindingDigest: request.CleanupBindingDigest,
		ProcessTerminalFactDigest: request.ProcessTerminalFactDigest, OrchestratorID: request.OrchestratorID,
		ExpectedAttemptSequence: request.ExpectedAttemptSequence, AttemptAuthorityFactDigest: request.AttemptAuthorityFactDigest,
		LiveRelativeName: live, TombstoneRelativeName: tombstone, MarkerRelativeName: markerName,
		LiveIdentity: provisionReceipt.LiveIdentity, MarkerIdentity: provisionReceipt.MarkerIdentity,
		Marker: provisionReceipt.Marker, MarkerDigest: provisionReceipt.MarkerDigest, RequestDigest: request.RequestDigest,
	}
	if err := typed.Validate(); err != nil {
		t.Fatal(err)
	}
	generic := allocationTestGenericIntent(terminal.Identity, EffectPhaseAllocationTerminate, effectID, binding.CommandID, binding.IdempotencyKey, typed.RequestDigest, typed.Marker.NonceDigest, terminal.HeadDigest)
	return generic, typed
}

func allocationTestTerminateReceipt(t *testing.T, snapshot allocationcontrol.AuthoritySnapshot) allocationcontrol.AllocationTerminateReceiptV1 {
	t.Helper()
	intent := *snapshot.TerminateIntent
	receipt := allocationcontrol.AllocationTerminateReceiptV1{
		SchemaVersion: allocationcontrol.TerminateReceiptSchema, ProtocolRevision: allocationcontrol.ProtocolRevision,
		Binding: intent.Binding, TerminalizationID: intent.TerminalizationID, CleanupBindingDigest: intent.CleanupBindingDigest,
		ProcessTerminalFactDigest: intent.ProcessTerminalFactDigest, OrchestratorID: intent.OrchestratorID,
		ExpectedAttemptSequence: intent.ExpectedAttemptSequence, AttemptAuthorityFactDigest: intent.AttemptAuthorityFactDigest,
		IntentFactDigest: snapshot.TerminateIntentFactDigest, RequestDigest: intent.RequestDigest,
		LiveRelativeName: intent.LiveRelativeName, TombstoneRelativeName: intent.TombstoneRelativeName,
		TombstoneIdentity: intent.LiveIdentity, MarkerRelativeName: intent.MarkerRelativeName,
		MarkerIdentity: intent.MarkerIdentity, Marker: intent.Marker, MarkerDigest: intent.MarkerDigest,
		LiveAbsent: true, TombstonePresent: true, Disposition: allocationcontrol.DispositionApplied,
	}
	if err := receipt.Seal(); err != nil || receipt.Validate(intent) != nil {
		t.Fatalf("seal terminate receipt err=%v", err)
	}
	return receipt
}

func TestAllocationAuthorityFiveFactsExactAndRestart(t *testing.T) {
	dir := t.TempDir()
	store, _ := OpenResultIngressStore(dir)
	provisioned, _, provision, _ := allocationCompleteProvision(t, store)
	terminal := allocationAdvanceToProcessTerminal(t, store, provisioned)
	authorityPort := allocationTestAuthority(t, store, terminal.Identity, false, true)
	generic, typed := allocationTestTerminateIntent(t, terminal, provision)
	intent, err := authorityPort.CompareAndAppendAllocationTerminateIntent(context.Background(), terminal.Identity, generic, typed)
	if err != nil || !intent.Appended || len(intent.Snapshot.Facts) != 4 {
		t.Fatalf("terminate intent=%#v err=%v", intent, err)
	}
	var final allocationcontrol.AuthoritySnapshot
	err = authorityPort.WithCurrentAllocation(context.Background(), intent.EffectKey, func(session allocationcontrol.AuthoritySession) error {
		snapshot, err := session.Snapshot()
		if err != nil {
			return err
		}
		if final, err = session.AppendTerminateReceipt(context.Background(), allocationTestTerminateReceipt(t, snapshot)); err != nil {
			return err
		}
		final, err = session.ProjectAndReconcile(context.Background(), func(allocationcontrol.AuthoritySnapshot) error { return nil })
		return err
	})
	if err != nil || final.Validate() != nil || len(final.Facts) != 5 || final.TerminateReceipt == nil {
		t.Fatalf("five-fact snapshot=%#v err=%v", final, err)
	}
	wantKinds := []allocationcontrol.RecordKind{allocationcontrol.RecordProvisionIntent, allocationcontrol.RecordProvisionPrepared, allocationcontrol.RecordProvisionReceipt, allocationcontrol.RecordTerminateIntent, allocationcontrol.RecordTerminateReceipt}
	for index, kind := range wantKinds {
		if final.Facts[index].RecordKind != kind || final.Facts[index].AttemptAuthorityFactDigest != allocationFactDigest(final, kind) {
			t.Fatalf("fact[%d]=%#v", index, final.Facts[index])
		}
	}
	reopened, err := OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	restartedAuthority := allocationTestAuthority(t, reopened, terminal.Identity, false, true)
	err = restartedAuthority.WithCurrentAllocation(context.Background(), intent.EffectKey, func(session allocationcontrol.AuthoritySession) error {
		replayed, err := session.Snapshot()
		if err != nil || replayed.Validate() != nil || len(replayed.Facts) != 5 || replayed.TerminateReceipt.ReceiptDigest != final.TerminateReceipt.ReceiptDigest {
			t.Fatalf("replayed=%#v err=%v", replayed, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAllocationAuthorityProvisionStaleReplayConflictAndSecondEffect(t *testing.T) {
	dir := t.TempDir()
	store, _ := OpenResultIngressStore(dir)
	id := attemptTestIdentity()
	opened, _ := appendAuthorizedAttempt(t, store, 0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: id})
	authorityPort := allocationTestAuthority(t, store, id, true, false)
	generic, typed := allocationTestProvisionIntent(t, opened.State, "provision-effect")
	before := allocationLedgerBytes(t, dir)
	staleTyped := typed
	staleTyped.ExpectedAttemptSequence++
	staleTyped.AttemptAuthorityFactDigest = attemptTestDigest("stale-head")
	if err := staleTyped.Seal(); err != nil {
		t.Fatal(err)
	}
	staleGeneric := generic
	staleGeneric.RequestDigest, staleGeneric.AuthorizationDigest = staleTyped.RequestDigest, staleTyped.AttemptAuthorityFactDigest
	if _, err := authorityPort.CompareAndAppendAllocationProvisionIntent(context.Background(), id, staleGeneric, staleTyped); err == nil || string(before) != string(allocationLedgerBytes(t, dir)) {
		t.Fatalf("stale append err=%v", err)
	}
	first, err := authorityPort.CompareAndAppendAllocationProvisionIntent(context.Background(), id, generic, typed)
	if err != nil || !first.Appended {
		t.Fatal(err)
	}
	replay, err := authorityPort.CompareAndAppendAllocationProvisionIntent(context.Background(), id, generic, typed)
	if err != nil || replay.Appended || replay.AllocationDigest != first.AllocationDigest {
		t.Fatalf("replay=%#v err=%v", replay, err)
	}
	current, _, _ := store.AttemptState(id)
	secondGeneric, secondTyped := allocationTestProvisionIntent(t, current, "second-effect")
	if _, err := authorityPort.CompareAndAppendAllocationProvisionIntent(context.Background(), id, secondGeneric, secondTyped); err == nil {
		t.Fatal("second pending provision admitted")
	}
	conflictGeneric, conflictTyped := allocationTestProvisionIntent(t, current, "conflict-effect")
	conflictTyped.Binding.CommandID = generic.CommandId
	if err := conflictTyped.Seal(); err != nil {
		t.Fatal(err)
	}
	conflictGeneric.CommandId, conflictGeneric.RequestDigest = generic.CommandId, conflictTyped.RequestDigest
	if _, err := authorityPort.CompareAndAppendAllocationProvisionIntent(context.Background(), id, conflictGeneric, conflictTyped); err == nil {
		t.Fatal("same command conflict admitted")
	}
}

func TestAllocationAuthorityTypedEffectCannotUseGenericRecovery(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	id := attemptTestIdentity()
	opened, err := appendAuthorizedAttempt(t, store, 0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: id})
	if err != nil {
		t.Fatal(err)
	}
	authorityPort := allocationTestAuthority(t, store, id, true, false)
	generic, typed := allocationTestProvisionIntent(t, opened.State, "typed-not-generic-effect")
	appended, err := authorityPort.CompareAndAppendAllocationProvisionIntent(context.Background(), id, generic, typed)
	if err != nil || !appended.Appended {
		t.Fatalf("typed append=%#v err=%v", appended, err)
	}
	before := allocationLedgerBytes(t, dir)
	callbacks := 0
	operator, err := NewEffectOperator(
		func(_ context.Context, _ EffectAuthorityState) (EffectInspection, error) {
			callbacks++
			return EffectInspection{}, errors.New("generic inspect must be unreachable")
		},
		func(_ context.Context, _ EffectAuthorityState) (authority.SideEffectReceipt, error) {
			callbacks++
			return authority.SideEffectReceipt{}, errors.New("generic apply must be unreachable")
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	request := EffectUseRequest{
		Binding: appended.Effect.Binding, EffectID: generic.EffectId,
		IntentFactDigest: appended.Effect.IntentFactDigest, IntentDeadline: generic.Deadline,
	}
	if _, err := store.RecoverPendingEffect(context.Background(), effectVerifier(appended.Effect.Binding), request, operator); err == nil || callbacks != 0 || !bytes.Equal(before, allocationLedgerBytes(t, dir)) {
		t.Fatalf("generic recovery advanced typed allocation callbacks=%d err=%v", callbacks, err)
	}
}

func TestAllocationAuthorityProvisionInactiveAndCleanupInactiveOrthogonal(t *testing.T) {
	store, _ := OpenResultIngressStore(t.TempDir())
	id := attemptTestIdentity()
	opened, _ := appendAuthorizedAttempt(t, store, 0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: id})
	generic, typed := allocationTestProvisionIntent(t, opened.State, "inactive-effect")
	inactive := allocationTestAuthority(t, store, id, false, false)
	statesBefore, _ := store.AttemptStates()
	if _, err := inactive.CompareAndAppendAllocationProvisionIntent(context.Background(), id, generic, typed); err == nil {
		t.Fatal("inactive provision admitted")
	}
	statesAfter, _ := store.AttemptStates()
	if statesAfter[0].HeadDigest != statesBefore[0].HeadDigest {
		t.Fatal("inactive provision mutated authority")
	}
	provisioned, _, provision, _ := allocationCompleteProvision(t, store)
	terminal := allocationAdvanceToProcessTerminal(t, store, provisioned)
	cleanup := allocationTestAuthority(t, store, terminal.Identity, false, true)
	terminateGeneric, terminateTyped := allocationTestTerminateIntent(t, terminal, provision)
	if _, err := cleanup.CompareAndAppendAllocationTerminateIntent(context.Background(), terminal.Identity, terminateGeneric, terminateTyped); err != nil {
		t.Fatalf("inactive dispatch eligibility incorrectly blocked exact cleanup: %v", err)
	}
}

func TestAllocationAuthorityWrongTerminalTupleZeroMutation(t *testing.T) {
	store, _ := OpenResultIngressStore(t.TempDir())
	provisioned, _, provision, _ := allocationCompleteProvision(t, store)
	terminal := allocationAdvanceToProcessTerminal(t, store, provisioned)
	generic, typed := allocationTestTerminateIntent(t, terminal, provision)
	typed.CleanupBindingDigest = attemptTestDigest("wrong-cleanup")
	request := typed.Request()
	request.CleanupBindingDigest = typed.CleanupBindingDigest
	if err := request.Seal(); err != nil {
		t.Fatal(err)
	}
	typed.RequestDigest = request.RequestDigest
	generic.RequestDigest = request.RequestDigest
	check := allocationCheck(EffectBinding{Identity: terminal.Identity, CurrentRunAuthority: runAuthorityBindingFor(terminal.Identity), Phase: EffectPhaseAllocationTerminate, MarkerDigest: typed.Marker.NonceDigest, AdmissionAttemptRevision: terminal.Revision, AdmissionAuthorityDigest: terminal.HeadDigest, TerminalizationID: terminal.TerminalizationID, TerminalGeneration: terminal.TerminalGeneration, CleanupBindingDigest: typed.CleanupBindingDigest, ProcessTerminalFactDigest: terminal.ProcessTerminalDigest}, time.Time{})
	authorityPort, _ := NewAllocationAuthority(store, allocationProvisionVerifier{identity: terminal.Identity}, allocationCleanupVerifier{identity: terminal.Identity, allow: true, want: &check})
	before, _, _ := store.AttemptState(terminal.Identity)
	if _, err := authorityPort.CompareAndAppendAllocationTerminateIntent(context.Background(), terminal.Identity, generic, typed); err == nil {
		t.Fatal("wrong terminal tuple admitted")
	}
	after, _, _ := store.AttemptState(terminal.Identity)
	if after.HeadDigest != before.HeadDigest || after.Revision != before.Revision {
		t.Fatal("wrong terminal tuple mutated authority")
	}
}

func TestAllocationAuthorityReceiptCrashGapRepairsWithoutSecondApplyAcrossTwoRestarts(t *testing.T) {
	dir := t.TempDir()
	store, _ := OpenResultIngressStore(dir)
	_, authorityPort, _, effectKey := allocationCompleteProvision(t, store)
	effectID := "allocation-provision-1"
	effect, ok, err := store.EffectState(attemptTestIdentity().AuthorityNamespaceID, effectID)
	if err != nil || !ok || effect.ReconcileFactDigest == "" {
		t.Fatalf("closed effect=%#v ok=%v err=%v", effect, ok, err)
	}
	removeFinalLedgerLine(t, dir)
	reopened, err := OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	pending, ok, err := reopened.EffectState(attemptTestIdentity().AuthorityNamespaceID, effectID)
	if err != nil || !ok || pending.ReceiptFactDigest == "" || pending.ReconcileFactDigest != "" {
		t.Fatalf("crash-gap effect=%#v ok=%v err=%v", pending, ok, err)
	}
	firstRestart := allocationTestAuthority(t, reopened, attemptTestIdentity(), true, false)
	callbacks := 0
	if err := firstRestart.WithCurrentAllocation(context.Background(), effectKey, func(session allocationcontrol.AuthoritySession) error {
		callbacks++
		snapshot, err := session.Snapshot()
		if err != nil || snapshot.ProvisionReceipt == nil {
			return ErrAllocationAuthorityConflict
		}
		_, err = session.ProjectAndReconcile(context.Background(), func(allocationcontrol.AuthoritySnapshot) error { return nil })
		return err
	}); err != nil || callbacks != 1 {
		t.Fatalf("first restart callbacks=%d err=%v", callbacks, err)
	}
	secondStore, err := OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	secondRestart := allocationTestAuthority(t, secondStore, attemptTestIdentity(), true, false)
	if err := secondRestart.WithCurrentAllocation(context.Background(), effectKey, func(session allocationcontrol.AuthoritySession) error {
		callbacks++
		_, err := session.Snapshot()
		return err
	}); err != nil || callbacks != 2 {
		t.Fatalf("second restart callbacks=%d err=%v", callbacks, err)
	}
	repaired, _, _ := secondStore.EffectState(attemptTestIdentity().AuthorityNamespaceID, effectID)
	if repaired.ReconcileFactDigest == "" || repaired.ReceiptRecordDigest != pending.ReceiptRecordDigest {
		t.Fatalf("repaired effect=%#v", repaired)
	}
	_ = authorityPort
}

func TestAllocationAuthoritySessionCannotEscapeHeldVerifier(t *testing.T) {
	store, _ := OpenResultIngressStore(t.TempDir())
	_, authorityPort, _, effectKey := allocationCompleteProvision(t, store)
	var escaped allocationcontrol.AuthoritySession
	if err := authorityPort.WithCurrentAllocation(context.Background(), effectKey, func(session allocationcontrol.AuthoritySession) error {
		escaped = session
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := escaped.Snapshot(); !errors.Is(err, ErrAllocationAuthorityConflict) {
		t.Fatalf("escaped session err=%v", err)
	}
}

func TestAllocationAuthorityRejectsAmbiguousDualVerifier(t *testing.T) {
	store, _ := OpenResultIngressStore(t.TempDir())
	id := attemptTestIdentity()
	opened, _ := appendAuthorizedAttempt(t, store, 0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: id})
	generic, typed := allocationTestProvisionIntent(t, opened.State, "dual-effect")
	authorityPort, _ := NewAllocationAuthority(store, allocationProvisionVerifier{identity: id, allow: true}, allocationPermissiveCleanupVerifier{})
	before := allocationLedgerBytes(t, store.dir)
	if _, err := authorityPort.CompareAndAppendAllocationProvisionIntent(context.Background(), id, generic, typed); err == nil || string(before) != string(allocationLedgerBytes(t, store.dir)) {
		t.Fatalf("dual verifier err=%v", err)
	}
}

func TestAllocationAuthorityReceiptProjectionReconcileOrderAndTwoRestarts(t *testing.T) {
	dir := t.TempDir()
	store, _ := OpenResultIngressStore(dir)
	id := attemptTestIdentity()
	opened, _ := appendAuthorizedAttempt(t, store, 0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: id})
	authorityPort := allocationTestAuthority(t, store, id, true, false)
	generic, typed := allocationTestProvisionIntent(t, opened.State, "ordered-receipt-effect")
	intent, err := authorityPort.CompareAndAppendAllocationProvisionIntent(context.Background(), id, generic, typed)
	if err != nil {
		t.Fatal(err)
	}
	projectionCalls := 0
	err = authorityPort.WithCurrentAllocation(context.Background(), intent.EffectKey, func(session allocationcontrol.AuthoritySession) error {
		snapshot, err := session.Snapshot()
		if err != nil {
			return err
		}
		snapshot, err = session.AppendProvisionPrepared(context.Background(), allocationTestPrepared(t, snapshot))
		if err != nil {
			return err
		}
		if _, err = session.AppendProvisionReceipt(context.Background(), allocationTestProvisionReceipt(t, snapshot)); err != nil {
			return err
		}
		state, ok, _ := store.EffectState(id.AuthorityNamespaceID, generic.EffectId)
		attempt, _, _ := store.AttemptState(id)
		if !ok || state.ReceiptFactDigest == "" || state.ReconcileFactDigest != "" || attempt.PendingEffectID != generic.EffectId {
			t.Fatalf("receipt cleared barrier before projection: effect=%#v attempt=%#v", state, attempt)
		}
		_, err = session.ProjectAndReconcile(context.Background(), func(allocationcontrol.AuthoritySnapshot) error {
			projectionCalls++
			return errors.New("injected projection fsync failure")
		})
		return err
	})
	if err == nil || projectionCalls != 1 {
		t.Fatalf("projection failure err=%v calls=%d", err, projectionCalls)
	}

	firstRestart, err := OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	firstAuthority := allocationTestAuthority(t, firstRestart, id, true, false)
	err = firstAuthority.WithCurrentAllocation(context.Background(), intent.EffectKey, func(session allocationcontrol.AuthoritySession) error {
		_, err := session.ProjectAndReconcile(context.Background(), func(snapshot allocationcontrol.AuthoritySnapshot) error {
			projectionCalls++
			if snapshot.ProvisionReceipt == nil {
				return errors.New("receipt missing from projection callback")
			}
			return nil
		})
		return err
	})
	if err != nil || projectionCalls != 2 {
		t.Fatalf("first restart err=%v calls=%d", err, projectionCalls)
	}
	secondRestart, err := OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	closed, ok, _ := secondRestart.EffectState(id.AuthorityNamespaceID, generic.EffectId)
	current, _, _ := secondRestart.AttemptState(id)
	if !ok || closed.ReconcileFactDigest == "" || current.PendingEffectID != "" || current.AllocationProvisionEffectDigest != closed.ReconcileFactDigest {
		t.Fatalf("second restart effect=%#v attempt=%#v", closed, current)
	}
}

func TestAllocationAuthorityProjectionConflictBecomesDurableIntervention(t *testing.T) {
	dir := t.TempDir()
	store, _ := OpenResultIngressStore(dir)
	id := attemptTestIdentity()
	opened, _ := appendAuthorizedAttempt(t, store, 0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: id})
	authorityPort := allocationTestAuthority(t, store, id, true, false)
	generic, typed := allocationTestProvisionIntent(t, opened.State, "projection-conflict-effect")
	intent, err := authorityPort.CompareAndAppendAllocationProvisionIntent(context.Background(), id, generic, typed)
	if err != nil {
		t.Fatal(err)
	}
	projectionCalls := 0
	err = authorityPort.WithCurrentAllocation(context.Background(), intent.EffectKey, func(session allocationcontrol.AuthoritySession) error {
		snapshot, err := session.Snapshot()
		if err != nil {
			return err
		}
		snapshot, err = session.AppendProvisionPrepared(context.Background(), allocationTestPrepared(t, snapshot))
		if err != nil {
			return err
		}
		if _, err = session.AppendProvisionReceipt(context.Background(), allocationTestProvisionReceipt(t, snapshot)); err != nil {
			return err
		}
		_, err = session.ProjectAndReconcile(context.Background(), func(allocationcontrol.AuthoritySnapshot) error {
			projectionCalls++
			return allocationcontrol.ErrFilesystemConflict
		})
		return err
	})
	if !errors.Is(err, allocationcontrol.ErrFilesystemConflict) || projectionCalls != 1 {
		t.Fatalf("projection conflict err=%v calls=%d", err, projectionCalls)
	}

	reopened, err := OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	restarted := allocationTestAuthority(t, reopened, id, true, false)
	operations := 0
	if err := restarted.WithCurrentAllocation(context.Background(), intent.EffectKey, func(allocationcontrol.AuthoritySession) error {
		operations++
		return nil
	}); !errors.Is(err, ErrAllocationIntervention) || operations != 0 {
		t.Fatalf("restart err=%v operations=%d", err, operations)
	}
	effect, ok, _ := reopened.EffectState(id.AuthorityNamespaceID, generic.EffectId)
	attempt, _, _ := reopened.AttemptState(id)
	if !ok || effect.AllocationFailureKind != allocationcontrol.AuthorityFailureConflict || effect.Reconcile.Decision != authority.DecisionBlock || effect.Receipt.Disposition != authority.DispositionApplied || attempt.EffectInterventionDigest != effect.ReconcileFactDigest || attempt.AllocationProvisionEffectDigest != "" {
		t.Fatalf("effect=%#v attempt=%#v", effect, attempt)
	}
}

func TestAllocationAuthorityFailureKindsAreDurableIdempotentIntervention(t *testing.T) {
	for _, kind := range []allocationcontrol.AuthorityFailureKind{
		allocationcontrol.AuthorityFailureConflict,
		allocationcontrol.AuthorityFailureAmbiguous,
		allocationcontrol.AuthorityFailureUnknown,
	} {
		t.Run(string(kind), func(t *testing.T) {
			dir := t.TempDir()
			store, _ := OpenResultIngressStore(dir)
			id := attemptTestIdentity()
			opened, _ := appendAuthorizedAttempt(t, store, 0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: id})
			authorityPort := allocationTestAuthority(t, store, id, true, false)
			generic, typed := allocationTestProvisionIntent(t, opened.State, "failure-"+string(kind))
			intent, err := authorityPort.CompareAndAppendAllocationProvisionIntent(context.Background(), id, generic, typed)
			if err != nil {
				t.Fatal(err)
			}
			operations := 0
			err = authorityPort.WithCurrentAllocation(context.Background(), intent.EffectKey, func(session allocationcontrol.AuthoritySession) error {
				operations++
				if err := session.RecordIntervention(context.Background(), kind); err != nil {
					return err
				}
				return session.RecordIntervention(context.Background(), kind)
			})
			if err != nil || operations != 1 {
				t.Fatalf("record err=%v operations=%d", err, operations)
			}
			reopened, _ := OpenResultIngressStore(dir)
			restarted := allocationTestAuthority(t, reopened, id, true, false)
			if err := restarted.WithCurrentAllocation(context.Background(), intent.EffectKey, func(allocationcontrol.AuthoritySession) error {
				operations++
				return nil
			}); !errors.Is(err, ErrAllocationIntervention) || operations != 1 {
				t.Fatalf("replay err=%v operations=%d", err, operations)
			}
			effect, ok, _ := reopened.EffectState(id.AuthorityNamespaceID, generic.EffectId)
			attempt, _, _ := reopened.AttemptState(id)
			if !ok || effect.AllocationFailureKind != kind || effect.Reconcile.Observation != allocationFailureObservation(kind) || effect.Reconcile.Decision != authority.DecisionBlock || effect.Receipt.Disposition == authority.DispositionApplied || attempt.EffectInterventionDigest != effect.ReconcileFactDigest || attempt.AllocationProvisionEffectDigest != "" {
				t.Fatalf("effect=%#v attempt=%#v", effect, attempt)
			}
		})
	}
}

func TestAllocationAuthorityEffectKeyIsolatesSameIDAcrossNamespaces(t *testing.T) {
	store, _ := OpenResultIngressStore(t.TempDir())
	firstID := attemptTestIdentity()
	secondID := firstID
	secondID.AuthorityNamespaceID.TenantNamespace = "tenant-2"
	secondID.TaskID, secondID.RunID, secondID.AttemptID, secondID.AllocationID, secondID.LeaseID = "task-2", "run-2", "attempt-2", "allocation-2", "lease-2"
	firstOpened, _ := appendAuthorizedAttempt(t, store, 0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: firstID})
	secondOpened, _ := appendAuthorizedAttempt(t, store, 0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: secondID})
	appendFor := func(id AttemptIdentity, opened AttemptAuthorityState) AllocationIntentAppendResult {
		authorityPort := allocationTestAuthority(t, store, id, true, false)
		generic, typed := allocationTestProvisionIntent(t, opened, "same-effect-id")
		result, err := authorityPort.CompareAndAppendAllocationProvisionIntent(context.Background(), id, generic, typed)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := appendFor(firstID, firstOpened.State)
	second := appendFor(secondID, secondOpened.State)
	if first.EffectKey == second.EffectKey || first.EffectKey == "" || second.EffectKey == "" {
		t.Fatalf("effect keys collided: first=%q second=%q", first.EffectKey, second.EffectKey)
	}
	firstState, _, _, err := store.loadAllocationEffect(first.EffectKey)
	if err != nil || firstState.Binding.Identity != firstID {
		t.Fatalf("first lookup=%#v err=%v", firstState, err)
	}
	secondState, _, _, err := store.loadAllocationEffect(second.EffectKey)
	if err != nil || secondState.Binding.Identity != secondID {
		t.Fatalf("second lookup=%#v err=%v", secondState, err)
	}
	firstAuthority := allocationTestAuthority(t, store, firstID, true, false)
	if err := firstAuthority.WithCurrentAllocation(context.Background(), first.EffectKey, func(session allocationcontrol.AuthoritySession) error {
		return session.RecordIntervention(context.Background(), allocationcontrol.AuthorityFailureConflict)
	}); err != nil {
		t.Fatal(err)
	}
	secondState, _, _, err = store.loadAllocationEffect(second.EffectKey)
	if err != nil || secondState.ReconcileFactDigest != "" || secondState.AllocationFailureKind != "" {
		t.Fatalf("first namespace mutation contaminated second: %#v err=%v", secondState, err)
	}
}

func allocationLedgerBytes(t *testing.T, dir string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, resultIngressStoreFileName))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func removeFinalLedgerLine(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, resultIngressStoreFileName)
	data := allocationLedgerBytes(t, dir)
	trimmed := strings.TrimSuffix(string(data), "\n")
	index := strings.LastIndexByte(trimmed, '\n')
	if index < 0 {
		t.Fatal("ledger has no final line to remove")
	}
	if err := os.WriteFile(path, []byte(trimmed[:index+1]), 0o600); err != nil {
		t.Fatal(err)
	}
}
