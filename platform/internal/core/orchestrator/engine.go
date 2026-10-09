package orchestrator

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	creq "github.com/LeXwDeX/DevOps-Agent/platform/internal/core/request"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/events"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/orchestrator/expr"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/ports"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/ids"
	mreq "github.com/LeXwDeX/DevOps-Agent/platform/internal/model/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/task"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/consts"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

// Deps 是引擎依赖的端口。
type Deps struct {
	Tasks    ports.TaskStore
	Requests ports.RequestStore
	Registry ports.Registry
	Clock    ports.Clock
	IDGen    ports.IDGen
}

// Engine 是既定任务编排引擎。它不执行任何东西：每一步生成一个草稿申请交给调用方
// （Harness）送进流水线，申请状态变化后调用方再通知引擎推进（Advance）。
// 引擎无状态，只缓存编译好的分支条件；可并发使用，并发冲突由存储端口的 CAS 挡住
// （返回 core.cas_conflict，调用方重新读取后重试）。
type Engine struct {
	d Deps

	mu    sync.Mutex
	cache map[string]map[string][]*expr.Program
}

// New 创建引擎。
func New(d Deps) (*Engine, error) {
	if d.Tasks == nil || d.Requests == nil || d.Registry == nil || d.Clock == nil || d.IDGen == nil {
		return nil, errcode.New(errcode.InvalidArgument, "orchestrator: all deps are required")
	}
	return &Engine{d: d, cache: map[string]map[string][]*expr.Program{}}, nil
}

// StartInput 是启动一次任务运行的输入。
type StartInput struct {
	TaskID    string
	Version   int64 // 0 = 当前启用版本
	SessionID string
	Scope     mreq.Scope
	Params    map[string]any
}

// load 读取并校验任务定义，返回定义和编译好的分支条件（按 ID+版本+摘要缓存，运行时不再解析新表达式）。
func (e *Engine) load(ctx context.Context, taskID string, version int64) (*task.Definition, map[string][]*expr.Program, error) {
	def, err := e.d.Tasks.Definition(ctx, taskID, version)
	if err != nil {
		return nil, nil, err
	}
	if def.ID != taskID {
		return nil, nil, errcode.Newf(errcode.TaskInvalid, "definition id %q does not match task %q", def.ID, taskID)
	}
	key := fmt.Sprintf("%s@%d@%s", def.ID, def.Version, def.Digest)
	e.mu.Lock()
	progs, ok := e.cache[key]
	e.mu.Unlock()
	if ok {
		return def, progs, nil
	}
	progs, err = checkAndCompile(def)
	if err != nil {
		return nil, nil, err
	}
	e.mu.Lock()
	e.cache[key] = progs
	e.mu.Unlock()
	return def, progs, nil
}

// loadForRun 读取运行绑定的版本，并核对摘要没有变（版本不可变，K-21）。
func (e *Engine) loadForRun(ctx context.Context, run *task.Run) (*task.Definition, map[string][]*expr.Program, error) {
	def, progs, err := e.load(ctx, run.TaskID, run.TaskVersion)
	if err != nil {
		return nil, nil, err
	}
	if def.Digest != run.TaskDigest {
		return nil, nil, errcode.Newf(errcode.DigestMismatch, "run %s: task %s version %d digest changed", run.RunID, run.TaskID, run.TaskVersion)
	}
	return def, progs, nil
}

func (e *Engine) eventID() string { return e.d.IDGen.NewID("event") }

// Start 启动一次运行并绑定任务版本和摘要，之后不变（61 第 7.3 节）。
// 参数不全时运行进入 awaiting_input，不建申请、不产生事件，返回的申请为 nil；
// 否则创建入口步骤的草稿申请，运行进入 running，产生 devops.task.started 和 devops.call.proposed。
// 返回的申请由调用方送进流水线。
func (e *Engine) Start(ctx context.Context, in StartInput) (*task.Run, *mreq.ExecutionRequest, error) {
	if in.TaskID == "" || in.SessionID == "" {
		return nil, nil, errcode.New(errcode.InvalidArgument, "task id and session id are required")
	}
	version := in.Version
	if version == 0 {
		v, err := e.d.Tasks.ActiveVersion(ctx, in.TaskID)
		if err != nil {
			return nil, nil, err
		}
		version = v
	}
	def, _, err := e.load(ctx, in.TaskID, version)
	if err != nil {
		return nil, nil, err
	}
	if err := checkParams(def, in.Params); err != nil {
		return nil, nil, err
	}
	now := e.d.Clock.Now()
	run := &task.Run{
		RunID:       e.d.IDGen.NewID("run"),
		TaskID:      def.ID,
		TaskVersion: def.Version,
		TaskDigest:  def.Digest,
		SessionID:   in.SessionID,
		Scope:       in.Scope.Normalize(),
		Params:      mreq.CloneMap(in.Params),
		Version:     1,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if missing := missingParams(def, run.Params); len(missing) > 0 {
		run.State = consts.RunAwaitingInput
		run.MissingInputs = missing
		if err := e.d.Tasks.CreateRun(ctx, run, nil); err != nil {
			return nil, nil, err
		}
		return run, nil, nil
	}
	return e.launch(ctx, def, run, 0, now)
}

// ProvideInput 补参数。只在 awaiting_input 时可用；合并后重新校验。
// 补齐时创建入口步骤的申请（同 Start）；仍缺时保存合并后的参数和缺项，返回的申请为 nil。
func (e *Engine) ProvideInput(ctx context.Context, runID string, params map[string]any) (*task.Run, *mreq.ExecutionRequest, error) {
	run, err := e.d.Tasks.GetRun(ctx, runID)
	if err != nil {
		return nil, nil, err
	}
	if run.State != consts.RunAwaitingInput {
		return nil, nil, errcode.Newf(errcode.RunInvalidState, "run %s is %s, not awaiting_input", runID, run.State)
	}
	def, _, err := e.loadForRun(ctx, run)
	if err != nil {
		return nil, nil, err
	}
	if err := checkParams(def, params); err != nil {
		return nil, nil, err
	}
	now := e.d.Clock.Now()
	expected := run.Version
	if run.Params == nil {
		run.Params = map[string]any{}
	}
	for k, v := range mreq.CloneMap(params) {
		run.Params[k] = v
	}
	if missing := missingParams(def, run.Params); len(missing) > 0 {
		run.MissingInputs = missing
		if err := e.saveRun(ctx, run, expected, now, nil); err != nil {
			return nil, nil, err
		}
		return run, nil, nil
	}
	return e.launch(ctx, def, run, expected, now)
}

// launch 创建入口步骤的申请并让运行进入 running。expected 为 0 表示运行尚未保存（Start）。
func (e *Engine) launch(ctx context.Context, def *task.Definition, run *task.Run, expected ids.Version, now time.Time) (*task.Run, *mreq.ExecutionRequest, error) {
	entry, ok := def.Step(def.Entry)
	if !ok {
		return nil, nil, errcode.Newf(errcode.TaskInvalid, "entry %q is not a step", def.Entry)
	}
	req, err := e.newStepRequest(ctx, def, run, entry, now)
	if err != nil {
		return nil, nil, err
	}
	run.State = consts.RunRunning
	run.MissingInputs = nil
	attach(run, entry.ID, req)
	started := events.ForRun(run, events.TaskStarted, map[string]any{
		"task_id":      def.ID,
		"task_version": def.Version,
		"task_digest":  string(def.Digest),
		"param_names":  sortedKeys(run.Params),
		"entry":        entry.ID,
	}, now, e.eventID())
	if expected == 0 {
		err = e.d.Tasks.CreateRun(ctx, run, []events.Event{started})
	} else {
		err = e.saveRun(ctx, run, expected, now, []events.Event{started})
	}
	if err != nil {
		return nil, nil, err
	}
	if err := e.createRequest(ctx, run, req, now); err != nil {
		return nil, nil, err
	}
	return run, req, nil
}

// attach 把新步骤申请记入运行。
func attach(run *task.Run, stepID string, req *mreq.ExecutionRequest) {
	run.Current = stepID
	run.Steps = append(run.Steps, task.StepRecord{StepID: stepID, CallID: req.CallID, State: consts.RequestDraft})
}

// createRequest 保存步骤申请和 devops.call.proposed。运行已先保存；
// 申请建不出来时运行不能悬空，尽力把它标为停止再返回原错误。
func (e *Engine) createRequest(ctx context.Context, run *task.Run, req *mreq.ExecutionRequest, now time.Time) error {
	proposed := events.ForRequest(req, events.CallProposed, map[string]any{
		"run_id":  run.RunID,
		"step_id": req.Task.StepID,
		"source":  string(req.Source),
	}, now, e.eventID())
	if err := e.d.Requests.Create(ctx, req, []events.Event{proposed}); err != nil {
		e.abandon(context.WithoutCancel(ctx), run.RunID, req, err)
		return err
	}
	return nil
}

// abandon 在步骤申请没建成时尽力停止运行。失败只能放弃：恢复会处理遗留的运行。
func (e *Engine) abandon(ctx context.Context, runID string, req *mreq.ExecutionRequest, cause error) {
	run, err := e.d.Tasks.GetRun(ctx, runID)
	if err != nil || run.State.IsTerminal() {
		return
	}
	now := e.d.Clock.Now()
	expected := run.Version
	if n := len(run.Steps); n > 0 && run.Steps[n-1].CallID == req.CallID {
		run.Steps = run.Steps[:n-1]
	}
	detail := "step request could not be created: " + cause.Error()
	setStopped(run, consts.RunStopped, consts.StopFailed, req.Task.StepID, detail, now)
	ev := e.stoppedEvent(run, "", now)
	_ = e.saveRun(ctx, run, expected, now, []events.Event{ev})
}

// newStepRequest 让 Registry 按参数和前序输出生成内容与预期，建草稿申请。
// 步骤内容不在 core 里模板化（61 第 7.3 节）。不保存任何东西。
func (e *Engine) newStepRequest(ctx context.Context, def *task.Definition, run *task.Run, step task.Step, now time.Time) (*mreq.ExecutionRequest, error) {
	content, expectation, err := e.d.Registry.RenderStep(ctx, def, step, mreq.CloneMap(run.Params), completedOutputs(run))
	if err != nil {
		return nil, err
	}
	// 任务定义固定了步骤引用的单元版本；Registry 生成的内容必须引用同一个单元。
	switch u := content.Unit; {
	case u == nil:
		ref := step.Unit
		content.Unit = &ref
	case u.ID != step.Unit.ID || u.Version != step.Unit.Version ||
		(step.Unit.Digest != "" && u.Digest != step.Unit.Digest):
		return nil, errcode.Newf(errcode.UnitMismatch, "step %s: rendered unit %s@%d differs from the definition's %s@%d",
			step.ID, u.ID, u.Version, step.Unit.ID, step.Unit.Version)
	}
	return creq.New(creq.NewParams{
		CallID:      ids.CallID(e.d.IDGen.NewID("call")),
		Source:      consts.SourceTaskStep,
		SessionID:   run.SessionID,
		Task:        &mreq.TaskRef{RunID: run.RunID, StepID: step.ID},
		Scope:       run.Scope,
		Content:     content,
		Expectation: expectation,
		Submission:  mreq.Submission{SelfReview: &mreq.SelfReview{Verdict: consts.VerdictNotApplicable}},
		Now:         now,
	})
}

// saveRun 设置 Version 和 UpdatedAt 后做 CAS 保存。
func (e *Engine) saveRun(ctx context.Context, run *task.Run, expected ids.Version, now time.Time, evts []events.Event) error {
	run.Version = expected.Next()
	run.UpdatedAt = now
	return e.d.Tasks.SaveRun(ctx, run, expected, evts)
}

// completedOutputs 返回已成功步骤的结构化输出，键为步骤 ID（深拷贝）。
func completedOutputs(run *task.Run) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, r := range run.Steps {
		if r.State == consts.RequestSucceeded {
			o := mreq.CloneMap(r.Output)
			if o == nil {
				o = map[string]any{}
			}
			out[r.StepID] = o
		}
	}
	return out
}

// branchEnv 是分支条件的求值环境：params 和每个已完成步骤的输出，别的都读不到。
func branchEnv(run *task.Run) expr.MapEnv {
	env := expr.MapEnv{ParamsPrefix: map[string]any(run.Params)}
	for id, o := range completedOutputs(run) {
		env[id] = o
	}
	return env
}

func sortedKeys(m map[string]any) []any {
	out := make([]any, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].(string) < out[j].(string) })
	return out
}

// setStopped 让运行进入 stopped / cancelled / recovery_interrupted 并记录停止信息。
func setStopped(run *task.Run, state consts.RunState, reason consts.StopReason, stepID, detail string, now time.Time) {
	run.State = state
	run.Stop = &task.RunStop{Reason: reason, StepID: stepID, Detail: detail, At: now}
}

// stoppedEvent 构造 devops.task.stopped。run.Stop 必须已设置。
func (e *Engine) stoppedEvent(run *task.Run, actor string, now time.Time) events.Event {
	data := map[string]any{"reason": string(run.Stop.Reason)}
	if run.Stop.StepID != "" {
		data["step_id"] = run.Stop.StepID
	}
	if run.Stop.Detail != "" {
		data["detail"] = run.Stop.Detail
	}
	if actor != "" {
		data["actor"] = actor
	}
	return events.ForRun(run, events.TaskStopped, data, now, e.eventID())
}
