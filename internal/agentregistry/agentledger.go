package agentregistry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

// agentLedgerFileName 是 agent registry 的 append-only 账本文件名。
const agentLedgerFileName = "agents.jsonl"
const agentLedgerLockFileName = "agents.lock"

// ErrMemoryOnlyAgentLedger 在账本未绑定耐久目录时返回：memory-only 的 agent
// 注册账本在生产权威路径中不被接受（与 LeaseLedger 的等价约束一致）。
var ErrMemoryOnlyAgentLedger = errors.New("agentregistry: memory-only agent ledger not allowed: the agent ledger is not bound to a durable directory")

const (
	agentFactTypeRegistered   = "agent-registered"
	agentFactTypeTransitioned = "agent-lifecycle-transitioned"
	agentFactTypeSnapshot     = "agent-capability-snapshot-captured"
	agentFactTypeActivated    = "agent-capability-snapshot-activated"
)

// agentRegisterFact 是 append-only 账本的一条注册事实：完整登记快照 + 序列号 +
// detached content digest。内容从不改写，只追加。
type agentRegisterFact struct {
	FactType     string            `json:"factType"`
	Sequence     int64             `json:"sequence"`
	Registration AgentRegistration `json:"registration"`
	Digest       string            `json:"digest"`
}

// agentTransitionFact 是 append-only 账本的一条生命周期迁移事实：不写回注册
// 行本身，迁移后形成新生命周期状态的快照登记。
type agentTransitionFact struct {
	FactType     string            `json:"factType"`
	Sequence     int64             `json:"sequence"`
	Registration AgentRegistration `json:"registration"`
	Digest       string            `json:"digest"`
}

// agentSnapshotFact 是不可变 capability snapshot 的 append-only capture
// 事实。相同 digest + 相同内容幂等；相同 digest + 不同内容冲突 fail closed。
type agentSnapshotFact struct {
	FactType string                  `json:"factType"`
	Sequence int64                   `json:"sequence"`
	Snapshot AgentCapabilitySnapshot `json:"snapshot"`
	Digest   string                  `json:"digest"`
}

// AgentLedger 是 agent registration + lifecycle + capability snapshot 的耐久 append-only 账本
// （R2 纵切）。与 dispatch.LeaseLedger 同构：每条 fact 一行 RFC 8785 JCS
// 规范 JSON + detached digest + 单调序列号；崩溃/重启由 NewAgentLedger
// 确定性重放重建索引。registration、snapshot capture/supersede 与 lifecycle
// 都是 current-ledger eligibility 的权威事实，禁止在重启后从临时 Probe
// 静默重建或用结果携带字段自证。
type AgentLedger struct {
	dir              string
	registrations    map[string]*AgentRegistration
	byIdempotencyKey map[string]string
	snapshots        map[string]*AgentCapabilitySnapshot
	activeSnapshot   map[string]string
	nextSequence     int64
	mu               sync.Mutex
}

// NewAgentLedger 打开（不存在则创建）耐久账本目录并重放全部 fact 重建索引。
// 空白目录保持可构造但所有读写 fail closed（ErrMemoryOnlyAgentLedger）。
// 损坏、非规范或冲突的账本行一律 fail closed，绝不静默跳过。
func NewAgentLedger(dir string) (*AgentLedger, error) {
	if strings.TrimSpace(dir) == "" {
		return &AgentLedger{}, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("agentregistry: create agent ledger directory: %w", err)
	}
	ledger := &AgentLedger{
		dir:              dir,
		registrations:    map[string]*AgentRegistration{},
		byIdempotencyKey: map[string]string{},
		snapshots:        map[string]*AgentCapabilitySnapshot{},
		activeSnapshot:   map[string]string{},
		nextSequence:     1,
	}
	release, err := ledger.lockAndRefresh()
	if err != nil {
		return nil, err
	}
	release()
	return ledger, nil
}

func (l *AgentLedger) requireBound() error {
	if l == nil || l.dir == "" {
		return ErrMemoryOnlyAgentLedger
	}
	return nil
}

func (l *AgentLedger) ledgerPath() string { return filepath.Join(l.dir, agentLedgerFileName) }

func (l *AgentLedger) lockPath() string { return filepath.Join(l.dir, agentLedgerLockFileName) }

// lockAndRefresh 先取得进程内锁，再取得固定路径 OS 锁，并在该锁下从完整
// 账本重建 current view。所有读写都走这里，确保长期存活的 CLI/Server 不会
// 使用另一进程写入前的陈旧 map；writer 的 sequence 也由同一 OS 锁串行化。
func (l *AgentLedger) lockAndRefresh() (func(), error) {
	if err := l.requireBound(); err != nil {
		return nil, err
	}
	l.mu.Lock()
	lock, err := acquireAgentLedgerLock(l.lockPath())
	if err != nil {
		l.mu.Unlock()
		return nil, err
	}
	release := func() {
		_ = releaseAgentLedgerLock(lock)
		l.mu.Unlock()
	}
	if err := l.refreshLocked(); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

func (l *AgentLedger) refreshLocked() error {
	l.registrations = map[string]*AgentRegistration{}
	l.byIdempotencyKey = map[string]string{}
	l.snapshots = map[string]*AgentCapabilitySnapshot{}
	l.activeSnapshot = map[string]string{}
	l.nextSequence = 1
	return l.recover()
}

// appendLine 给 fact 计算 detached digest、canonical 化并追加一行+sync。
func (l *AgentLedger) appendLine(fact any, getDigest func() string, setDigest func(string) error) error {
	if getDigest() != "" {
		return fmt.Errorf("agentregistry: fact digest must be detached before sealing")
	}
	digest, err := digestOf(fact)
	if err != nil {
		return err
	}
	if err := setDigest(digest); err != nil {
		return err
	}
	rawJSON, err := json.Marshal(fact)
	if err != nil {
		return fmt.Errorf("agentregistry: marshal fact: %w", err)
	}
	raw, err := canonical.JSON(rawJSON)
	if err != nil {
		return fmt.Errorf("agentregistry: canonicalize fact: %w", err)
	}
	line := append(raw, '\n')
	f, err := os.OpenFile(l.ledgerPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("agentregistry: open agent ledger: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("agentregistry: append fact: %w", err)
	}
	return f.Sync()
}

// Register 持久化一条注册事实（幂等同 Registry.Register：同 key 同 digest 幂等
// 重放，同 key 不同 digest 冲突 fail closed，RegistrationID 撞不同 key 亦冲突）。
func (l *AgentLedger) Register(reg AgentRegistration) (*AgentRegistration, error) {
	if err := l.requireBound(); err != nil {
		return nil, err
	}
	if err := reg.Validate(); err != nil {
		return nil, err
	}
	release, err := l.lockAndRefresh()
	if err != nil {
		return nil, err
	}
	defer release()
	if existingID, ok := l.byIdempotencyKey[reg.IdempotencyKey]; ok {
		existing := l.registrations[existingID]
		if existing.RequestDigest == reg.RequestDigest {
			copy := *existing
			return &copy, nil // 幂等重放，不追加新行
		}
		return nil, fmt.Errorf("agentregistry: idempotency key %q reused with different RequestDigest (conflict)", reg.IdempotencyKey)
	}
	if _, exists := l.registrations[reg.RegistrationID]; exists {
		return nil, fmt.Errorf("agentregistry: RegistrationID %q already exists with a different IdempotencyKey", reg.RegistrationID)
	}
	fact := &agentRegisterFact{
		FactType:     agentFactTypeRegistered,
		Sequence:     l.nextSequence,
		Registration: reg,
	}
	if err := l.appendRegisterFact(fact); err != nil {
		return nil, err
	}
	stored := reg
	l.registrations[reg.RegistrationID] = &stored
	l.byIdempotencyKey[reg.IdempotencyKey] = reg.RegistrationID
	l.nextSequence++
	copy := stored
	return &copy, nil
}

func (l *AgentLedger) appendRegisterFact(fact *agentRegisterFact) error {
	fact.Digest = ""
	return l.appendLine(fact,
		func() string { return fact.Digest },
		func(d string) error { fact.Digest = d; return nil })
}

// Transition 持久化一次生命周期迁移（仅当合法；终态无出边；未注册 fail closed）。
// 迁移后整条新状态登记为一行，绝不改写注册行。
func (l *AgentLedger) Transition(registrationID string, target LifecycleState) (*AgentRegistration, error) {
	if err := l.requireBound(); err != nil {
		return nil, err
	}
	if err := target.validate(); err != nil {
		return nil, err
	}
	release, err := l.lockAndRefresh()
	if err != nil {
		return nil, err
	}
	defer release()
	reg, ok := l.registrations[registrationID]
	if !ok {
		return nil, fmt.Errorf("agentregistry: registration %q not found", registrationID)
	}
	allowed, ok := legalTransitions[reg.LifecycleState]
	if !ok {
		return nil, fmt.Errorf("agentregistry: state %q is terminal; transition to %q rejected", reg.LifecycleState, target)
	}
	if _, ok := allowed[target]; !ok {
		return nil, fmt.Errorf("agentregistry: illegal lifecycle transition %q → %q", reg.LifecycleState, target)
	}
	next := *reg
	next.LifecycleState = target
	fact := &agentTransitionFact{
		FactType:     agentFactTypeTransitioned,
		Sequence:     l.nextSequence,
		Registration: next,
	}
	fact.Digest = ""
	if err := l.appendLine(fact,
		func() string { return fact.Digest },
		func(d string) error { fact.Digest = d; return nil }); err != nil {
		return nil, err
	}
	reg.LifecycleState = target
	l.nextSequence++
	copy := *reg
	return &copy, nil
}

// Lookup 按 RegistrationID 查注册（exact；未注册报错）。
func (l *AgentLedger) Lookup(registrationID string) (*AgentRegistration, error) {
	if err := l.requireBound(); err != nil {
		return nil, err
	}
	release, err := l.lockAndRefresh()
	if err != nil {
		return nil, err
	}
	defer release()
	reg, ok := l.registrations[registrationID]
	if !ok {
		return nil, fmt.Errorf("agentregistry: registration %q not found", registrationID)
	}
	copy := *reg
	return &copy, nil
}

// AddSnapshot 持久化不可变 capability snapshot。active snapshot 的后续
// capture 会成为该 registration 的 current snapshot；旧 snapshot 保留为历史
// 事实但不再 eligible。未注册引用、digest 冲突或落账失败均 fail closed。
func (l *AgentLedger) AddSnapshot(snap AgentCapabilitySnapshot) (*AgentCapabilitySnapshot, error) {
	if err := l.requireBound(); err != nil {
		return nil, err
	}
	if err := snap.Validate(); err != nil {
		return nil, err
	}
	release, err := l.lockAndRefresh()
	if err != nil {
		return nil, err
	}
	defer release()
	if _, ok := l.registrations[snap.RegistrationID]; !ok {
		return nil, fmt.Errorf("agentregistry: registration %q not found; cannot add snapshot", snap.RegistrationID)
	}
	if existing, ok := l.snapshots[snap.SnapshotDigest]; ok {
		existingDigest, existingErr := existing.Digest()
		incomingDigest, incomingErr := snap.Digest()
		if existingErr != nil || incomingErr != nil || existingDigest != incomingDigest {
			return nil, fmt.Errorf("agentregistry: SnapshotDigest %q reused with different content (conflict)", snap.SnapshotDigest)
		}
		if snap.SnapshotState == SnapshotStateActive && l.activeSnapshot[snap.RegistrationID] != snap.SnapshotDigest {
			current := l.activeSnapshot[snap.RegistrationID]
			return nil, fmt.Errorf("agentregistry: historical SnapshotDigest %q cannot be reactivated after current changed to %q", snap.SnapshotDigest, current)
		}
		copy := *existing
		return &copy, nil
	}
	fact := &agentSnapshotFact{
		FactType: agentFactTypeSnapshot,
		Sequence: l.nextSequence,
		Snapshot: snap,
	}
	if err := l.appendLine(fact,
		func() string { return fact.Digest },
		func(d string) error { fact.Digest = d; return nil }); err != nil {
		return nil, err
	}
	stored := snap
	l.snapshots[snap.SnapshotDigest] = &stored
	if snap.SnapshotState == SnapshotStateActive {
		l.activeSnapshot[snap.RegistrationID] = snap.SnapshotDigest
	}
	l.nextSequence++
	copy := stored
	return &copy, nil
}

// ActiveSnapshot 返回 registration 当前唯一 eligible 的 active snapshot。
func (l *AgentLedger) ActiveSnapshot(registrationID string) (*AgentCapabilitySnapshot, error) {
	if err := l.requireBound(); err != nil {
		return nil, err
	}
	release, err := l.lockAndRefresh()
	if err != nil {
		return nil, err
	}
	defer release()
	return l.activeSnapshotLocked(registrationID)
}

func (l *AgentLedger) activeSnapshotLocked(registrationID string) (*AgentCapabilitySnapshot, error) {
	digest, ok := l.activeSnapshot[registrationID]
	if !ok {
		return nil, fmt.Errorf("agentregistry: no active snapshot for registration %q", registrationID)
	}
	snap, ok := l.snapshots[digest]
	if !ok || snap.SnapshotState != SnapshotStateActive {
		return nil, fmt.Errorf("agentregistry: active snapshot for registration %q is no longer active", registrationID)
	}
	copy := *snap
	return &copy, nil
}

// CurrentAuthority 在一次 ledger 临界区内返回 registration 与 current active
// snapshot 的一致视图，供 result ingress 做 current-ledger recheck。
func (l *AgentLedger) CurrentAuthority(registrationID string) (*AgentRegistration, *AgentCapabilitySnapshot, error) {
	if err := l.requireBound(); err != nil {
		return nil, nil, err
	}
	release, err := l.lockAndRefresh()
	if err != nil {
		return nil, nil, err
	}
	defer release()
	reg, ok := l.registrations[registrationID]
	if !ok {
		return nil, nil, fmt.Errorf("agentregistry: registration %q not found", registrationID)
	}
	snap, err := l.activeSnapshotLocked(registrationID)
	if err != nil {
		return nil, nil, err
	}
	regCopy := *reg
	return &regCopy, snap, nil
}

// recover 重放账本文件重建索引。损坏/非规范/冲突行一律 fail closed。
func (l *AgentLedger) recover() error {
	data, err := os.ReadFile(l.ledgerPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("agentregistry: read agent ledger: %w", err)
	}
	for i, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if err := l.applyLine([]byte(line)); err != nil {
			return fmt.Errorf("agentregistry: apply ledger line %d: %w", i+1, err)
		}
	}
	return nil
}

// applyLine 解析一行并落到索引。行的 digest 必须与内容一致性校验。
func (l *AgentLedger) applyLine(line []byte) error {
	var head struct {
		FactType string `json:"factType"`
		Sequence int64  `json:"sequence"`
		Digest   string `json:"digest"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		return err
	}
	if head.Sequence != l.nextSequence {
		return fmt.Errorf("non-monotonic agent ledger sequence: got %d want %d", head.Sequence, l.nextSequence)
	}
	switch head.FactType {
	case agentFactTypeRegistered:
		var fact agentRegisterFact
		if err := json.Unmarshal(line, &fact); err != nil {
			return err
		}
		storeddigest := fact.Digest
		fact.Digest = ""
		if digest, err := digestOf(&fact); err != nil || digest != storeddigest {
			return fmt.Errorf("digest mismatch on %s fact", agentFactTypeRegistered)
		}
		fact.Digest = storeddigest
		reg := fact.Registration
		if err := reg.Validate(); err != nil {
			return err
		}
		l.registrations[reg.RegistrationID] = &reg
		l.byIdempotencyKey[reg.IdempotencyKey] = reg.RegistrationID
	case agentFactTypeTransitioned:
		var fact agentTransitionFact
		if err := json.Unmarshal(line, &fact); err != nil {
			return err
		}
		storeddigest := fact.Digest
		fact.Digest = ""
		if digest, err := digestOf(&fact); err != nil || digest != storeddigest {
			return fmt.Errorf("digest mismatch on %s fact", agentFactTypeTransitioned)
		}
		reg := fact.Registration
		l.registrations[reg.RegistrationID] = &reg
		l.byIdempotencyKey[reg.IdempotencyKey] = reg.RegistrationID
	case agentFactTypeSnapshot:
		var fact agentSnapshotFact
		if err := json.Unmarshal(line, &fact); err != nil {
			return err
		}
		storedDigest := fact.Digest
		fact.Digest = ""
		if digest, err := digestOf(&fact); err != nil || digest != storedDigest {
			return fmt.Errorf("digest mismatch on %s fact", agentFactTypeSnapshot)
		}
		snap := fact.Snapshot
		if err := snap.Validate(); err != nil {
			return err
		}
		if _, ok := l.registrations[snap.RegistrationID]; !ok {
			return fmt.Errorf("snapshot references unknown registration %q", snap.RegistrationID)
		}
		if existing, ok := l.snapshots[snap.SnapshotDigest]; ok {
			existingDigest, existingErr := existing.Digest()
			incomingDigest, incomingErr := snap.Digest()
			if existingErr != nil || incomingErr != nil || existingDigest != incomingDigest {
				return fmt.Errorf("SnapshotDigest %q reused with different content", snap.SnapshotDigest)
			}
		} else {
			l.snapshots[snap.SnapshotDigest] = &snap
		}
		if snap.SnapshotState == SnapshotStateActive {
			l.activeSnapshot[snap.RegistrationID] = snap.SnapshotDigest
		}
	case agentFactTypeActivated:
		return fmt.Errorf("historical %s fact is unsupported because it can revive stale attempt authority", agentFactTypeActivated)
	default:
		return fmt.Errorf("unknown agent ledger fact type %q", head.FactType)
	}
	l.nextSequence++
	return nil
}

func digestOf(v any) (string, error) {
	rawJSON, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	raw, err := canonical.JSON(rawJSON)
	if err != nil {
		return "", err
	}
	return canonical.DigestBytes(raw), nil
}
