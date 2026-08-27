package soak

import (
	"testing"

	"github.com/chiga0/marshal-harness/internal/effectsink"
)

// R6-C 合入门禁：10k 迭代 accelerated soak。种子固定，任何违例可重放。
func TestInvariantSoak10k(t *testing.T) {
	stats, err := InvariantSoak(10000, []byte("i186-r6-soak-seed-1"), effectsink.NewEffectLedger(), nil)
	if err != nil {
		t.Fatalf("InvariantSoak: %v", err)
	}
	if stats.ResumeCount+stats.FenceCount == 0 {
		t.Errorf("soak produced no decisions: %+v", stats)
	}
	if stats.EffectCount != 10000 {
		t.Errorf("effect iterations = %d, want 10000", stats.EffectCount)
	}
	t.Logf("soak stats: %+v", stats)
}

// 同一种子必须产生完全相同的场景序列与完全相同的违例（若有）——可重放性。
func TestInvariantSoakReplayIdentical(t *testing.T) {
	s1, e1 := InvariantSoak(500, []byte("replay-seed"), effectsink.NewEffectLedger(), nil)
	s2, e2 := InvariantSoak(500, []byte("replay-seed"), effectsink.NewEffectLedger(), nil)
	if (e1 == nil) != (e2 == nil) {
		t.Fatalf("replay divergence in error surface: %v vs %v", e1, e2)
	}
	if e1 != nil && e1.Error() != e2.Error() {
		t.Fatalf("replay divergence: %v vs %v", e1, e2)
	}
	if s1 != s2 {
		t.Fatalf("replay stats divergence: %+v vs %+v", s1, s2)
	}
}

// 不同种子覆盖不同场景（防固定场景自证）。
func TestInvariantSoakSeedDiversity(t *testing.T) {
	s1, _ := InvariantSoak(200, []byte("seed-A"), effectsink.NewEffectLedger(), nil)
	s2, _ := InvariantSoak(200, []byte("seed-B"), effectsink.NewEffectLedger(), nil)
	if s1.ResumeCount == s2.ResumeCount && s1.FenceCount == s2.FenceCount {
		t.Errorf("distinct seeds must cover distinct scenarios: %+v vs %+v", s1, s2)
	}
}

func TestInvariantSoakRejectsBadInput(t *testing.T) {
	if _, err := InvariantSoak(0, []byte("x"), effectsink.NewEffectLedger(), nil); err == nil {
		t.Errorf("zero iterations must fail closed")
	}
	if _, err := InvariantSoak(1, []byte("x"), nil, nil); err == nil {
		t.Errorf("nil effect ledger must fail closed")
	}
}
