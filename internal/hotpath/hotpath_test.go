package hotpath

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func testDigest(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func baseAdmission() Admission {
	return Admission{
		Digest:         testDigest("base"),
		Kind:           KindWorkerResult,
		Channel:        ChannelCold,
		LedgerSequence: 1,
	}
}

func requireErrorIs(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Errorf("expected %v, got %v", want, err)
	}
}

func requireHotPathPrefix(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	if !strings.HasPrefix(err.Error(), "hotpath: ") {
		t.Errorf("error %q missing hotpath: prefix", err.Error())
	}
}

// ── Record：校验矩阵 ────────────────────────────────────────────────────────

func TestRecord_ValidationMatrix(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(a *Admission)
		wantErr error
	}{
		{name: "valid business kind on cold"},
		{name: "valid checkpoint on cold", mutate: func(a *Admission) {
			a.Kind = KindCheckpoint
		}},
		{name: "valid checkpoint on hot", mutate: func(a *Admission) {
			a.Kind, a.Channel = KindCheckpoint, ChannelHot
		}},
		{name: "valid heartbeat on hot", mutate: func(a *Admission) {
			a.Kind, a.Channel = KindHeartbeat, ChannelHot
		}},
		{name: "valid log on hot", mutate: func(a *Admission) {
			a.Kind, a.Channel = KindLog, ChannelHot
		}},
		{name: "empty digest", mutate: func(a *Admission) {
			a.Digest = ""
		}, wantErr: ErrInvalidDigest},
		{name: "digest missing sha256 prefix", mutate: func(a *Admission) {
			a.Digest = strings.TrimPrefix(a.Digest, "sha256:")
		}, wantErr: ErrInvalidDigest},
		{name: "digest short hex", mutate: func(a *Admission) {
			a.Digest = "sha256:abcd"
		}, wantErr: ErrInvalidDigest},
		{name: "digest uppercase hex", mutate: func(a *Admission) {
			a.Digest = "sha256:" + strings.ToUpper(strings.TrimPrefix(a.Digest, "sha256:"))
		}, wantErr: ErrInvalidDigest},
		{name: "digest non-hex character", mutate: func(a *Admission) {
			a.Digest = "sha256:g" + strings.TrimPrefix(a.Digest, "sha256:")[1:]
		}, wantErr: ErrInvalidDigest},
		{name: "whitespace digest", mutate: func(a *Admission) {
			a.Digest = "   "
		}, wantErr: ErrInvalidDigest},
		{name: "empty kind", mutate: func(a *Admission) {
			a.Kind = ""
		}, wantErr: ErrUnknownKind},
		{name: "unknown kind", mutate: func(a *Admission) {
			a.Kind = EnvelopeKind("cloak")
		}, wantErr: ErrUnknownKind},
		{name: "empty channel", mutate: func(a *Admission) {
			a.Channel = ""
		}, wantErr: ErrUnknownChannel},
		{name: "unknown channel", mutate: func(a *Admission) {
			a.Channel = Channel("warm")
		}, wantErr: ErrUnknownChannel},
		{name: "zero ledger sequence", mutate: func(a *Admission) {
			a.LedgerSequence = 0
		}, wantErr: ErrInvalidSequence},
		{name: "worker-result on hot", mutate: func(a *Admission) {
			a.Channel = ChannelHot
		}, wantErr: ErrHotPathForbidden},
		{name: "candidate on hot", mutate: func(a *Admission) {
			a.Kind, a.Channel = KindCandidate, ChannelHot
		}, wantErr: ErrHotPathForbidden},
		{name: "evidence-ref on hot", mutate: func(a *Admission) {
			a.Kind, a.Channel = KindEvidenceRef, ChannelHot
		}, wantErr: ErrHotPathForbidden},
		{name: "receipt on hot", mutate: func(a *Admission) {
			a.Kind, a.Channel = KindReceipt, ChannelHot
		}, wantErr: ErrHotPathForbidden},
		{name: "assessment on hot", mutate: func(a *Admission) {
			a.Kind, a.Channel = KindAssessment, ChannelHot
		}, wantErr: ErrHotPathForbidden},
		{name: "business kinds all accepted on cold", mutate: func(a *Admission) {
			a.Kind = KindAssessment
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ledger := NewAdmissionLedger()
			a := baseAdmission()
			if tc.mutate != nil {
				tc.mutate(&a)
			}
			err := ledger.Record(a)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Record: unexpected error %v", err)
				}
				got, ok := ledger.Lookup(a.Digest)
				if !ok || got != a {
					t.Fatalf("Lookup: got (%+v, %v), want (%+v, true)", got, ok, a)
				}
				return
			}
			requireErrorIs(t, err, tc.wantErr)
			requireHotPathPrefix(t, err)
		})
	}
}

func TestRecord_NilLedger(t *testing.T) {
	var ledger *AdmissionLedger
	err := ledger.Record(baseAdmission())
	requireErrorIs(t, err, ErrNilLedger)
	requireHotPathPrefix(t, err)
}

// ── Record：幂等 replay ─────────────────────────────────────────────────────

func TestRecord_IdempotentReplay(t *testing.T) {
	ledger := NewAdmissionLedger()
	a := Admission{Digest: testDigest("replay"), Kind: KindCheckpoint, Channel: ChannelHot, LedgerSequence: 7}

	if err := ledger.Record(a); err != nil {
		t.Fatalf("first Record: %v", err)
	}
	if err := ledger.Record(a); err != nil {
		t.Errorf("idempotent re-record must succeed, got %v", err)
	}
	got, ok := ledger.Lookup(a.Digest)
	if !ok || got != a {
		t.Errorf("Lookup: got (%+v, %v), want (%+v, true)", got, ok, a)
	}
}

// ── Record：冲突不覆盖 ──────────────────────────────────────────────────────

func TestRecord_ConflictNonOverwrite(t *testing.T) {
	base := Admission{Digest: testDigest("conflict"), Kind: KindCheckpoint, Channel: ChannelCold, LedgerSequence: 3}

	cases := []struct {
		name   string
		mutate func(a *Admission)
	}{
		{name: "different kind", mutate: func(a *Admission) { a.Kind = KindHeartbeat }},
		{name: "different channel", mutate: func(a *Admission) { a.Channel = ChannelHot }},
		{name: "different ledger sequence", mutate: func(a *Admission) { a.LedgerSequence = 4 }},
		{name: "all fields different", mutate: func(a *Admission) {
			a.Kind, a.Channel, a.LedgerSequence = KindLog, ChannelHot, 9
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ledger := NewAdmissionLedger()
			if err := ledger.Record(base); err != nil {
				t.Fatalf("first Record: %v", err)
			}

			conflicting := base
			tc.mutate(&conflicting)
			err := ledger.Record(conflicting)
			requireErrorIs(t, err, ErrAdmissionConflict)
			requireHotPathPrefix(t, err)

			// 冲突后原始记录必须保持不动（账本永不覆盖）：
			// re-record 原始 admission 幂等成功，Lookup 仍返回原始值。
			if err := ledger.Record(base); err != nil {
				t.Errorf("original admission must still replay idempotently, got %v", err)
			}
			got, ok := ledger.Lookup(base.Digest)
			if !ok || got != base {
				t.Errorf("conflict must not overwrite: got (%+v, %v), want (%+v, true)", got, ok, base)
			}
		})
	}
}

// “把 hot 事实洗成 cold”负测：同 digest 先热路径接纳，再以冷路径重记，
// 必须在账本层冲突拒绝，绝不升格为可 Restore 的 authority。
func TestRecord_HotToColdWashIsConflict(t *testing.T) {
	ledger := NewAdmissionLedger()
	digest := testDigest("wash")
	hot := Admission{Digest: digest, Kind: KindCheckpoint, Channel: ChannelHot, LedgerSequence: 11}
	if err := ledger.Record(hot); err != nil {
		t.Fatalf("hot Record: %v", err)
	}

	washed := hot
	washed.Channel = ChannelCold
	err := ledger.Record(washed)
	requireErrorIs(t, err, ErrAdmissionConflict)
	requireHotPathPrefix(t, err)

	// 洗白失败后账本仍持 hot 事实，且该 checkpoint 仍不可 Restore。
	got, ok := ledger.Lookup(digest)
	if !ok || got != hot {
		t.Errorf("wash attempt must not overwrite: got (%+v, %v), want (%+v, true)", got, ok, hot)
	}
	requireErrorIs(t, ConsumeForRestore(ledger, digest), ErrHotPathForbidden)
}

// ── AllowEffect：4×2 effect×channel 冻结表 ─────────────────────────────────

func TestAllowEffect_Matrix(t *testing.T) {
	effects := []AuthorityEffect{
		EffectRecordObservation,
		EffectExtendLease,
		EffectBumpGeneration,
		EffectDecideFencing,
	}
	channels := []Channel{ChannelHot, ChannelCold}

	for _, ch := range channels {
		for _, eff := range effects {
			name := string(eff) + " on " + string(ch)
			t.Run(name, func(t *testing.T) {
				ledger := NewAdmissionLedger()
				// 热路径用 checkpoint（hot-capable kind）构造合法热接纳。
				a := Admission{
					Digest:         testDigest("effect:" + name),
					Kind:           KindCheckpoint,
					Channel:        ch,
					LedgerSequence: 1,
				}
				if err := ledger.Record(a); err != nil {
					t.Fatalf("Record: %v", err)
				}

				err := AllowEffect(ledger, a.Digest, eff)
				requireHotPathPrefix(t, err)

				if eff == EffectRecordObservation {
					if err != nil {
						t.Errorf("record-observation must be allowed on %s, got %v", ch, err)
					}
					return
				}
				if ch == ChannelHot {
					requireErrorIs(t, err, ErrHotPathForbidden)
					return
				}
				if err != nil {
					t.Errorf("%v must be allowed on cold, got %v", eff, err)
				}
			})
		}
	}
}

func TestAllowEffect_StructuralFailures(t *testing.T) {
	ledger := NewAdmissionLedger()
	a := baseAdmission()
	if err := ledger.Record(a); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// 未知 effect：即使 digest 存在也 fail closed。
	requireErrorIs(t, AllowEffect(ledger, a.Digest, AuthorityEffect("seize")), ErrUnknownEffect)
	requireErrorIs(t, AllowEffect(ledger, a.Digest, ""), ErrUnknownEffect)
	// 未知 digest。
	requireErrorIs(t, AllowEffect(ledger, testDigest("never-recorded"), EffectRecordObservation), ErrUnknownAdmission)
	// nil 账本。
	requireErrorIs(t, AllowEffect(nil, a.Digest, EffectRecordObservation), ErrNilLedger)
}

// ── ConsumeForRestore ───────────────────────────────────────────────────────

func TestConsumeForRestore(t *testing.T) {
	cold := func(seed string, kind EnvelopeKind) Admission {
		return Admission{Digest: testDigest(seed), Kind: kind, Channel: ChannelCold, LedgerSequence: 1}
	}
	hot := func(seed string, kind EnvelopeKind) Admission {
		return Admission{Digest: testDigest(seed), Kind: kind, Channel: ChannelHot, LedgerSequence: 1}
	}

	cases := []struct {
		name    string
		seeded  []Admission // 预置账本（nil 表示空账本）
		digest  string
		wantErr error
	}{
		{
			name:    "unknown digest",
			digest:  testDigest("never-recorded"),
			wantErr: ErrUnknownAdmission,
		},
		{
			name:    "cold log is wrong kind",
			seeded:  []Admission{cold("log", KindLog)},
			digest:  testDigest("log"),
			wantErr: ErrWrongKind,
		},
		{
			name:    "cold heartbeat is wrong kind",
			seeded:  []Admission{cold("hb", KindHeartbeat)},
			digest:  testDigest("hb"),
			wantErr: ErrWrongKind,
		},
		{
			name:    "cold worker-result is wrong kind",
			seeded:  []Admission{cold("wr", KindWorkerResult)},
			digest:  testDigest("wr"),
			wantErr: ErrWrongKind,
		},
		{
			name:    "hot checkpoint is not restorable",
			seeded:  []Admission{hot("hot-ckpt", KindCheckpoint)},
			digest:  testDigest("hot-ckpt"),
			wantErr: ErrHotPathForbidden,
		},
		{
			name:   "cold checkpoint is restorable",
			seeded: []Admission{cold("cold-ckpt", KindCheckpoint)},
			digest: testDigest("cold-ckpt"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ledger := NewAdmissionLedger()
			for _, a := range tc.seeded {
				if err := ledger.Record(a); err != nil {
					t.Fatalf("seed Record: %v", err)
				}
			}
			err := ConsumeForRestore(ledger, tc.digest)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("ConsumeForRestore: unexpected error %v", err)
				}
				// 门禁不消耗账本条目，可幂等重查。
				if err := ConsumeForRestore(ledger, tc.digest); err != nil {
					t.Errorf("restore gate must be idempotent, got %v", err)
				}
				return
			}
			requireErrorIs(t, err, tc.wantErr)
			requireHotPathPrefix(t, err)
		})
	}
}

func TestConsumeForRestore_NilLedger(t *testing.T) {
	requireErrorIs(t, ConsumeForRestore(nil, testDigest("x")), ErrNilLedger)
	requireHotPathPrefix(t, ConsumeForRestore(nil, testDigest("x")))
}

// ── 端到端：热路径无法铸造 cold authority ───────────────────────────────────

// 热路径 checkpoint 可以记录观察、不能延长 lease/bump generation/决定
// fencing、不能 Restore；冷路径 checkpoint 全部通行。这是三条冻结规则的
// 组合断言。
func TestHotAdmissionCannotForgeColdAuthority(t *testing.T) {
	ledger := NewAdmissionLedger()
	hotCkpt := Admission{Digest: testDigest("e2e-hot"), Kind: KindCheckpoint, Channel: ChannelHot, LedgerSequence: 1}
	coldCkpt := Admission{Digest: testDigest("e2e-cold"), Kind: KindCheckpoint, Channel: ChannelCold, LedgerSequence: 2}
	for _, a := range []Admission{hotCkpt, coldCkpt} {
		if err := ledger.Record(a); err != nil {
			t.Fatalf("Record %v: %v", a.Kind, err)
		}
	}

	for _, eff := range []AuthorityEffect{EffectExtendLease, EffectBumpGeneration, EffectDecideFencing} {
		requireErrorIs(t, AllowEffect(ledger, hotCkpt.Digest, eff), ErrHotPathForbidden)
		if err := AllowEffect(ledger, coldCkpt.Digest, eff); err != nil {
			t.Errorf("cold admission must allow %v, got %v", eff, err)
		}
	}
	if err := AllowEffect(ledger, hotCkpt.Digest, EffectRecordObservation); err != nil {
		t.Errorf("hot admission must allow record-observation, got %v", err)
	}
	requireErrorIs(t, ConsumeForRestore(ledger, hotCkpt.Digest), ErrHotPathForbidden)
	if err := ConsumeForRestore(ledger, coldCkpt.Digest); err != nil {
		t.Errorf("cold checkpoint must be restorable, got %v", err)
	}
}
