package flowtest

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/coretest"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/events"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/orchestrator"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/pipeline"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/ports"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/recovery"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/ids"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/policy"
	mreq "github.com/LeXwDeX/DevOps-Agent/platform/internal/model/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/task"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/consts"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

var ctx = context.Background()

// errCrashed 模拟进程在终态保存前死掉：保存没有发生，申请留在 executing。
var errCrashed = errors.New("flowtest: process crashed before the result was saved")

// crashStore 包住申请存储：crash 打开后，任何把申请写成执行终态的保存都失败，
// 存储里的申请停在 executing，正像执行器进程在回写结果前崩溃。
type crashStore struct {
	ports.RequestStore
	crash atomic.Bool
}

func (s *crashStore) Save(ctx context.Context, r *mreq.ExecutionRequest, expected ids.Version, evts []events.Event) error {
	if s.crash.Load() && r.State.IsTerminal() && r.Execution != nil && r.Execution.Outcome != "" {
		return errCrashed
	}
	return s.RequestStore.Save(ctx, r, expected, evts)
}

// harness 把流水线、引擎和替身装在一起。
type harness struct {
	t     *testing.T
	env   *coretest.Env
	store *crashStore
	svc   *pipeline.Service
	eng   *orchestrator.Engine
}

const (
	project = "proj"
	envName = "prod"
	target  = "host-a"
	session = "s1"
)

func unitDigest(id string) ids.Digest { return ids.Digest(strings.Repeat(string(id[len(id)-1]), 64)) }

// taskDef 是 A→B→D，D 按 status 分支到 E 或 H；没有匹配就停。
func taskDef() *task.Definition {
	step := func(id, next string) task.Step {
		u := "u" + strings.ToLower(id)
		return task.Step{ID: id, Unit: mreq.UnitRef{ID: u, Version: 1, Digest: unitDigest(u)}, Next: next}
	}
	d := step("D", "")
	d.Outputs = []task.FieldSpec{{Name: "status", Type: task.FieldType{Kind: task.KindString}}}
	d.Branches = []task.Branch{
		{When: `D.status == "ok"`, To: "E"},
		{When: `D.status == "degraded"`, To: "H"},
	}
	return &task.Definition{
		ID: "deploy", Version: 1, Digest: ids.Digest(strings.Repeat("f", 64)),
		Params: []task.FieldSpec{{Name: "svc", Type: task.FieldType{Kind: task.KindString}, Required: true}},
		Entry:  "A",
		Steps:  []task.Step{step("A", "B"), step("B", "D"), d, step("E", ""), step("H", "")},
	}
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	env := coretest.NewEnv()
	h := &harness{t: t, env: env, store: &crashStore{RequestStore: env.Requests}}

	env.Scope.AddSession(session, ports.SessionInfo{Project: project, Environment: envName, Mode: consts.SessionAuto})
	env.Scope.AddTargets(project, envName, target)
	env.Policy.Set(policy.Set{Version: "p1", Rules: []policy.Rule{
		{Layer: consts.LayerGlobal, Subject: "svc", Mode: policy.ModePtr(consts.PolicyAuto)},
	}})

	def := taskDef()
	env.Tasks.SeedDefinition(def, true)
	for _, st := range def.Steps {
		env.Registry.AddUnit(ports.UnitInfo{
			Ref: st.Unit, Subject: "svc", Action: "step-" + st.ID, Zone: consts.ZoneWrite,
			Category: consts.CategoryWrite, Reversible: consts.Reversible, HasPreview: true,
			FingerprintMethod: "fake", FingerprintMethodVersion: "v1",
		})
	}
	// 像 Registry 那样按定义生成步骤内容和预期：单元引用取定义里的，内容和参数不进事件。
	env.Registry.RenderFn = func(_ context.Context, _ *task.Definition, step task.Step, params map[string]any, _ map[string]map[string]any) (mreq.Content, mreq.Expectation, error) {
		u := step.Unit
		return mreq.Content{Unit: &u, Rendered: "RENDERED-" + step.ID, Params: map[string]any{"svc": params["svc"]}},
			mreq.Expectation{
				Intent: "run step " + step.ID, Targets: []string{target}, Category: consts.CategoryWrite,
				ExpectedChange: mreq.ExpectedChange{Text: "step applied"}, ExpectedOutput: "ok",
				SuccessCriteria: []mreq.Criterion{{Expr: "exit_code == 0"}},
				Reversible:      consts.Reversible, Rollback: "rerun previous step", HasPreview: true,
			}, nil
	}

	svc, err := pipeline.New(pipeline.Deps{
		Requests: h.store, Revisions: env.Revisions, Policy: env.Policy, Registry: env.Registry, Scope: env.Scope,
		Preview: env.Preview, Fingerprint: env.Fingerprint, DCG: env.DCG, Executor: env.Executor,
		Audit: env.Audit, Clock: env.Clock, IDs: env.IDs,
	})
	if err != nil {
		t.Fatal(err)
	}
	eng, err := orchestrator.New(orchestrator.Deps{Tasks: env.Tasks, Requests: h.store, Registry: env.Registry, Clock: env.Clock, IDGen: env.IDs})
	if err != nil {
		t.Fatal(err)
	}
	h.svc, h.eng = svc, eng
	return h
}

func (h *harness) recoveryDeps() recovery.Deps {
	return recovery.Deps{Requests: h.store, Tasks: h.env.Tasks, Events: h.env.Sink, Clock: h.env.Clock, IDGen: h.env.IDs}
}

// setStepPolicy 给某一步的功能设置 action 层策略（具体功能优先于 global）。
func (h *harness) setStepPolicy(stepID string, mode consts.PolicyMode) {
	h.env.Policy.Set(policy.Set{Version: "p2", Rules: []policy.Rule{
		{Layer: consts.LayerGlobal, Subject: "svc", Mode: policy.ModePtr(consts.PolicyAuto)},
		{Layer: consts.LayerAction, Subject: "svc", Action: "step-" + stepID, Mode: policy.ModePtr(mode)},
	}})
}

func (h *harness) start() (*task.Run, *mreq.ExecutionRequest) {
	h.t.Helper()
	run, req, err := h.eng.Start(ctx, orchestrator.StartInput{
		TaskID: "deploy", SessionID: session,
		Scope:  mreq.Scope{Project: project, Environment: envName, Targets: []string{target}},
		Params: map[string]any{"svc": "nginx"},
	})
	if err != nil {
		h.t.Fatalf("Start: %v", err)
	}
	return run, req
}

func (h *harness) advance(run *task.Run, r *mreq.ExecutionRequest) (*task.Run, *mreq.ExecutionRequest) {
	h.t.Helper()
	run, next, err := h.eng.Advance(ctx, run.RunID, r)
	if err != nil {
		h.t.Fatalf("Advance(%s, %s): %v", r.Task.StepID, r.State, err)
	}
	return run, next
}

// execute 执行前核验并执行；目标变化时返回已保存的 target_changed 申请。
func (h *harness) execute(r *mreq.ExecutionRequest) *mreq.ExecutionRequest {
	h.t.Helper()
	out, err := h.svc.Execute(ctx, r.CallID)
	if err != nil {
		if code, _ := errcode.CodeOf(err); code == errcode.FingerprintChanged && out != nil {
			return out
		}
		h.t.Fatalf("Execute(%s): %v", r.Task.StepID, err)
	}
	return out
}

// drive 像 Harness 那样推进运行：每个步骤申请检验 → 引擎跟进 → （冻结则）执行 → 引擎推进，
// 直到运行结束或需要人工。返回最后的运行和停下时的步骤申请（运行结束时为 nil）。
func (h *harness) drive(run *task.Run, req *mreq.ExecutionRequest) (*task.Run, *mreq.ExecutionRequest) {
	h.t.Helper()
	for req != nil {
		r, err := h.svc.Inspect(ctx, req.CallID)
		if err != nil {
			h.t.Fatalf("Inspect(%s): %v", req.Task.StepID, err)
		}
		var next *mreq.ExecutionRequest
		run, next = h.advance(run, r)
		switch r.State {
		case consts.RequestFrozen:
			done := h.execute(r)
			run, next = h.advance(run, done)
		case consts.RequestAwaitingReview:
			return run, r
		default:
			if next != nil {
				h.t.Fatalf("step %s is %s but engine created a next step", r.Task.StepID, r.State)
			}
		}
		req = next
	}
	return run, nil
}

// approve 人工 ▶ 执行当前步骤，然后执行并推进，继续走完。
func (h *harness) approve(run *task.Run, r *mreq.ExecutionRequest) (*task.Run, *mreq.ExecutionRequest) {
	h.t.Helper()
	frozen, err := h.svc.Review(ctx, r.CallID, pipeline.ReviewInput{
		Action: consts.ActionExecute, Revision: r.Revision, Digest: r.Digest, Actor: "alice",
	})
	if err != nil {
		h.t.Fatalf("Review: %v", err)
	}
	if frozen.State != consts.RequestFrozen {
		h.t.Fatalf("after approval state = %s", frozen.State)
	}
	run, _ = h.advance(run, frozen)
	if run.State != consts.RunRunning {
		h.t.Fatalf("run after approval = %s", run.State)
	}
	done := h.execute(frozen)
	run, next := h.advance(run, done)
	return h.drive(run, next)
}

func (h *harness) storedRun(id string) *task.Run {
	h.t.Helper()
	run, err := h.env.Tasks.GetRun(ctx, id)
	if err != nil {
		h.t.Fatal(err)
	}
	return run
}

func (h *harness) storedReq(id ids.CallID) *mreq.ExecutionRequest {
	h.t.Helper()
	r, err := h.env.Requests.Get(ctx, id)
	if err != nil {
		h.t.Fatal(err)
	}
	return r
}

func (h *harness) executedSteps() []string {
	var out []string
	for _, r := range h.env.Executor.Requests() {
		out = append(out, r.Task.StepID)
	}
	return out
}

func streamTypes(evts []events.Event, stream string) []events.Type {
	var out []events.Type
	for _, e := range evts {
		if e.StreamID == stream {
			out = append(out, e.Type)
		}
	}
	return out
}

func sameTypes(got, want []events.Type) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func stepIDs(run *task.Run) []string {
	var out []string
	for _, s := range run.Steps {
		out = append(out, s.StepID)
	}
	return out
}

// autoStepEvents 是一步自动通过并执行成功时，申请事件流的固定顺序。
var autoStepEvents = []events.Type{
	events.CallProposed, events.InspectionStarted, events.InspectionPassed, events.PolicyDecided,
	events.CallFrozen, events.ExecutionStarted, events.ExecutionCompleted,
}

func TestTaskRunsEndToEndThroughOKBranch(t *testing.T) {
	h := newHarness(t)
	// D 的结构化输出决定分支；其余步骤用执行器默认结果（退出码 0）。
	h.env.Executor.PushOutput(nil)                            // A
	h.env.Executor.PushOutput(nil)                            // B
	h.env.Executor.PushOutput(map[string]any{"status": "ok"}) // D

	run, req := h.start()
	run, left := h.drive(run, req)
	if left != nil {
		t.Fatalf("run paused at step %s (%s)", left.Task.StepID, left.State)
	}
	if run.State != consts.RunCompleted || run.Stop != nil {
		t.Fatalf("run = %s stop %+v", run.State, run.Stop)
	}
	if got := stepIDs(run); !sameStrings(got, []string{"A", "B", "D", "E"}) {
		t.Fatalf("steps = %v", got)
	}
	if got := h.executedSteps(); !sameStrings(got, []string{"A", "B", "D", "E"}) {
		t.Fatalf("executor ran %v, want exactly once per step", got)
	}
	for i, s := range run.Steps {
		if s.State != consts.RequestSucceeded {
			t.Errorf("step %s state = %s", s.StepID, s.State)
		}
		if stored := h.storedReq(s.CallID); stored.State != consts.RequestSucceeded || stored.Frozen == nil {
			t.Errorf("stored step %s = %s", s.StepID, stored.State)
		}
		wantNext := []string{"B", "D", "E", ""}[i]
		if s.Next != wantNext {
			t.Errorf("step %s next = %q, want %q", s.StepID, s.Next, wantNext)
		}
	}
	if run.Steps[2].Output["status"] != "ok" {
		t.Errorf("D output not recorded: %v", run.Steps[2].Output)
	}

	all := h.env.Log.All()
	wantRun := []events.Type{
		events.TaskStarted,
		events.TaskStepCompleted, events.TaskStepCompleted, events.TaskStepCompleted, events.TaskStepCompleted,
		events.TaskCompleted,
	}
	if got := streamTypes(all, events.RunStreamPrefix+run.RunID); !sameTypes(got, wantRun) {
		t.Fatalf("run events = %v, want %v", got, wantRun)
	}
	for _, s := range run.Steps {
		if got := streamTypes(all, events.CallStreamPrefix+string(s.CallID)); !sameTypes(got, autoStepEvents) {
			t.Errorf("step %s events = %v, want %v", s.StepID, got, autoStepEvents)
		}
	}
	// 事件里没有内容原文。
	for _, e := range all {
		for _, v := range e.Data {
			if s, ok := v.(string); ok && strings.Contains(s, "RENDERED-") {
				t.Errorf("event %s leaks rendered content: %v", e.Type, e.Data)
			}
		}
	}
	// 事件序号在每条流上连续，且与存储里的记录一致。
	if stored := h.storedRun(run.RunID); stored.EventSeq != int64(len(wantRun)) || stored.Version != run.Version {
		t.Errorf("stored run seq %d version %d, want %d / %d", stored.EventSeq, stored.Version, len(wantRun), run.Version)
	}
	if n := len(h.env.Sink.Events()); n != 0 {
		t.Errorf("EventSink got %d events; all state events must go with the saves", n)
	}
}

func TestTaskRunsThroughDegradedBranch(t *testing.T) {
	h := newHarness(t)
	h.env.Executor.PushOutput(nil)
	h.env.Executor.PushOutput(nil)
	h.env.Executor.PushOutput(map[string]any{"status": "degraded"})
	run, req := h.start()
	run, _ = h.drive(run, req)
	if run.State != consts.RunCompleted || !sameStrings(stepIDs(run), []string{"A", "B", "D", "H"}) {
		t.Fatalf("run = %s steps %v", run.State, stepIDs(run))
	}
	if got := h.executedSteps(); !sameStrings(got, []string{"A", "B", "D", "H"}) {
		t.Fatalf("executor ran %v", got)
	}
}

func TestNoBranchMatchedStopsRunForHuman(t *testing.T) {
	h := newHarness(t)
	h.env.Executor.PushOutput(nil)
	h.env.Executor.PushOutput(nil)
	h.env.Executor.PushOutput(map[string]any{"status": "weird"})
	run, req := h.start()
	run, left := h.drive(run, req)
	if left != nil || run.State != consts.RunStopped || run.Stop == nil || run.Stop.Reason != consts.StopNoBranchMatched || run.Stop.StepID != "D" {
		t.Fatalf("run = %s stop %+v", run.State, run.Stop)
	}
	if got := h.executedSteps(); !sameStrings(got, []string{"A", "B", "D"}) {
		t.Fatalf("executor ran %v", got)
	}
}

func TestManualStepWaitsForHumanThenContinues(t *testing.T) {
	h := newHarness(t)
	h.setStepPolicy("B", consts.PolicyManual)
	h.env.Executor.PushOutput(nil)
	h.env.Executor.PushOutput(nil)
	h.env.Executor.PushOutput(map[string]any{"status": "ok"})

	run, req := h.start()
	run, paused := h.drive(run, req)
	if paused == nil || paused.Task.StepID != "B" || paused.State != consts.RequestAwaitingReview {
		t.Fatalf("expected to pause at B awaiting_review, got %+v", paused)
	}
	if run.State != consts.RunAwaitingReview || run.Current != "B" {
		t.Fatalf("run = %s current %s", run.State, run.Current)
	}
	if got := h.executedSteps(); !sameStrings(got, []string{"A"}) {
		t.Fatalf("executor ran %v while B awaits review", got)
	}
	// 没有人工批准不能执行。
	if _, err := h.svc.Execute(ctx, paused.CallID); err == nil {
		t.Fatal("Execute succeeded on an awaiting_review request")
	}
	if got := h.executedSteps(); !sameStrings(got, []string{"A"}) {
		t.Fatalf("executor ran %v after refused Execute", got)
	}

	run, left := h.approve(run, paused)
	if left != nil || run.State != consts.RunCompleted {
		t.Fatalf("run = %s", run.State)
	}
	if got := h.executedSteps(); !sameStrings(got, []string{"A", "B", "D", "E"}) {
		t.Fatalf("executor ran %v", got)
	}
	b := h.storedReq(paused.CallID)
	if len(b.Reviews) != 1 || b.Reviews[0].Action != consts.ActionExecute || b.Reviews[0].Revision != b.Revision || b.Reviews[0].Digest != b.Digest {
		t.Fatalf("approval not bound to B's revision: %+v", b.Reviews)
	}
	wantB := []events.Type{
		events.CallProposed, events.InspectionStarted, events.InspectionPassed, events.PolicyDecided,
		events.ReviewRequested, events.ReviewApproved, events.CallFrozen, events.ExecutionStarted, events.ExecutionCompleted,
	}
	if got := streamTypes(h.env.Log.All(), events.CallStreamPrefix+string(b.CallID)); !sameTypes(got, wantB) {
		t.Fatalf("B events = %v, want %v", got, wantB)
	}
	wantRun := []events.Type{
		events.TaskStarted, events.TaskStepCompleted, events.TaskStepCompleted, events.TaskStepCompleted, events.TaskStepCompleted,
		events.TaskCompleted,
	}
	if got := streamTypes(h.env.Log.All(), events.RunStreamPrefix+run.RunID); !sameTypes(got, wantRun) {
		t.Fatalf("run events = %v", got)
	}
}

func TestFingerprintChangeBeforeStepStopsRun(t *testing.T) {
	h := newHarness(t)
	// 采集顺序：A 检验、A 执行前、B 检验、B 执行前。B 执行前目标变了。
	h.env.Fingerprint.PushValue("fp-1")
	h.env.Fingerprint.PushValue("fp-1")
	h.env.Fingerprint.PushValue("fp-1")
	h.env.Fingerprint.PushValue("fp-2")

	run, req := h.start()
	run, left := h.drive(run, req)
	if left != nil {
		t.Fatalf("drive paused at %s", left.State)
	}
	if run.State != consts.RunStopped || run.Stop == nil || run.Stop.Reason != consts.StopTargetChanged || run.Stop.StepID != "B" {
		t.Fatalf("run = %s stop %+v", run.State, run.Stop)
	}
	if got := h.executedSteps(); !sameStrings(got, []string{"A"}) {
		t.Fatalf("executor ran %v, B must not run after the target changed", got)
	}
	b := h.storedReq(run.Steps[1].CallID)
	if b.State != consts.RequestStopped || b.Stop == nil || b.Stop.Reason != consts.StopTargetChanged {
		t.Fatalf("B = %s stop %+v", b.State, b.Stop)
	}
	if h.env.Fingerprint.Calls() != 4 {
		t.Errorf("fingerprint collected %d times, want 4", h.env.Fingerprint.Calls())
	}
	wantB := []events.Type{
		events.CallProposed, events.InspectionStarted, events.InspectionPassed, events.PolicyDecided,
		events.CallFrozen, events.CallTargetChanged, events.CallStopped,
	}
	if got := streamTypes(h.env.Log.All(), events.CallStreamPrefix+string(b.CallID)); !sameTypes(got, wantB) {
		t.Fatalf("B events = %v, want %v", got, wantB)
	}
	wantRun := []events.Type{events.TaskStarted, events.TaskStepCompleted, events.TaskStopped}
	if got := streamTypes(h.env.Log.All(), events.RunStreamPrefix+run.RunID); !sameTypes(got, wantRun) {
		t.Fatalf("run events = %v, want %v", got, wantRun)
	}
	// 登记过的步骤不由 Agent 修订；运行已停，不能再推进。
	_, err := h.svc.Revise(ctx, b.CallID, pipeline.ReviseInput{Content: b.Content, Expectation: b.Expectation, Source: consts.RevisionSelfReview})
	if err == nil {
		t.Fatal("Revise succeeded on a stopped task step")
	}
	if _, _, err := h.eng.Advance(ctx, run.RunID, b); err == nil {
		t.Fatal("Advance succeeded on a stopped run")
	}
}

func TestCrashRecoveryStopsEverythingAndNothingResumes(t *testing.T) {
	h := newHarness(t)

	// 运行 1：A 执行中，结果没写回就崩溃。
	run1, reqA := h.start()
	a, err := h.svc.Inspect(ctx, reqA.CallID)
	if err != nil || a.State != consts.RequestFrozen {
		t.Fatalf("A inspect: %v %s", err, a.State)
	}
	run1, _ = h.advance(run1, a)
	h.store.crash.Store(true)
	if _, err := h.svc.Execute(ctx, a.CallID); !errors.Is(err, errCrashed) {
		t.Fatalf("Execute err = %v, want the crash", err)
	}
	h.store.crash.Store(false)
	if got := h.storedReq(a.CallID); got.State != consts.RequestExecuting {
		t.Fatalf("A after crash = %s, want executing", got.State)
	}
	if run1 = h.storedRun(run1.RunID); run1.State != consts.RunRunning {
		t.Fatalf("run1 = %s", run1.State)
	}

	// 运行 2：A 需要人工，停在待人工。
	h.setStepPolicy("A", consts.PolicyManual)
	run2, reqA2 := h.start()
	run2, paused := h.drive(run2, reqA2)
	if paused == nil || paused.State != consts.RequestAwaitingReview || run2.State != consts.RunAwaitingReview {
		t.Fatalf("run2 = %s paused %+v", run2.State, paused)
	}

	// 一个不属于任务的已登记工具申请，也在待人工。
	h.env.Registry.AddUnit(ports.UnitInfo{
		Ref: mreq.UnitRef{ID: "tool", Version: 1, Digest: unitDigest("tool")}, Subject: "svc", Action: "tool", Zone: consts.ZoneWrite,
		Category: consts.CategoryWrite, Reversible: consts.Reversible, HasPreview: true, FingerprintMethod: "fake", FingerprintMethodVersion: "v1",
	})
	tool, err := h.svc.Submit(ctx, pipeline.SubmitInput{
		Source: consts.SourceRegisteredTool, SessionID: session,
		Scope:   mreq.Scope{Project: project, Environment: envName, Targets: []string{target}},
		Content: mreq.Content{Unit: &mreq.UnitRef{ID: "tool", Version: 1, Digest: unitDigest("tool")}, Rendered: "RENDERED-tool"},
		Expectation: mreq.Expectation{
			Intent: "tool", Targets: []string{target}, Category: consts.CategoryWrite,
			ExpectedChange: mreq.ExpectedChange{Text: "x"}, ExpectedOutput: "y",
			SuccessCriteria: []mreq.Criterion{{Expr: "exit_code == 0"}}, Reversible: consts.Reversible, Rollback: "z",
		},
		Submission: mreq.Submission{SelfReview: &mreq.SelfReview{Verdict: consts.VerdictRisk}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if tool, err = h.svc.Inspect(ctx, tool.CallID); err != nil || tool.State != consts.RequestAwaitingReview {
		t.Fatalf("tool inspect: %v %s", err, tool.State)
	}
	execBefore := h.env.Executor.Calls()

	// 服务重启：恢复即停止。
	rep, err := recovery.OnStartup(ctx, h.recoveryDeps())
	if err != nil || len(rep.Errors) != 0 {
		t.Fatalf("recovery: %v errors %v", err, rep.Errors)
	}
	if !sameStrings(callIDs(rep.UnknownRequests), []string{string(a.CallID)}) {
		t.Errorf("unknown = %v, want A", rep.UnknownRequests)
	}
	wantStopped := []string{string(paused.CallID), string(tool.CallID)}
	if string(paused.CallID) > string(tool.CallID) {
		wantStopped = []string{string(tool.CallID), string(paused.CallID)}
	}
	if !sameStrings(callIDs(rep.StoppedRequests), wantStopped) {
		t.Errorf("stopped = %v, want %v", rep.StoppedRequests, wantStopped)
	}
	wantRuns := []string{run1.RunID, run2.RunID}
	if run1.RunID > run2.RunID {
		wantRuns = []string{run2.RunID, run1.RunID}
	}
	if !sameStrings(rep.StoppedRuns, wantRuns) {
		t.Errorf("stopped runs = %v, want %v", rep.StoppedRuns, wantRuns)
	}

	aNow := h.storedReq(a.CallID)
	if aNow.State != consts.RequestUnknown || aNow.Execution == nil || aNow.Execution.Outcome != consts.RequestUnknown {
		t.Fatalf("A = %s %+v", aNow.State, aNow.Execution)
	}
	for _, id := range []ids.CallID{paused.CallID, tool.CallID} {
		if r := h.storedReq(id); r.State != consts.RequestStopped || r.Stop == nil || r.Stop.Reason != consts.StopRecoveryInterrupted {
			t.Fatalf("%s = %s stop %+v", id, r.State, r.Stop)
		}
	}
	for _, id := range []string{run1.RunID, run2.RunID} {
		r := h.storedRun(id)
		if r.State != consts.RunRecoveryInterrupted || r.Stop == nil || r.Stop.Reason != consts.StopRecoveryInterrupted {
			t.Fatalf("run %s = %s stop %+v", id, r.State, r.Stop)
		}
	}
	if r := h.storedRun(run1.RunID); r.Steps[0].State != consts.RequestUnknown {
		t.Errorf("run1 step A record = %s, want unknown", r.Steps[0].State)
	}
	if r := h.storedRun(run2.RunID); r.Steps[0].State != consts.RequestStopped {
		t.Errorf("run2 step A record = %s, want stopped", r.Steps[0].State)
	}

	// 恢复事件经 EventSink，状态事件随保存。
	if got := streamTypes(h.env.Sink.Events(), recovery.StreamPrefix+rep.RecoveryID); !sameTypes(got, []events.Type{events.RecoveryStarted, events.RecoveryCompleted}) {
		t.Errorf("recovery events = %v", got)
	}
	all := h.env.Log.All()
	if got := streamTypes(all, events.CallStreamPrefix+string(a.CallID)); got[len(got)-1] != events.ExecutionUnknown {
		t.Errorf("A events end with %v, want execution.unknown", got[len(got)-1])
	}
	for _, id := range []ids.CallID{paused.CallID, tool.CallID} {
		if got := streamTypes(all, events.CallStreamPrefix+string(id)); got[len(got)-1] != events.CallStopped {
			t.Errorf("%s events end with %v, want call.stopped", id, got[len(got)-1])
		}
	}
	for _, id := range []string{run1.RunID, run2.RunID} {
		if got := streamTypes(all, events.RunStreamPrefix+id); got[len(got)-1] != events.TaskStopped {
			t.Errorf("run %s events end with %v, want task.stopped", id, got[len(got)-1])
		}
	}

	// 之后什么都不会执行：执行、审核、推进都被拒，执行器调用次数不变。
	for _, id := range []ids.CallID{a.CallID, paused.CallID, tool.CallID} {
		if _, err := h.svc.Execute(ctx, id); err == nil {
			t.Errorf("Execute(%s) succeeded after recovery", id)
		}
		_, err := h.svc.Review(ctx, id, pipeline.ReviewInput{Action: consts.ActionExecute, Revision: 1, Digest: h.storedReq(id).Digest, Actor: "alice"})
		if err == nil {
			t.Errorf("Review(%s) succeeded after recovery", id)
		}
	}
	for _, id := range []string{run1.RunID, run2.RunID} {
		r := h.storedRun(id)
		if _, _, err := h.eng.Advance(ctx, id, h.storedReq(r.Steps[0].CallID)); err == nil {
			t.Errorf("Advance(%s) succeeded after recovery", id)
		}
	}
	if h.env.Executor.Calls() != execBefore {
		t.Fatalf("executor called %d times after recovery, want %d", h.env.Executor.Calls(), execBefore)
	}
	// 重复恢复什么都不改。
	again, err := recovery.OnStartup(ctx, h.recoveryDeps())
	if err != nil || len(again.StoppedRequests)+len(again.UnknownRequests)+len(again.StoppedRuns)+len(again.Errors) != 0 {
		t.Fatalf("second recovery = %+v, %v", again, err)
	}
}

func callIDs(in []ids.CallID) []string {
	out := make([]string, len(in))
	for i, id := range in {
		out[i] = string(id)
	}
	return out
}
