package outbox

import (
	"errors"
	"fmt"
	"testing"

	"github.com/chiga0/marshal-harness/internal/engine"
)

const (
	testFactDigest     = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testRequestDigest  = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testResultDigest   = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	testFactDigest2    = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	testRequestDigest2 = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
)

func validRequest() Request {
	return Request{
		IdempotencyKey: "key-1",
		RequestDigest:  testRequestDigest,
		Kind:           engine.CommandKindDispatch,
		FactDigest:     testFactDigest,
	}
}

func TestCrashMatrix(t *testing.T) {
	type step struct {
		crashPoint CrashPoint
		op         string
		wantErr    error
	}

	cases := []struct {
		name      string
		steps     []step
		wantSeq   int64
		wantFacts int
		wantEnts  int
		wantDisp  int
		wantRes   int
	}{
		{
			name:      "commit no-crash no-replay",
			steps:     []step{{op: "commit"}},
			wantSeq:   1,
			wantFacts: 1,
			wantEnts:  1,
			wantDisp:  0,
			wantRes:   0,
		},
		{
			name:      "commit no-crash replay",
			steps:     []step{{op: "commit"}, {op: "commit"}},
			wantSeq:   1,
			wantFacts: 1,
			wantEnts:  1,
			wantDisp:  0,
			wantRes:   0,
		},
		{
			name:      "commit crash no-replay",
			steps:     []step{{crashPoint: CrashPointCommit, op: "commit", wantErr: ErrCrashInjected}},
			wantSeq:   0,
			wantFacts: 0,
			wantEnts:  0,
			wantDisp:  0,
			wantRes:   0,
		},
		{
			name: "commit crash replay",
			steps: []step{
				{crashPoint: CrashPointCommit, op: "commit", wantErr: ErrCrashInjected},
				{op: "commit"},
			},
			wantSeq:   1,
			wantFacts: 1,
			wantEnts:  1,
			wantDisp:  0,
			wantRes:   0,
		},
		{
			name: "dispatch no-crash no-replay",
			steps: []step{
				{op: "commit"},
				{op: "dispatch"},
			},
			wantSeq:   1,
			wantFacts: 1,
			wantEnts:  1,
			wantDisp:  1,
			wantRes:   0,
		},
		{
			name: "dispatch no-crash replay",
			steps: []step{
				{op: "commit"},
				{op: "dispatch"},
				{op: "dispatch"},
			},
			wantSeq:   1,
			wantFacts: 1,
			wantEnts:  1,
			wantDisp:  1,
			wantRes:   0,
		},
		{
			name: "dispatch crash no-replay",
			steps: []step{
				{op: "commit"},
				{crashPoint: CrashPointDispatch, op: "dispatch", wantErr: ErrCrashInjected},
			},
			wantSeq:   1,
			wantFacts: 1,
			wantEnts:  1,
			wantDisp:  0,
			wantRes:   0,
		},
		{
			name: "dispatch crash replay",
			steps: []step{
				{op: "commit"},
				{crashPoint: CrashPointDispatch, op: "dispatch", wantErr: ErrCrashInjected},
				{op: "dispatch"},
			},
			wantSeq:   1,
			wantFacts: 1,
			wantEnts:  1,
			wantDisp:  1,
			wantRes:   0,
		},
		{
			name: "result no-crash no-replay",
			steps: []step{
				{op: "commit"},
				{op: "dispatch"},
				{op: "result"},
			},
			wantSeq:   1,
			wantFacts: 1,
			wantEnts:  1,
			wantDisp:  1,
			wantRes:   1,
		},
		{
			name: "result no-crash replay",
			steps: []step{
				{op: "commit"},
				{op: "dispatch"},
				{op: "result"},
				{op: "result"},
			},
			wantSeq:   1,
			wantFacts: 1,
			wantEnts:  1,
			wantDisp:  1,
			wantRes:   1,
		},
		{
			name: "result crash no-replay",
			steps: []step{
				{op: "commit"},
				{op: "dispatch"},
				{crashPoint: CrashPointResult, op: "result", wantErr: ErrCrashInjected},
			},
			wantSeq:   1,
			wantFacts: 1,
			wantEnts:  1,
			wantDisp:  1,
			wantRes:   0,
		},
		{
			name: "result crash replay",
			steps: []step{
				{op: "commit"},
				{op: "dispatch"},
				{crashPoint: CrashPointResult, op: "result", wantErr: ErrCrashInjected},
				{op: "result"},
			},
			wantSeq:   1,
			wantFacts: 1,
			wantEnts:  1,
			wantDisp:  1,
			wantRes:   1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ob := New()
			req := validRequest()
			var commandId string

			for _, s := range tc.steps {
				if s.crashPoint != "" {
					ob.setCrashPoint(s.crashPoint)
				} else {
					ob.setCrashPoint("")
				}

				switch s.op {
				case "commit":
					r, err := ob.Commit(req)
					if s.wantErr != nil {
						if !errors.Is(err, s.wantErr) {
							t.Fatalf("commit: want %v, got %v", s.wantErr, err)
						}
						continue
					}
					if err != nil {
						t.Fatalf("commit: unexpected error: %v", err)
					}
					commandId = r.CommandId
				case "dispatch":
					if commandId == "" {
						t.Fatal("dispatch: no commandId")
					}
					_, err := ob.Dispatch(commandId)
					if s.wantErr != nil {
						if !errors.Is(err, s.wantErr) {
							t.Fatalf("dispatch: want %v, got %v", s.wantErr, err)
						}
						continue
					}
					if err != nil {
						t.Fatalf("dispatch: unexpected error: %v", err)
					}
				case "result":
					if commandId == "" {
						t.Fatal("result: no commandId")
					}
					_, err := ob.RecordResult(commandId, testResultDigest)
					if s.wantErr != nil {
						if !errors.Is(err, s.wantErr) {
							t.Fatalf("result: want %v, got %v", s.wantErr, err)
						}
						continue
					}
					if err != nil {
						t.Fatalf("result: unexpected error: %v", err)
					}
				}
			}

			report, err := ob.Recover()
			if err != nil {
				t.Fatalf("Recover: %v", err)
			}
			if report.Sequence != tc.wantSeq {
				t.Errorf("sequence: want %d, got %d", tc.wantSeq, report.Sequence)
			}
			if report.FactCount != tc.wantFacts {
				t.Errorf("facts: want %d, got %d", tc.wantFacts, report.FactCount)
			}
			if report.EntryCount != tc.wantEnts {
				t.Errorf("entries: want %d, got %d", tc.wantEnts, report.EntryCount)
			}
			if report.DispatchedCount != tc.wantDisp {
				t.Errorf("dispatched: want %d, got %d", tc.wantDisp, report.DispatchedCount)
			}
			if report.ResultCount != tc.wantRes {
				t.Errorf("results: want %d, got %d", tc.wantRes, report.ResultCount)
			}
		})
	}
}

func TestMalformedInputs(t *testing.T) {
	cases := []struct {
		name string
		req  Request
	}{
		{name: "empty idempotencyKey", req: Request{IdempotencyKey: "", RequestDigest: testRequestDigest, Kind: engine.CommandKindDispatch, FactDigest: testFactDigest}},
		{name: "empty requestDigest", req: Request{IdempotencyKey: "k", RequestDigest: "", Kind: engine.CommandKindDispatch, FactDigest: testFactDigest}},
		{name: "bad requestDigest prefix", req: Request{IdempotencyKey: "k", RequestDigest: "bad", Kind: engine.CommandKindDispatch, FactDigest: testFactDigest}},
		{name: "empty factDigest", req: Request{IdempotencyKey: "k", RequestDigest: testRequestDigest, Kind: engine.CommandKindDispatch, FactDigest: ""}},
		{name: "unknown kind", req: Request{IdempotencyKey: "k", RequestDigest: testRequestDigest, Kind: "bogus", FactDigest: testFactDigest}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ob := New()
			_, err := ob.Commit(tc.req)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, ErrRequestInvalid) {
				t.Errorf("want ErrRequestInvalid, got %v", err)
			}
		})
	}

	t.Run("nil request dispatch", func(t *testing.T) {
		ob := New()
		_, err := ob.Dispatch("")
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("unknown command dispatch", func(t *testing.T) {
		ob := New()
		_, err := ob.Dispatch("nonexistent")
		if err == nil {
			t.Fatal("expected error")
		}
		if !errors.Is(err, ErrUnknownCommand) {
			t.Errorf("want ErrUnknownCommand, got %v", err)
		}
	})

	t.Run("empty resultDigest", func(t *testing.T) {
		ob := New()
		req := validRequest()
		r, _ := ob.Commit(req)
		ob.Dispatch(r.CommandId)
		_, err := ob.RecordResult(r.CommandId, "")
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("unknown crash point", func(t *testing.T) {
		cp := CrashPoint("bogus")
		if err := cp.Validate(); err == nil {
			t.Fatal("expected error for unknown crash point")
		}
	})

	t.Run("result without dispatch", func(t *testing.T) {
		ob := New()
		req := validRequest()
		r, _ := ob.Commit(req)
		_, err := ob.RecordResult(r.CommandId, testResultDigest)
		if !errors.Is(err, ErrNotDispatched) {
			t.Fatalf("want ErrNotDispatched, got %v", err)
		}
	})

	t.Run("result for unknown command", func(t *testing.T) {
		ob := New()
		_, err := ob.RecordResult("nonexistent", testResultDigest)
		if !errors.Is(err, ErrUnknownCommand) {
			t.Fatalf("want ErrUnknownCommand, got %v", err)
		}
	})
}

func TestIdempotentCommit(t *testing.T) {
	ob := New()
	req := validRequest()

	r1, err := ob.Commit(req)
	if err != nil {
		t.Fatalf("first commit: %v", err)
	}

	r2, err := ob.Commit(req)
	if err != nil {
		t.Fatalf("replay commit: %v", err)
	}

	if r1.CommandId != r2.CommandId {
		t.Errorf("commandId mismatch: %s vs %s", r1.CommandId, r2.CommandId)
	}
	if r1.Sequence != r2.Sequence {
		t.Errorf("sequence mismatch: %d vs %d", r1.Sequence, r2.Sequence)
	}
	if ob.LedgerSequence() != 1 {
		t.Errorf("sequence advanced: %d", ob.LedgerSequence())
	}
	if ob.EntryCount() != 1 {
		t.Errorf("entry count: %d", ob.EntryCount())
	}
}

func TestIdempotencyConflict(t *testing.T) {
	ob := New()
	req1 := validRequest()
	_, err := ob.Commit(req1)
	if err != nil {
		t.Fatalf("first commit: %v", err)
	}

	req2 := req1
	req2.RequestDigest = testRequestDigest2
	_, err = ob.Commit(req2)
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("want ErrIdempotencyConflict, got %v", err)
	}
}

func TestCommandIdCrossVerify(t *testing.T) {
	ob := New()
	req := validRequest()
	r, err := ob.Commit(req)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	expected, err := engine.DeriveCommandId(req.FactDigest, req.Kind)
	if err != nil {
		t.Fatalf("engine.DeriveCommandId: %v", err)
	}

	if r.CommandId != expected {
		t.Errorf("commandId: outbox %s, engine %s", r.CommandId, expected)
	}
}

func TestDispatchIdempotent(t *testing.T) {
	ob := New()
	req := validRequest()
	r, _ := ob.Commit(req)

	first, err := ob.Dispatch(r.CommandId)
	if err != nil {
		t.Fatalf("first dispatch: %v", err)
	}
	if !first {
		t.Error("first dispatch should return true")
	}

	second, err := ob.Dispatch(r.CommandId)
	if err != nil {
		t.Fatalf("second dispatch: %v", err)
	}
	if second {
		t.Error("second dispatch should return false (idempotent)")
	}
	if ob.DispatchCount() != 1 {
		t.Errorf("dispatch count: %d", ob.DispatchCount())
	}
}

func TestResultIdempotent(t *testing.T) {
	ob := New()
	req := validRequest()
	r, _ := ob.Commit(req)
	ob.Dispatch(r.CommandId)

	_, err := ob.RecordResult(r.CommandId, testResultDigest)
	if err != nil {
		t.Fatalf("first result: %v", err)
	}

	replay, err := ob.RecordResult(r.CommandId, testResultDigest)
	if err != nil {
		t.Fatalf("replay result: %v", err)
	}
	if !replay {
		t.Error("replay should return true (idempotent)")
	}
	if ob.ResultCount() != 1 {
		t.Errorf("result count: %d", ob.ResultCount())
	}
}

func TestResultConflict(t *testing.T) {
	ob := New()
	req := validRequest()
	r, _ := ob.Commit(req)
	ob.Dispatch(r.CommandId)

	_, err := ob.RecordResult(r.CommandId, testResultDigest)
	if err != nil {
		t.Fatalf("first result: %v", err)
	}

	differentDigest := "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	_, err = ob.RecordResult(r.CommandId, differentDigest)
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("want ErrIdempotencyConflict, got %v", err)
	}
}

func TestRecoverEmpty(t *testing.T) {
	ob := New()
	report, err := ob.Recover()
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if report.Sequence != 0 {
		t.Errorf("sequence: %d", report.Sequence)
	}
	if report.FactCount != 0 {
		t.Errorf("facts: %d", report.FactCount)
	}
	if report.EntryCount != 0 {
		t.Errorf("entries: %d", report.EntryCount)
	}
}

func TestRecoverPendingDispatch(t *testing.T) {
	ob := New()
	req := validRequest()
	r, _ := ob.Commit(req)

	report, err := ob.Recover()
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if len(report.PendingDispatch) != 1 || report.PendingDispatch[0] != r.CommandId {
		t.Errorf("pending dispatch: %v", report.PendingDispatch)
	}
}

func TestRecoverPendingResult(t *testing.T) {
	ob := New()
	req := validRequest()
	r, _ := ob.Commit(req)
	ob.Dispatch(r.CommandId)

	report, err := ob.Recover()
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if len(report.PendingResult) != 1 || report.PendingResult[0] != r.CommandId {
		t.Errorf("pending result: %v", report.PendingResult)
	}
}

func TestMultipleCommitsAdvanceSequence(t *testing.T) {
	ob := New()
	req1 := validRequest()
	r1, err := ob.Commit(req1)
	if err != nil {
		t.Fatalf("commit 1: %v", err)
	}

	req2 := Request{
		IdempotencyKey: "key-2",
		RequestDigest:  testRequestDigest2,
		Kind:           engine.CommandKindSignal,
		FactDigest:     testFactDigest2,
	}
	r2, err := ob.Commit(req2)
	if err != nil {
		t.Fatalf("commit 2: %v", err)
	}

	if r1.Sequence != 1 {
		t.Errorf("first sequence: %d", r1.Sequence)
	}
	if r2.Sequence != 2 {
		t.Errorf("second sequence: %d", r2.Sequence)
	}
	if ob.LedgerSequence() != 2 {
		t.Errorf("ledger sequence: %d", ob.LedgerSequence())
	}
	if ob.FactCount() != 2 {
		t.Errorf("fact count: %d", ob.FactCount())
	}
	if ob.EntryCount() != 2 {
		t.Errorf("entry count: %d", ob.EntryCount())
	}
}

func TestCrashAtCommitPreservesPriorState(t *testing.T) {
	ob := New()
	req1 := validRequest()
	_, err := ob.Commit(req1)
	if err != nil {
		t.Fatalf("first commit: %v", err)
	}

	obWithCrash := New(WithCrashPoint(CrashPointCommit))
	req2 := Request{
		IdempotencyKey: "key-2",
		RequestDigest:  testRequestDigest2,
		Kind:           engine.CommandKindSignal,
		FactDigest:     testFactDigest2,
	}
	_, err = obWithCrash.Commit(req2)
	if !errors.Is(err, ErrCrashInjected) {
		t.Fatalf("want ErrCrashInjected, got %v", err)
	}

	if obWithCrash.LedgerSequence() != 0 {
		t.Errorf("sequence should be 0, got %d", obWithCrash.LedgerSequence())
	}
	if obWithCrash.EntryCount() != 0 {
		t.Errorf("entries should be 0, got %d", obWithCrash.EntryCount())
	}
}

func TestFullPipeline(t *testing.T) {
	ob := New()
	req := validRequest()

	r, err := ob.Commit(req)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	fresh, err := ob.Dispatch(r.CommandId)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !fresh {
		t.Error("dispatch should be fresh")
	}

	replay, err := ob.RecordResult(r.CommandId, testResultDigest)
	if err != nil {
		t.Fatalf("result: %v", err)
	}
	if replay {
		t.Error("first result should not be replay")
	}

	report, err := ob.Recover()
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if report.Sequence != 1 || report.FactCount != 1 || report.EntryCount != 1 ||
		report.DispatchedCount != 1 || report.ResultCount != 1 {
		t.Errorf("unexpected report: %+v", report)
	}
	if len(report.PendingDispatch) != 0 || len(report.PendingResult) != 0 {
		t.Errorf("unexpected pending: dispatch=%v result=%v",
			report.PendingDispatch, report.PendingResult)
	}
}

func TestErrorPrefixes(t *testing.T) {
	ob := New()
	_, err := ob.Commit(Request{})
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if len(msg) < 7 || msg[:7] != "outbox:" {
		t.Errorf("error missing outbox: prefix: %s", msg)
	}
}

func TestWithCrashPointOption(t *testing.T) {
	for _, cp := range []CrashPoint{CrashPointCommit, CrashPointDispatch, CrashPointResult} {
		t.Run(string(cp), func(t *testing.T) {
			ob := New(WithCrashPoint(cp))
			if ob.CrashPointConfig() != cp {
				t.Errorf("want %s, got %s", cp, ob.CrashPointConfig())
			}
		})
	}
}

func TestCrashMatrixSummary(t *testing.T) {
	type counts struct {
		seq   int64
		facts int
		ents  int
		disp  int
		res   int
	}

	matrix := []struct {
		window    CrashPoint
		crash     bool
		replay    bool
		wantFirst counts
		wantFinal counts
	}{
		{CrashPointCommit, false, false, counts{1, 1, 1, 0, 0}, counts{1, 1, 1, 0, 0}},
		{CrashPointCommit, false, true, counts{1, 1, 1, 0, 0}, counts{1, 1, 1, 0, 0}},
		{CrashPointCommit, true, false, counts{0, 0, 0, 0, 0}, counts{0, 0, 0, 0, 0}},
		{CrashPointCommit, true, true, counts{0, 0, 0, 0, 0}, counts{1, 1, 1, 0, 0}},
		{CrashPointDispatch, false, false, counts{1, 1, 1, 1, 0}, counts{1, 1, 1, 1, 0}},
		{CrashPointDispatch, false, true, counts{1, 1, 1, 1, 0}, counts{1, 1, 1, 1, 0}},
		{CrashPointDispatch, true, false, counts{1, 1, 1, 0, 0}, counts{1, 1, 1, 0, 0}},
		{CrashPointDispatch, true, true, counts{1, 1, 1, 0, 0}, counts{1, 1, 1, 1, 0}},
		{CrashPointResult, false, false, counts{1, 1, 1, 1, 1}, counts{1, 1, 1, 1, 1}},
		{CrashPointResult, false, true, counts{1, 1, 1, 1, 1}, counts{1, 1, 1, 1, 1}},
		{CrashPointResult, true, false, counts{1, 1, 1, 1, 0}, counts{1, 1, 1, 1, 0}},
		{CrashPointResult, true, true, counts{1, 1, 1, 1, 0}, counts{1, 1, 1, 1, 1}},
	}

	for _, m := range matrix {
		label := fmt.Sprintf("%s_crash=%v_replay=%v", m.window, m.crash, m.replay)
		t.Run(label, func(t *testing.T) {
			ob := New()
			req := validRequest()
			var commandId string

			if m.crash {
				switch m.window {
				case CrashPointCommit:
					ob.setCrashPoint(CrashPointCommit)
					_, err := ob.Commit(req)
					if !errors.Is(err, ErrCrashInjected) {
						t.Fatalf("want crash at commit, got %v", err)
					}
				case CrashPointDispatch:
					r, err := ob.Commit(req)
					if err != nil {
						t.Fatalf("commit: %v", err)
					}
					commandId = r.CommandId
					ob.setCrashPoint(CrashPointDispatch)
					_, err = ob.Dispatch(commandId)
					if !errors.Is(err, ErrCrashInjected) {
						t.Fatalf("want crash at dispatch, got %v", err)
					}
				case CrashPointResult:
					r, err := ob.Commit(req)
					if err != nil {
						t.Fatalf("commit: %v", err)
					}
					commandId = r.CommandId
					_, err = ob.Dispatch(commandId)
					if err != nil {
						t.Fatalf("dispatch: %v", err)
					}
					ob.setCrashPoint(CrashPointResult)
					_, err = ob.RecordResult(commandId, testResultDigest)
					if !errors.Is(err, ErrCrashInjected) {
						t.Fatalf("want crash at result, got %v", err)
					}
				}
			} else {
				r, err := ob.Commit(req)
				if err != nil {
					t.Fatalf("commit: %v", err)
				}
				commandId = r.CommandId
				if m.window == CrashPointDispatch || m.window == CrashPointResult {
					_, err = ob.Dispatch(commandId)
					if err != nil {
						t.Fatalf("dispatch: %v", err)
					}
				}
				if m.window == CrashPointResult {
					_, err = ob.RecordResult(commandId, testResultDigest)
					if err != nil {
						t.Fatalf("result: %v", err)
					}
				}
			}

			if m.replay && !m.crash {
				ob.Commit(req)
				if m.window == CrashPointDispatch || m.window == CrashPointResult {
					ob.Dispatch(commandId)
				}
				if m.window == CrashPointResult {
					ob.RecordResult(commandId, testResultDigest)
				}
			}

			if m.replay && m.crash {
				ob.setCrashPoint("")
				r, err := ob.Commit(req)
				if err != nil {
					t.Fatalf("replay commit: %v", err)
				}
				commandId = r.CommandId
				if m.window == CrashPointDispatch || m.window == CrashPointResult {
					_, err = ob.Dispatch(commandId)
					if err != nil {
						t.Fatalf("replay dispatch: %v", err)
					}
				}
				if m.window == CrashPointResult {
					_, err = ob.RecordResult(commandId, testResultDigest)
					if err != nil {
						t.Fatalf("replay result: %v", err)
					}
				}
			}

			report, err := ob.Recover()
			if err != nil {
				t.Fatalf("Recover: %v", err)
			}
			want := m.wantFinal
			if report.Sequence != want.seq {
				t.Errorf("sequence: want %d got %d", want.seq, report.Sequence)
			}
			if report.FactCount != want.facts {
				t.Errorf("facts: want %d got %d", want.facts, report.FactCount)
			}
			if report.EntryCount != want.ents {
				t.Errorf("entries: want %d got %d", want.ents, report.EntryCount)
			}
			if report.DispatchedCount != want.disp {
				t.Errorf("dispatched: want %d got %d", want.disp, report.DispatchedCount)
			}
			if report.ResultCount != want.res {
				t.Errorf("results: want %d got %d", want.res, report.ResultCount)
			}
		})
	}
}
