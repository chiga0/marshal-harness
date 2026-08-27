package locationattest

import (
	"errors"
	"fmt"
	"sync"
)

// ErrFactConflict 拒绝同身份元组绑定不同 HandleDigest（重复观测同一句柄
// 身份却给出不同内容，即篡改或身份混用证据，绝不静默覆盖）。
var ErrFactConflict = errors.New("location fact conflict")

// factKey 是 fact 的注册身份元组；同元组同内容幂等，同元组不同内容
// fail closed。FactDigest 仍作为完整性 proof 校验与出示引用。
type factKey struct {
	allocationID string
	generation   int64
	handleKind   HandleKind
	observerID   string
}

func keyOf(f LocationFact) factKey {
	return factKey{
		allocationID: f.AllocationID,
		generation:   f.Generation,
		handleKind:   f.HandleKind,
		observerID:   f.ObserverID,
	}
}

// FactLedger 是 authority-verified location fact 的 immutable 存储：
// put-if-absent（身份元组为键）、注册时重算 FactDigest、重复注册同一
// fact 幂等。fact 只增不改；并发安全；无时钟依赖。
type FactLedger struct {
	mu    sync.Mutex
	facts map[factKey]LocationFact
}

// NewFactLedger 返回一个空的可用 FactLedger。
func NewFactLedger() *FactLedger {
	return &FactLedger{facts: make(map[factKey]LocationFact)}
}

// RegisterFact 注册一条 fact。Validate 全通过才接纳；同身份元组的同一
// 内容幂等成功，同身份元组不同 HandleDigest fail closed（篡改或身份
// 混用证据，绝不静默覆盖）。
func (l *FactLedger) RegisterFact(fact LocationFact) error {
	if err := fact.Validate(); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	key := keyOf(fact)
	if existing, ok := l.facts[key]; ok {
		if existing.FactDigest == fact.FactDigest {
			return nil
		}
		return fmt.Errorf("locationattest: %w: identity (%s gen=%d kind=%s observer=%s) already bound to different content",
			ErrFactConflict, key.allocationID, key.generation, key.handleKind, key.observerID)
	}
	l.facts[key] = fact
	return nil
}

// FactsFor 返回与 (allocationID, generation) 精确匹配的全部 fact 副本。
func (l *FactLedger) FactsFor(allocationID string, generation int64) []LocationFact {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []LocationFact
	for _, f := range l.facts {
		if f.AllocationID == allocationID && f.Generation == generation {
			out = append(out, f)
		}
	}
	return out
}
