package coretest

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/events"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/ports"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/policy"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/task"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/consts"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

// Epoch 是替身默认使用的时间。
var Epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func stamp(c ports.Clock) time.Time {
	if c == nil {
		return Epoch
	}
	return c.Now()
}

// ---- PolicySource ----

// FakePolicySource 返回设定的策略集（克隆）。方法名：Load。
type FakePolicySource struct {
	Faults
	mu  sync.Mutex
	set policy.Set
}

var _ ports.PolicySource = (*FakePolicySource)(nil)

func NewFakePolicySource(set policy.Set) *FakePolicySource {
	return &FakePolicySource{set: set.Clone()}
}

// Set 换成新的策略集。
func (p *FakePolicySource) Set(set policy.Set) { p.mu.Lock(); p.set = set.Clone(); p.mu.Unlock() }

func (p *FakePolicySource) Load(ctx context.Context) (policy.Set, error) {
	if err := p.begin(ctx, "Load"); err != nil {
		return policy.Set{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.set.Clone(), nil
}

// ---- Registry ----

type rendered struct {
	c request.Content
	e request.Expectation
}

// FakeRegistry 是登记表替身。方法名：Unit、RenderStep。
// RenderStep 默认按 SetStep 设定的步骤 ID 返回内容和预期；设置 RenderFn 可以完全自定义。
type FakeRegistry struct {
	Faults
	// RenderFn 非空时替代默认的 RenderStep 行为。
	RenderFn func(ctx context.Context, def *task.Definition, step task.Step, params map[string]any, outputs map[string]map[string]any) (request.Content, request.Expectation, error)

	mu    sync.Mutex
	units map[string]ports.UnitInfo
	steps map[string]rendered
}

var _ ports.Registry = (*FakeRegistry)(nil)

func NewFakeRegistry() *FakeRegistry {
	return &FakeRegistry{units: map[string]ports.UnitInfo{}, steps: map[string]rendered{}}
}

func unitKey(id string, version int64) string { return fmt.Sprintf("%s@%d", id, version) }

// AddUnit 登记一个单元。
func (r *FakeRegistry) AddUnit(info ports.UnitInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.units[unitKey(info.Ref.ID, info.Ref.Version)] = info
}

// SetStep 设定某个步骤 ID 的 RenderStep 结果。
func (r *FakeRegistry) SetStep(stepID string, c request.Content, e request.Expectation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steps[stepID] = rendered{c.Clone(), e.Clone()}
}

func (r *FakeRegistry) Unit(ctx context.Context, id string, version int64) (ports.UnitInfo, error) {
	if err := r.begin(ctx, "Unit"); err != nil {
		return ports.UnitInfo{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.units[unitKey(id, version)]
	if !ok {
		return ports.UnitInfo{}, errcode.Newf(errcode.NotFound, "unit %s version %d", id, version)
	}
	return u, nil
}

func (r *FakeRegistry) RenderStep(ctx context.Context, def *task.Definition, step task.Step, params map[string]any, outputs map[string]map[string]any) (request.Content, request.Expectation, error) {
	if err := r.begin(ctx, "RenderStep"); err != nil {
		return request.Content{}, request.Expectation{}, err
	}
	if r.RenderFn != nil {
		return r.RenderFn(ctx, def, step, params, outputs)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out, ok := r.steps[step.ID]
	if !ok {
		return request.Content{}, request.Expectation{}, errcode.Newf(errcode.NotFound, "no rendering set for step %s", step.ID)
	}
	return out.c.Clone(), out.e.Clone(), nil
}

// ---- ScopeSource ----

// FakeScopeSource 是会话与范围替身。方法名：Session、OutsideScope。
// 没有登记过的项目和环境组合里，所有目标都算范围外。
type FakeScopeSource struct {
	Faults
	mu       sync.Mutex
	sessions map[string]ports.SessionInfo
	targets  map[string]map[string]bool
}

var _ ports.ScopeSource = (*FakeScopeSource)(nil)

func NewFakeScopeSource() *FakeScopeSource {
	return &FakeScopeSource{sessions: map[string]ports.SessionInfo{}, targets: map[string]map[string]bool{}}
}

func (s *FakeScopeSource) AddSession(id string, info ports.SessionInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[id] = info
}

// AddTargets 登记属于该项目和环境的目标。
func (s *FakeScopeSource) AddTargets(project, env string, targets ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := project + "/" + env
	if s.targets[k] == nil {
		s.targets[k] = map[string]bool{}
	}
	for _, t := range targets {
		s.targets[k][t] = true
	}
}

func (s *FakeScopeSource) Session(ctx context.Context, sessionID string) (ports.SessionInfo, error) {
	if err := s.begin(ctx, "Session"); err != nil {
		return ports.SessionInfo{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	info, ok := s.sessions[sessionID]
	if !ok {
		return ports.SessionInfo{}, errcode.Newf(errcode.NotFound, "session %s", sessionID)
	}
	return info, nil
}

func (s *FakeScopeSource) OutsideScope(ctx context.Context, project, env string, targets []string) ([]string, error) {
	if err := s.begin(ctx, "OutsideScope"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	in := s.targets[project+"/"+env]
	var out []string
	for _, t := range targets {
		if !in[t] {
			out = append(out, t)
		}
	}
	return out, nil
}

// ---- SecretKeys ----

// FakeSecretKeys 是凭证库 K 查询替身，只记 K 名，没有 V。方法名：Missing。
// 没有登记过的项目和环境组合里，所有 K 都算缺失。
type FakeSecretKeys struct {
	Faults
	mu   sync.Mutex
	keys map[string]map[string]bool // "项目/环境" -> K 集合
}

var _ ports.SecretKeys = (*FakeSecretKeys)(nil)

func NewFakeSecretKeys() *FakeSecretKeys {
	return &FakeSecretKeys{keys: map[string]map[string]bool{}}
}

// AddKeys 登记属于该项目和环境的 K。
func (s *FakeSecretKeys) AddKeys(project, env string, keys ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := project + "/" + env
	if s.keys[k] == nil {
		s.keys[k] = map[string]bool{}
	}
	for _, key := range keys {
		s.keys[k][key] = true
	}
}

// RemoveKeys 撤销已登记的 K。
func (s *FakeSecretKeys) RemoveKeys(project, env string, keys ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, key := range keys {
		delete(s.keys[project+"/"+env], key)
	}
}

// Calls 返回 Missing 的调用次数。
func (s *FakeSecretKeys) Calls() int { return s.CallCount("Missing") }

func (s *FakeSecretKeys) Missing(ctx context.Context, project, env string, keys []string) ([]string, error) {
	if err := s.begin(ctx, "Missing"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	have := s.keys[project+"/"+env]
	var out []string
	seen := map[string]bool{}
	for _, key := range keys {
		if !have[key] && !seen[key] {
			out = append(out, key)
		}
		seen[key] = true
	}
	return out, nil
}

// ---- PreviewPort ----

// FakePreview 是原生预演替身，默认返回 passed。方法名：Preview。
// 用 Push 排队脚本化结果，取完后返回默认值。
type FakePreview struct {
	Faults
	// Clock 可选，用来给默认结果打时间。
	Clock ports.Clock
	q     queue[request.PreviewResult]
}

var _ ports.PreviewPort = (*FakePreview)(nil)

func NewFakePreview() *FakePreview {
	p := &FakePreview{}
	p.q.def = request.PreviewResult{Status: consts.PreviewPassed, Output: "preview ok"}
	return p
}

// Push 排队一个结果（或错误）。
func (p *FakePreview) Push(res request.PreviewResult, err error) { p.q.push(res, err) }

// SetDefault 设定队列取完后的默认结果。
func (p *FakePreview) SetDefault(res request.PreviewResult) { p.q.setDefault(res) }

// Calls 返回 Preview 的调用次数。
func (p *FakePreview) Calls() int { return p.CallCount("Preview") }

func (p *FakePreview) Preview(ctx context.Context, r *request.ExecutionRequest) (request.PreviewResult, error) {
	if err := p.begin(ctx, "Preview"); err != nil {
		return request.PreviewResult{}, err
	}
	res, err := p.q.next()
	if err != nil {
		return request.PreviewResult{}, err
	}
	if res.At.IsZero() {
		res.At = stamp(p.Clock)
	}
	return res, nil
}

// ---- FingerprintPort ----

// FakeFingerprint 是指纹采集替身。方法名：Collect。
// 默认每次返回同一个已采集的指纹；用 PushValue 排队不同的值，例如检验时一个值、执行前另一个值。
type FakeFingerprint struct {
	Faults
	Clock ports.Clock
	q     queue[request.Fingerprint]
}

var _ ports.FingerprintPort = (*FakeFingerprint)(nil)

// DefaultFingerprintValue 是默认指纹值。
const DefaultFingerprintValue = "fp-default"

func NewFakeFingerprint() *FakeFingerprint {
	f := &FakeFingerprint{}
	f.q.def = request.Fingerprint{Method: "fake", MethodVersion: "v1", Status: consts.FingerprintCollected, Value: DefaultFingerprintValue}
	return f
}

// Push 排队一个结果（或错误）。
func (f *FakeFingerprint) Push(fp request.Fingerprint, err error) { f.q.push(fp, err) }

// PushValue 排队一个已采集的指纹值。
func (f *FakeFingerprint) PushValue(v string) {
	f.q.push(request.Fingerprint{Method: "fake", MethodVersion: "v1", Status: consts.FingerprintCollected, Value: v}, nil)
}

// PushUnavailable 排队一次"采集不到"。
func (f *FakeFingerprint) PushUnavailable() {
	f.q.push(request.Fingerprint{Method: "fake", MethodVersion: "v1", Status: consts.FingerprintUnavailable}, nil)
}

// SetDefault 设定队列取完后的默认结果。
func (f *FakeFingerprint) SetDefault(fp request.Fingerprint) { f.q.setDefault(fp) }

func (f *FakeFingerprint) Calls() int { return f.CallCount("Collect") }

func (f *FakeFingerprint) Collect(ctx context.Context, r *request.ExecutionRequest) (request.Fingerprint, error) {
	if err := f.begin(ctx, "Collect"); err != nil {
		return request.Fingerprint{}, err
	}
	fp, err := f.q.next()
	if err != nil {
		return request.Fingerprint{}, err
	}
	if fp.CollectedAt.IsZero() {
		fp.CollectedAt = stamp(f.Clock)
	}
	return fp, nil
}

// ---- DCGPort ----

// FakeDCG 是 dcg 替身，默认放行。方法名：Check。注入错误表示"不可用"。
type FakeDCG struct {
	Faults
	Clock ports.Clock
	q     queue[request.DCGCheck]
}

var _ ports.DCGPort = (*FakeDCG)(nil)

func NewFakeDCG() *FakeDCG {
	d := &FakeDCG{}
	d.q.def = request.DCGCheck{Result: consts.DCGPass, RulesetVersion: "fake-1"}
	return d
}

// Push 排队一个结果（或错误）。
func (d *FakeDCG) Push(res request.DCGCheck, err error) { d.q.push(cloneDCG(res), err) }

// PushResult 排队一个指定结论，可带命中的规则 ID。
func (d *FakeDCG) PushResult(result consts.DCGResult, ruleIDs ...string) {
	d.q.push(cloneDCG(request.DCGCheck{Result: result, RuleIDs: ruleIDs, RulesetVersion: "fake-1"}), nil)
}

// SetDefault 设定队列取完后的默认结果。
func (d *FakeDCG) SetDefault(res request.DCGCheck) { d.q.setDefault(cloneDCG(res)) }

func (d *FakeDCG) Calls() int { return d.CallCount("Check") }

func (d *FakeDCG) Check(ctx context.Context, r *request.ExecutionRequest) (request.DCGCheck, error) {
	if err := d.begin(ctx, "Check"); err != nil {
		return request.DCGCheck{}, err
	}
	res, err := d.q.next()
	if err != nil {
		return request.DCGCheck{}, err
	}
	res = cloneDCG(res) // 队列里的结果每次调用都返回副本，调用方改动不影响后面的调用
	if res.At.IsZero() {
		res.At = stamp(d.Clock)
	}
	return res, nil
}

func cloneDCG(c request.DCGCheck) request.DCGCheck {
	c.RuleIDs = slices.Clone(c.RuleIDs)
	return c
}

// ---- ExecutorPort ----

// FakeExecutor 是执行器替身，默认返回退出码 0 和空输出。方法名：Execute。
// Calls() 是执行次数，"目标变化时执行端口调用次数为 0"这类断言用它。
type FakeExecutor struct {
	Faults
	Clock ports.Clock
	q     queue[request.ExecutionResult]
	mu    sync.Mutex
	reqs  []*request.ExecutionRequest
}

var _ ports.ExecutorPort = (*FakeExecutor)(nil)

func NewFakeExecutor() *FakeExecutor {
	zero := 0
	e := &FakeExecutor{}
	e.q.def = request.ExecutionResult{ExitCode: &zero, Output: map[string]any{}}
	return e
}

// Push 排队一个结果（或错误）。
func (e *FakeExecutor) Push(res request.ExecutionResult, err error) { e.q.push(res, err) }

// PushOutput 排队一次成功执行：退出码 0 和给定的结构化输出。
func (e *FakeExecutor) PushOutput(out map[string]any) {
	zero := 0
	e.q.push(request.ExecutionResult{ExitCode: &zero, Output: out}, nil)
}

// SetDefault 设定队列取完后的默认结果。
func (e *FakeExecutor) SetDefault(res request.ExecutionResult) { e.q.setDefault(res) }

// Calls 返回 Execute 的调用次数。
func (e *FakeExecutor) Calls() int { return e.CallCount("Execute") }

// Requests 返回每次 Execute 收到的申请（克隆，按调用顺序）。
func (e *FakeExecutor) Requests() []*request.ExecutionRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]*request.ExecutionRequest, len(e.reqs))
	for i, r := range e.reqs {
		out[i] = r.Clone()
	}
	return out
}

func (e *FakeExecutor) Execute(ctx context.Context, r *request.ExecutionRequest) (request.ExecutionResult, error) {
	if err := e.begin(ctx, "Execute"); err != nil {
		return request.ExecutionResult{}, err
	}
	e.mu.Lock()
	e.reqs = append(e.reqs, r.Clone())
	e.mu.Unlock()
	res, err := e.q.next()
	if err != nil {
		return request.ExecutionResult{}, err
	}
	res = *res.Clone()
	now := stamp(e.Clock)
	if res.StartedAt.IsZero() {
		res.StartedAt = now
	}
	if res.EndedAt.IsZero() {
		res.EndedAt = now
	}
	return res, nil
}

// ---- EventSink ----

// MemEventSink 记录 Emit 收到的事件。方法名：Emit。
type MemEventSink struct {
	Faults
	mu  sync.Mutex
	rec recorder
}

var (
	_ ports.EventSink = (*MemEventSink)(nil)
	_ EventRecorder   = (*MemEventSink)(nil)
)

func NewMemEventSink() *MemEventSink { return &MemEventSink{} }

func (s *MemEventSink) SetLog(l *EventLog) { s.mu.Lock(); s.rec.log = l; s.mu.Unlock() }

func (s *MemEventSink) Emit(ctx context.Context, evts ...events.Event) error {
	if err := s.begin(ctx, "Emit"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rec.add(evts)
	return nil
}

// Events 返回收到的全部事件（克隆，按先后顺序）。
func (s *MemEventSink) Events() []events.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneEvents(s.rec.evts)
}

// ---- AuditSink ----

// MemAuditSink 记录审计记录。方法名：Append。
type MemAuditSink struct {
	Faults
	mu   sync.Mutex
	recs []ports.AuditRecord
}

var _ ports.AuditSink = (*MemAuditSink)(nil)

func NewMemAuditSink() *MemAuditSink { return &MemAuditSink{} }

func (a *MemAuditSink) Append(ctx context.Context, rec ports.AuditRecord) error {
	if err := a.begin(ctx, "Append"); err != nil {
		return err
	}
	rec.Detail = request.CloneMap(rec.Detail)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.recs = append(a.recs, rec)
	return nil
}

// Records 返回全部审计记录（克隆）。
func (a *MemAuditSink) Records() []ports.AuditRecord {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]ports.AuditRecord, len(a.recs))
	for i, r := range a.recs {
		r.Detail = request.CloneMap(r.Detail)
		out[i] = r
	}
	return out
}
