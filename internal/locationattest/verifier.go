package locationattest

import (
	"errors"
	"fmt"
)

// ErrNilDependency 拒绝构造时的 nil 依赖。
var ErrNilDependency = errors.New("nil FactLedger")

// AssuranceReason 是裁决结论的封闭原因枚举。
type AssuranceReason string

const (
	// AssuranceReasonFactBound 存在故障域外 fact 精确绑定，可支撑 production assurance。
	AssuranceReasonFactBound AssuranceReason = "fact-bound"
	// AssuranceReasonClaimOnly 只有 provider claim、无可用 authority fact；不可支撑 production assurance。
	AssuranceReasonClaimOnly AssuranceReason = "claim-only"
)

// Assurance 是一次位置裁决的确定性结论。
type Assurance struct {
	ClaimDigest string
	// BoundFacts 是排除自证后、与 claim 同 (allocationId, generation) 的
	// authority fact digest 列表。
	BoundFacts []string
	// ProductionAssurance 为 true 仅当 BoundFacts 非空。
	ProductionAssurance bool
	Reason              AssuranceReason
}

// Verifier 对 LocationClaim 做 production assurance 裁决。
type Verifier struct {
	ledger *FactLedger
}

// NewVerifier 构造 Verifier；nil ledger fail closed。
func NewVerifier(ledger *FactLedger) (*Verifier, error) {
	if ledger == nil {
		return nil, fmt.Errorf("locationattest: %w", ErrNilDependency)
	}
	return &Verifier{ledger: ledger}, nil
}

// Evaluate 裁决 claim 的 production assurance：
//
//  1. claim.Validate（ digest 重算，篡改 fail closed）；
//  2. 查找与 claim 同 (allocationId, generation) 的 fact；
//  3. 排除 observerID == claim.ProviderRegistrationID 的 fact（被证明方
//     不得自证；该 fact 视同不存在）；
//  4. 排除后仍剩 fact → ProductionAssurance=true（fact-bound）；否则
//     claim-only。
//
// claim 永远成立为「provider 自报位置」这一观测本身（ClaimlyKnown 语义由
// ClaimDigest 的存在表达）；本裁决只回答「能否支撑 production
// assurance/publication」。
func (v *Verifier) Evaluate(claim LocationClaim) (Assurance, error) {
	if err := claim.Validate(); err != nil {
		return Assurance{}, err
	}

	candidates := v.ledger.FactsFor(claim.AllocationID, claim.Generation)
	bound := make([]string, 0, len(candidates))
	for _, f := range candidates {
		if f.ObserverID == claim.ProviderRegistrationID {
			// 自证 fact：被证明方出示/旁路的"事实"，排除且不计数。
			continue
		}
		bound = append(bound, f.FactDigest)
	}

	if len(bound) == 0 {
		return Assurance{
			ClaimDigest:         claim.ClaimDigest,
			BoundFacts:          bound,
			ProductionAssurance: false,
			Reason:              AssuranceReasonClaimOnly,
		}, nil
	}
	return Assurance{
		ClaimDigest:         claim.ClaimDigest,
		BoundFacts:          bound,
		ProductionAssurance: true,
		Reason:              AssuranceReasonFactBound,
	}, nil
}
