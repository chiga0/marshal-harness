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

// ErrMemoryOnlyAgentLedger 在账本未绑定耐久目录时返回：memory-only 的 agent
// 注册账本在生产权威路径中不被接受（与 LeaseLedger 的等价约束一致）。
var ErrMemoryOnlyAgentLedger = errors.New("agentregistry: memory-only agent ledger not allowed: the agent ledger is not bound to a durable directory")

const (
	agentFactTypeRegistered   = "agent-registered"
	agentFactTypeTransitioned = "agent-lifecycle-transitioned"
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

// AgentLedger 是 agent registration + lifecycle 的耐久 append-only 账本
// （R2 纵切）。与 dispatch.LeaseLedger 同构：每条 fact 一行 RFC 8785 JCS
// 规范 JSON + detached digest + 单调序列号；崩溃/重启由 NewAgentLedger
// 确定性重放重建索引。快照内容（capability snapshot）由 adapter Probe 稳定
// 派生，不重复落账——只有注册身份 + 生命周期（active/revoked/…）属于
// 权威状态需要耐久。
type AgentLedger struct {
	dir              string
	registrations    map[string]*AgentRegistration
	byIdempotencyKey map[string]string
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
		nextSequence:     1,
	}
	if err := ledger.recover(); err != nil {
		return nil, err
	}
	return ledger, nil
}

func (l *AgentLedger) requireBound() error {
	if l == nil || l.dir == "" {
		return ErrMemoryOnlyAgentLedger
	}
	return nil
}

func (l *AgentLedger) ledgerPath() string { return filepath.Join(l.dir, agentLedgerFileName) }

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
	l.mu.Lock()
	defer l.mu.Unlock()
	if existingID, ok := l.byIdempotencyKey[reg.IdempotencyKey]; ok {
		existing := l.registrations[existingID]
		if existing.RequestDigest == reg.RequestDigest {
			return existing, nil // 幂等重放，不追加新行
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
	return &stored, nil
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
	l.mu.Lock()
	defer l.mu.Unlock()
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
	return reg, nil
}

// Lookup 按 RegistrationID 查注册（exact；未注册报错）。
func (l *AgentLedger) Lookup(registrationID string) (*AgentRegistration, error) {
	if err := l.requireBound(); err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	reg, ok := l.registrations[registrationID]
	if !ok {
		return nil, fmt.Errorf("agentregistry: registration %q not found", registrationID)
	}
	return reg, nil
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
		Digest   string `json:"digest"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		return err
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
