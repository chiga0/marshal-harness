package cutovereq

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

var (
	// ErrMalformedStep 拒绝形态非法的 NormalizedStep：必需业务身份字段
	// 为空（taskId/runId/attemptId/command.kind）、sequence 为 0、
	// generation 为负、已出示的 digest 形态非法、digest 键在封闭集之外、
	// drcId/drcBinding 不互恰。
	ErrMalformedStep = errors.New("malformed normalized step")
	// ErrTraceMisaligned 拒绝无法按 sequence 逐对配齐的 old/new trace：
	// 步数不等，或同一索引位上的 sequence 不同。
	ErrTraceMisaligned = errors.New("trace misaligned")
	// ErrFakeDrift 拒绝 deterministic Fake 路径上任何不在授予升级集之内
	// 的差异（digests 全等 + 业务字段全等之外的漂移）。
	ErrFakeDrift = errors.New("fake deterministic drift")
	// ErrAuthorityRegression 拒绝 authority 相关资源计数（attempt/gate/
	// review）的任何变化——资源归一化不劣化的权威面必须严格相等。
	ErrAuthorityRegression = errors.New("authority count regression")
	// ErrInvalidTolerance 拒绝越界的 basis-point 容差（必须落在
	// [0, 10000]）。
	ErrInvalidTolerance = errors.New("invalid tolerance")
	// ErrResourceRegression 拒绝统计面的劣化：峰值内存或墙钟超过
	// old × (10000+toleranceBP)/10000，或 old 为 0 而 new 为正。
	ErrResourceRegression = errors.New("statistical resource regression")
)

// DiffClass 是 trace diff 分类的封闭枚举。
type DiffClass string

const (
	// ClassAuthorityUpgrade 是允许且必须解释的升级：白名单字段从
	// null（old 侧空）变为非空（new 侧），且升级值通过形态校验。
	ClassAuthorityUpgrade DiffClass = "authority-upgrade"
	// ClassBusinessMismatch 阻断 cutover：业务身份不变量或 digest
	// 完整性被破坏（含升级值形态校验失败）。
	ClassBusinessMismatch DiffClass = "business-mismatch"
	// ClassUnexplainedDrift 阻断 cutover：任何落在不变量集与授予升级
	// 集之外的字段差异——人工解释不了 authority diff。
	ClassUnexplainedDrift DiffClass = "unexplained-drift"
)

// CommandRef 是 normalized trace 中 command 的投影：CommandID 为空表示
// old 路径的 null（无 durable command 账本）。
type CommandRef struct {
	Kind      string
	Origin    string
	CommandID string // 空 = old 路径 null
}

// RegistrationRef 是一条 registration 的投影。digest 字段在 old 路径可
// 为空；非空时必须是 sha256:<64-小写hex> 形态。
type RegistrationRef struct {
	ProviderID        string
	RegistrationID    string
	CapabilityDigest  string // 空允许；非空必须 digest 形态
	AttestationDigest string // 空允许；非空必须 digest 形态
}

// DrcBinding 是 ADR 0018 DispatchResultCapability 冻结绑定集合
// （attemptId/allocationId/leaseId/generation）的投影。
type DrcBinding struct {
	AttemptID    string
	AllocationID string
	LeaseID      string
	Generation   int64
}

// ResultCapabilityRef 是 resultCapability 的投影：DrcID 为空表示 old
// 路径的 null（结果不经 DRC-bound ingress）；非空时必须 digest 形态且
// DrcBinding 非空。
type ResultCapabilityRef struct {
	DrcID      string // 空 = old 路径 null；非空必须 digest 形态
	DrcBinding *DrcBinding
}

// NormalizedStep 是 golden-trace 文档 §3/§4 normalized trace schema 的
// Go 投影：业务身份字段（TaskID/RunID/AttemptID/Sequence）必需；lease、
// allocation、registration、resultCapability 在 old 路径可为空形态。
type NormalizedStep struct {
	TaskID            string
	RunID             string
	AttemptID         string
	Sequence          uint64 // >0
	Command           CommandRef
	LeaseGeneration   int64  // >=0
	LeaseFencingToken string // old 路径可为空
	SandboxProvider   string // old 路径可为空
	AllocationID      string // old 路径可为空
	Agent             RegistrationRef
	Sandbox           RegistrationRef
	ResultCapability  ResultCapabilityRef
	// Digests 键集封闭：spec/diff/verification/decision；存在的值必须是
	// digest 形态。
	Digests map[string]string
}

// knownDigestKeys 是 Digests 的封闭键集（golden-trace §3/§4）。
var knownDigestKeys = map[string]struct{}{
	"spec":         {},
	"diff":         {},
	"verification": {},
	"decision":     {},
}

// Validate 对 NormalizedStep 形态校验，fail closed：必需业务身份字段
// （taskId/runId/attemptId/command.kind）为空、sequence 为 0、
// generation 为负、已出示 digest 形态非法、digest 键越出封闭集、
// drcId/drcBinding 不互恰，均返回 ErrMalformedStep。
func (s NormalizedStep) Validate() error {
	if s.TaskID == "" {
		return fmt.Errorf("cutovereq: %w: taskId must not be empty", ErrMalformedStep)
	}
	if s.RunID == "" {
		return fmt.Errorf("cutovereq: %w: runId must not be empty", ErrMalformedStep)
	}
	if s.AttemptID == "" {
		return fmt.Errorf("cutovereq: %w: attemptId must not be empty", ErrMalformedStep)
	}
	if s.Command.Kind == "" {
		return fmt.Errorf("cutovereq: %w: command.kind must not be empty", ErrMalformedStep)
	}
	if s.Sequence == 0 {
		return fmt.Errorf("cutovereq: %w: sequence must be positive", ErrMalformedStep)
	}
	if s.LeaseGeneration < 0 {
		return fmt.Errorf("cutovereq: %w: lease.generation must not be negative, got %d",
			ErrMalformedStep, s.LeaseGeneration)
	}
	if err := validateRegistration("agentRegistration", s.Agent); err != nil {
		return err
	}
	if err := validateRegistration("sandboxRegistration", s.Sandbox); err != nil {
		return err
	}
	if s.ResultCapability.DrcID != "" {
		if reason := requireDigest(s.ResultCapability.DrcID); reason != nil {
			return fmt.Errorf("cutovereq: %w: resultCapability.drcId: %v", ErrMalformedStep, reason)
		}
		if s.ResultCapability.DrcBinding == nil {
			return fmt.Errorf("cutovereq: %w: resultCapability.drcId present without drcBinding", ErrMalformedStep)
		}
	}
	if b := s.ResultCapability.DrcBinding; b != nil {
		if s.ResultCapability.DrcID == "" {
			return fmt.Errorf("cutovereq: %w: resultCapability.drcBinding present without drcId", ErrMalformedStep)
		}
		if b.Generation < 0 {
			return fmt.Errorf("cutovereq: %w: resultCapability.drcBinding.generation must not be negative, got %d",
				ErrMalformedStep, b.Generation)
		}
	}
	for key, value := range s.Digests {
		if _, ok := knownDigestKeys[key]; !ok {
			return fmt.Errorf("cutovereq: %w: digests key %q outside the closed set spec/diff/verification/decision",
				ErrMalformedStep, key)
		}
		if reason := requireDigest(value); reason != nil {
			return fmt.Errorf("cutovereq: %w: digests.%s: %v", ErrMalformedStep, key, reason)
		}
	}
	return nil
}

// TraceDiff 是一条逐对比较得出的差异：Sequence 为所属 step 序号，Field
// 使用 dotted schema 路径（如 "command.commandId"、"digests.spec"），
// Detail 携带差异的可读解释（authority-upgrade 的解释义务落在这里）。
type TraceDiff struct {
	Sequence uint64
	Field    string
	Class    DiffClass
	Detail   string
}

// TraceVerdict 是 authority-trace 等价判定。Equivalent 仅当全部 diff 中
// 不存在 business-mismatch 与 unexplained-drift 时为真——verdict 只由
// typed 比较派生，不存在任何人工 override 通道。
type TraceVerdict struct {
	Equivalent bool
	Diffs      []TraceDiff
}

// CompareAuthorityTrace 比较 old/new normalized trace 的 authority
// invariants。两侧 step 先各自 Validate（fail closed）；按 Sequence 逐对
// 配齐，步数不等或同位 sequence 不同即 ErrTraceMisaligned。每对执行：
//
//  1. 业务身份不变量：taskId/runId/attemptId/command.kind 必须相等；
//     digests 中 old 侧出现的键必须在 new 侧出现且相等；违反即
//     business-mismatch；
//  2. 授予升级集（null→非空）：command.commandId、lease.fencingToken、
//     allocation.allocationId、allocation.sandboxProvider、
//     agentRegistration.registrationId、agentRegistration.attestationDigest、
//     sandboxRegistration.registrationId、resultCapability.drcId/
//     drcBinding——记为 authority-upgrade；升级值必须过形态校验
//     （digest 形态；drcBinding 非空且 attemptId==本 step.attemptId、
//     generation==本 step.lease.generation），不满足即 business-mismatch；
//  3. 其余任何字段差异（changed、new 侧清空、在两侧均非空时不同）一律
//     unexplained-drift。
func CompareAuthorityTrace(oldSteps, newSteps []NormalizedStep) (TraceVerdict, error) {
	for i := range oldSteps {
		if err := oldSteps[i].Validate(); err != nil {
			return TraceVerdict{}, fmt.Errorf("cutovereq: old step %d (sequence %d): %w",
				i, oldSteps[i].Sequence, err)
		}
	}
	for i := range newSteps {
		if err := newSteps[i].Validate(); err != nil {
			return TraceVerdict{}, fmt.Errorf("cutovereq: new step %d (sequence %d): %w",
				i, newSteps[i].Sequence, err)
		}
	}
	if len(oldSteps) != len(newSteps) {
		return TraceVerdict{}, fmt.Errorf("cutovereq: %w: old trace carries %d steps, new trace carries %d",
			ErrTraceMisaligned, len(oldSteps), len(newSteps))
	}
	var verdict TraceVerdict
	for i := range oldSteps {
		if oldSteps[i].Sequence != newSteps[i].Sequence {
			return TraceVerdict{}, fmt.Errorf("cutovereq: %w: step %d pairs old sequence %d with new sequence %d",
				ErrTraceMisaligned, i, oldSteps[i].Sequence, newSteps[i].Sequence)
		}
		verdict.Diffs = append(verdict.Diffs, comparePair(oldSteps[i], newSteps[i])...)
	}
	verdict.Equivalent = true
	for _, d := range verdict.Diffs {
		if d.Class == ClassBusinessMismatch || d.Class == ClassUnexplainedDrift {
			verdict.Equivalent = false
			break
		}
	}
	return verdict, nil
}

// comparePair 对一对已 Validate、同 sequence 的 step 执行逐字段冻结比较。
func comparePair(o, n NormalizedStep) []TraceDiff {
	var diffs []TraceDiff
	add := func(field string, class DiffClass, format string, args ...any) {
		diffs = append(diffs, TraceDiff{
			Sequence: o.Sequence,
			Field:    field,
			Class:    class,
			Detail:   fmt.Sprintf(format, args...),
		})
	}

	// 1. 业务身份不变量（违反即 business-mismatch）。
	if o.TaskID != n.TaskID {
		add("taskId", ClassBusinessMismatch, "business identity changed: old %q vs new %q", o.TaskID, n.TaskID)
	}
	if o.RunID != n.RunID {
		add("runId", ClassBusinessMismatch, "business identity changed: old %q vs new %q", o.RunID, n.RunID)
	}
	if o.AttemptID != n.AttemptID {
		add("attemptId", ClassBusinessMismatch, "business identity changed: old %q vs new %q", o.AttemptID, n.AttemptID)
	}
	if o.Command.Kind != n.Command.Kind {
		add("command.kind", ClassBusinessMismatch, "command kind changed: old %q vs new %q", o.Command.Kind, n.Command.Kind)
	}
	for _, key := range sortedKeys(o.Digests) {
		ov := o.Digests[key]
		nv, ok := n.Digests[key]
		switch {
		case !ok:
			add("digests."+key, ClassBusinessMismatch, "old-side digest %q has no new-side entry", ov)
		case nv != ov:
			add("digests."+key, ClassBusinessMismatch, "digest changed: old %q vs new %q", ov, nv)
		}
	}
	for _, key := range sortedKeys(n.Digests) {
		if _, ok := o.Digests[key]; !ok {
			add("digests."+key, ClassUnexplainedDrift,
				"new-side digest %q has no old-side entry (digests are content, not an explainable authority upgrade)", n.Digests[key])
		}
	}

	// 2. 授予升级集（old 空 → new 非空）。
	upgradeField := func(field, ov, nv string, digestShaped bool) {
		switch {
		case ov == nv:
			// 无变化（含两侧皆空），不产生 diff。
		case ov == "":
			if digestShaped {
				if reason := requireDigest(nv); reason != nil {
					add(field, ClassBusinessMismatch, "authority upgrade value malformed: %v", reason)
					return
				}
			}
			add(field, ClassAuthorityUpgrade, "null→%q granted authority upgrade", nv)
		default:
			add(field, ClassUnexplainedDrift,
				"authority field changed outside the granted upgrade set: old %q vs new %q", ov, nv)
		}
	}
	upgradeField("command.commandId", o.Command.CommandID, n.Command.CommandID, false)
	upgradeField("lease.fencingToken", o.LeaseFencingToken, n.LeaseFencingToken, false)
	upgradeField("allocation.allocationId", o.AllocationID, n.AllocationID, false)
	upgradeField("allocation.sandboxProvider", o.SandboxProvider, n.SandboxProvider, false)
	upgradeField("agentRegistration.registrationId", o.Agent.RegistrationID, n.Agent.RegistrationID, false)
	upgradeField("agentRegistration.attestationDigest", o.Agent.AttestationDigest, n.Agent.AttestationDigest, true)
	upgradeField("sandboxRegistration.registrationId", o.Sandbox.RegistrationID, n.Sandbox.RegistrationID, false)
	upgradeField("resultCapability.drcId", o.ResultCapability.DrcID, n.ResultCapability.DrcID, true)

	// drcBinding：nil→非空属于 DRC 升级的一部分，必须绑定本 step。
	ob, nb := o.ResultCapability.DrcBinding, n.ResultCapability.DrcBinding
	switch {
	case ob == nil && nb == nil:
	case ob == nil:
		ok := true
		if nb.AttemptID != n.AttemptID {
			add("resultCapability.drcBinding", ClassBusinessMismatch,
				"drcBinding.attemptId %q must equal the carrying step's attemptId %q", nb.AttemptID, n.AttemptID)
			ok = false
		}
		if nb.Generation != n.LeaseGeneration {
			add("resultCapability.drcBinding", ClassBusinessMismatch,
				"drcBinding.generation %d must equal the carrying step's lease.generation %d", nb.Generation, n.LeaseGeneration)
			ok = false
		}
		if ok {
			add("resultCapability.drcBinding", ClassAuthorityUpgrade,
				"null→binding{attemptId:%q allocationId:%q leaseId:%q generation:%d} granted authority upgrade",
				nb.AttemptID, nb.AllocationID, nb.LeaseID, nb.Generation)
		}
	case nb == nil:
		add("resultCapability.drcBinding", ClassUnexplainedDrift, "new-side emptied a present drcBinding")
	default:
		if *ob != *nb {
			add("resultCapability.drcBinding", ClassUnexplainedDrift,
				"drcBinding changed outside the granted upgrade set: old %+v vs new %+v", *ob, *nb)
		}
	}
	if o.ResultCapability.DrcID == "" && n.ResultCapability.DrcID != "" && nb == nil {
		add("resultCapability.drcBinding", ClassBusinessMismatch, "drcId upgraded without drcBinding")
	}

	// 3. 其余字段：任何差异即 unexplained-drift。
	strictField := func(field, ov, nv string) {
		if ov != nv {
			add(field, ClassUnexplainedDrift,
				"field outside the invariant/upgrade sets changed: old %q vs new %q", ov, nv)
		}
	}
	strictField("command.origin", o.Command.Origin, n.Command.Origin)
	strictField("agentRegistration.providerId", o.Agent.ProviderID, n.Agent.ProviderID)
	strictField("agentRegistration.capabilityDigest", o.Agent.CapabilityDigest, n.Agent.CapabilityDigest)
	strictField("sandboxRegistration.providerId", o.Sandbox.ProviderID, n.Sandbox.ProviderID)
	strictField("sandboxRegistration.capabilityDigest", o.Sandbox.CapabilityDigest, n.Sandbox.CapabilityDigest)
	strictField("sandboxRegistration.attestationDigest", o.Sandbox.AttestationDigest, n.Sandbox.AttestationDigest)
	if o.LeaseGeneration != n.LeaseGeneration {
		add("lease.generation", ClassUnexplainedDrift,
			"lease.generation changed outside the granted upgrade set: old %d vs new %d", o.LeaseGeneration, n.LeaseGeneration)
	}

	return diffs
}

// CompareFakeDeterministic 是 deterministic-Fake 路径的成对比较：Digests
// map 全等（键集与值逐条相等），业务字段（taskId/runId/attemptId/
// command.kind/sequence）全等，authority 字段遵循与
// CompareAuthorityTrace 相同的升级集规则；任何 business-mismatch 或
// unexplained-drift 即 ErrFakeDrift，fail closed。
func CompareFakeDeterministic(oldStep, newStep NormalizedStep) error {
	if err := oldStep.Validate(); err != nil {
		return fmt.Errorf("cutovereq: old step: %w", err)
	}
	if err := newStep.Validate(); err != nil {
		return fmt.Errorf("cutovereq: new step: %w", err)
	}
	if oldStep.Sequence != newStep.Sequence {
		return fmt.Errorf("cutovereq: %w: sequence %d vs %d",
			ErrFakeDrift, oldStep.Sequence, newStep.Sequence)
	}
	for _, d := range comparePair(oldStep, newStep) {
		if d.Class != ClassAuthorityUpgrade {
			return fmt.Errorf("cutovereq: %w: sequence %d field %s: %s",
				ErrFakeDrift, d.Sequence, d.Field, d.Detail)
		}
	}
	return nil
}

// ResourceStat 是真实 Agent 资源归一化统计：AttemptCount/GateRuns/
// ReviewRounds 是 authority 面（必须严格相等）；PeakMemoryBytes 与
// WallMillis 是统计面（容差内不劣化）。
type ResourceStat struct {
	AttemptCount    int
	GateRuns        int
	ReviewRounds    int
	PeakMemoryBytes int64
	WallMillis      int64
}

// ResourceVerdict 是资源比较结论：authority 三项与统计两项各自的通过
// 标志，外加全部劣化的可读条目。
type ResourceVerdict struct {
	AttemptsOK  bool
	GatesOK     bool
	ReviewsOK   bool
	MemoryOK    bool
	WallOK      bool
	Regressions []string
}

// CompareResource 执行资源归一化不劣化判定：
//
//  1. toleranceBP 必须落在 [0,10000]（basis points），否则
//     ErrInvalidTolerance；
//  2. AttemptCount/GateRuns/ReviewRounds 必须严格相等，否则
//     ErrAuthorityRegression（记录劣化的计数轴）；
//  3. PeakMemoryBytes/WallMillis 不得超过
//     old × (10000+toleranceBP)/10000；old==0 时 new 必须为 0；否则
//     ErrResourceRegression。
//
// verdict 始终完整填充（含 Regressions），error 为首个阻断类别。
func CompareResource(oldStat, newStat ResourceStat, toleranceBP int) (ResourceVerdict, error) {
	if toleranceBP < 0 || toleranceBP > 10000 {
		return ResourceVerdict{}, fmt.Errorf("cutovereq: %w: %d (must be within [0,10000] basis points)",
			ErrInvalidTolerance, toleranceBP)
	}
	verdict := ResourceVerdict{
		AttemptsOK: oldStat.AttemptCount == newStat.AttemptCount,
		GatesOK:    oldStat.GateRuns == newStat.GateRuns,
		ReviewsOK:  oldStat.ReviewRounds == newStat.ReviewRounds,
		MemoryOK:   withinLimit(oldStat.PeakMemoryBytes, newStat.PeakMemoryBytes, toleranceBP),
		WallOK:     withinLimit(oldStat.WallMillis, newStat.WallMillis, toleranceBP),
	}
	if !verdict.AttemptsOK {
		verdict.Regressions = append(verdict.Regressions,
			fmt.Sprintf("attemptCount %d→%d", oldStat.AttemptCount, newStat.AttemptCount))
	}
	if !verdict.GatesOK {
		verdict.Regressions = append(verdict.Regressions,
			fmt.Sprintf("gateRuns %d→%d", oldStat.GateRuns, newStat.GateRuns))
	}
	if !verdict.ReviewsOK {
		verdict.Regressions = append(verdict.Regressions,
			fmt.Sprintf("reviewRounds %d→%d", oldStat.ReviewRounds, newStat.ReviewRounds))
	}
	if len(verdict.Regressions) > 0 {
		return verdict, fmt.Errorf("cutovereq: %w: %s",
			ErrAuthorityRegression, strings.Join(verdict.Regressions, "; "))
	}
	var statRegs []string
	if !verdict.MemoryOK {
		statRegs = append(statRegs, fmt.Sprintf("peakMemoryBytes %d→%d exceeds limit %d (tolerance %dBP)",
			oldStat.PeakMemoryBytes, newStat.PeakMemoryBytes, statLimit(oldStat.PeakMemoryBytes, toleranceBP), toleranceBP))
	}
	if !verdict.WallOK {
		statRegs = append(statRegs, fmt.Sprintf("wallMillis %d→%d exceeds limit %d (tolerance %dBP)",
			oldStat.WallMillis, newStat.WallMillis, statLimit(oldStat.WallMillis, toleranceBP), toleranceBP))
	}
	verdict.Regressions = append(verdict.Regressions, statRegs...)
	if len(statRegs) > 0 {
		return verdict, fmt.Errorf("cutovereq: %w: %s",
			ErrResourceRegression, strings.Join(statRegs, "; "))
	}
	return verdict, nil
}

// ── helpers ─────────────────────────────────────────────────────────────────

// statLimit 返回 old 基线在 toleranceBP（basis points）下的上限；old==0
// 时只允许 new==0（返回 0）。含 int64 溢出保护。
func statLimit(old int64, toleranceBP int) int64 {
	if old == 0 {
		return 0
	}
	factor := int64(10000 + toleranceBP)
	if old > math.MaxInt64/factor {
		return math.MaxInt64
	}
	return old * factor / 10000
}

// withinLimit 判定 new 是否落在 statLimit(old, toleranceBP) 之内；
// old==0 && new==0 视为通过，old==0 && new>0 视为劣化。
func withinLimit(old, new int64, toleranceBP int) bool {
	return new <= statLimit(old, toleranceBP)
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func validateRegistration(name string, r RegistrationRef) error {
	if r.CapabilityDigest != "" {
		if reason := requireDigest(r.CapabilityDigest); reason != nil {
			return fmt.Errorf("cutovereq: %w: %s.capabilityDigest: %v", ErrMalformedStep, name, reason)
		}
	}
	if r.AttestationDigest != "" {
		if reason := requireDigest(r.AttestationDigest); reason != nil {
			return fmt.Errorf("cutovereq: %w: %s.attestationDigest: %v", ErrMalformedStep, name, reason)
		}
	}
	return nil
}

// requireDigest 校验 sha256:<64-hex-lowercase> 形态，返回纯原因错误，
// 由调用方包装 sentinel 与前缀。
func requireDigest(value string) error {
	const prefix = "sha256:"
	if strings.TrimSpace(value) == "" {
		return errors.New("digest must not be empty")
	}
	if !strings.HasPrefix(value, prefix) {
		return fmt.Errorf("digest must carry the %q prefix", prefix)
	}
	hexPart := strings.TrimPrefix(value, prefix)
	if len(hexPart) != 64 {
		return fmt.Errorf("digest must be a 64-character sha256 hex digest, got %d", len(hexPart))
	}
	for _, c := range hexPart {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return errors.New("digest must be lowercase hex")
		}
	}
	return nil
}
