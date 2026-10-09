package flowtest

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/events"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/orchestrator"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/pipeline"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/ports"
	mreq "github.com/LeXwDeX/DevOps-Agent/platform/internal/model/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/task"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/consts"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

// 这个文件补 core 层联合路径里的缺口：取消各种状态的步骤、补输入后跑完、
// 两个调用方同时推进同一步骤、取消目标已变化的申请。

// wantCancelled 检查取消后的运行：状态 cancelled，原因 cancelled_by_human，步骤记录为 stopped。
func wantCancelled(t *testing.T, run *task.Run, stepID string) {
	t.Helper()
	if run.State != consts.RunCancelled || run.Stop == nil || run.Stop.Reason != consts.StopCancelledByHuman || run.Stop.StepID != stepID {
		t.Fatalf("run = %s stop %+v", run.State, run.Stop)
	}
	if last := run.Steps[len(run.Steps)-1]; last.StepID != stepID || last.State != consts.RequestStopped {
		t.Fatalf("step record = %+v", last)
	}
}

// 取消一个已冻结、还没执行的步骤：申请停止，之后不能执行。
func TestCancelRunWithFrozenStep(t *testing.T) {
	h := newHarness(t)
	run, req := h.start()
	a, err := h.svc.Inspect(ctx, req.CallID)
	if err != nil || a.State != consts.RequestFrozen {
		t.Fatalf("Inspect: %v %s", err, a.State)
	}
	run, _ = h.advance(run, a)

	run, err = h.eng.Cancel(ctx, run.RunID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	wantCancelled(t, run, "A")
	stored := h.storedReq(a.CallID)
	if stored.State != consts.RequestStopped || stored.Stop == nil || stored.Stop.Reason != consts.StopCancelledByHuman {
		t.Fatalf("A = %s stop %+v", stored.State, stored.Stop)
	}

	// 取消之后不会执行：Execute 被拒，执行器一次都没有被调用。
	if _, err := h.svc.Execute(ctx, a.CallID); err == nil {
		t.Fatal("Execute succeeded on a cancelled step")
	}
	if err := h.svc.PreExecute(ctx, a.CallID); err == nil {
		t.Fatal("PreExecute succeeded on a cancelled step")
	}
	if h.env.Executor.Calls() != 0 {
		t.Fatalf("executor called %d times", h.env.Executor.Calls())
	}
	wantA := []events.Type{
		events.CallProposed, events.InspectionStarted, events.InspectionPassed, events.PolicyDecided,
		events.CallFrozen, events.CallStopped,
	}
	if got := streamTypes(h.env.Log.All(), events.CallStreamPrefix+string(a.CallID)); !sameTypes(got, wantA) {
		t.Errorf("A events = %v, want %v", got, wantA)
	}
	wantRun := []events.Type{events.TaskStarted, events.TaskStopped}
	if got := streamTypes(h.env.Log.All(), events.RunStreamPrefix+run.RunID); !sameTypes(got, wantRun) {
		t.Errorf("run events = %v, want %v", got, wantRun)
	}
}

// 取消一个待人工审核的步骤：申请停止，之后人工 ▶ 也执行不了。
func TestCancelRunWithAwaitingReviewStep(t *testing.T) {
	h := newHarness(t)
	h.setStepPolicy("A", consts.PolicyManual)
	run, req := h.start()
	run, paused := h.drive(run, req)
	if paused == nil || paused.State != consts.RequestAwaitingReview || run.State != consts.RunAwaitingReview {
		t.Fatalf("run = %s paused %+v", run.State, paused)
	}

	run, err := h.eng.Cancel(ctx, run.RunID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	wantCancelled(t, run, "A")
	stored := h.storedReq(paused.CallID)
	if stored.State != consts.RequestStopped || stored.Stop == nil || stored.Stop.Reason != consts.StopCancelledByHuman {
		t.Fatalf("A = %s stop %+v", stored.State, stored.Stop)
	}

	_, err = h.svc.Review(ctx, paused.CallID, pipeline.ReviewInput{
		Action: consts.ActionExecute, Revision: paused.Revision, Digest: paused.Digest, Actor: "alice",
	})
	if err == nil {
		t.Fatal("Review ▶ succeeded on a cancelled step")
	}
	if _, err := h.svc.Execute(ctx, paused.CallID); err == nil {
		t.Fatal("Execute succeeded on a cancelled step")
	}
	if h.env.Executor.Calls() != 0 {
		t.Fatalf("executor called %d times", h.env.Executor.Calls())
	}
	if got := h.storedReq(paused.CallID); len(got.Reviews) != 0 {
		t.Errorf("a refused approval was recorded: %+v", got.Reviews)
	}
}

// 取消一个目标已变化（执行前指纹不一致）、运行还没来得及推进的步骤。
func TestCancelRunWithTargetChangedStep(t *testing.T) {
	h := newHarness(t)
	h.env.Fingerprint.PushValue("fp-1") // 检验
	h.env.Fingerprint.PushValue("fp-2") // 执行前：目标变了
	run, req := h.start()
	a, err := h.svc.Inspect(ctx, req.CallID)
	if err != nil || a.State != consts.RequestFrozen {
		t.Fatalf("Inspect: %v %s", err, a.State)
	}
	run, _ = h.advance(run, a)
	changed, err := h.svc.Execute(ctx, a.CallID)
	if code, _ := errcode.CodeOf(err); code != errcode.FingerprintChanged || changed == nil || changed.State != consts.RequestTargetChanged {
		t.Fatalf("Execute = %+v, %v", changed, err)
	}
	// 引擎还不知道申请已是 target_changed；此时人工取消运行。
	if run = h.storedRun(run.RunID); run.State != consts.RunRunning {
		t.Fatalf("run = %s", run.State)
	}

	run, err = h.eng.Cancel(ctx, run.RunID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	wantCancelled(t, run, "A")
	stored := h.storedReq(a.CallID)
	if stored.State != consts.RequestStopped || stored.Stop == nil || stored.Stop.Reason != consts.StopCancelledByHuman {
		t.Fatalf("A = %s stop %+v", stored.State, stored.Stop)
	}
	if h.env.Executor.Calls() != 0 {
		t.Fatalf("executor called %d times", h.env.Executor.Calls())
	}
	wantA := []events.Type{
		events.CallProposed, events.InspectionStarted, events.InspectionPassed, events.PolicyDecided,
		events.CallFrozen, events.CallTargetChanged, events.CallStopped,
	}
	if got := streamTypes(h.env.Log.All(), events.CallStreamPrefix+string(a.CallID)); !sameTypes(got, wantA) {
		t.Errorf("A events = %v, want %v", got, wantA)
	}
	// 运行已取消，不能再推进。
	if _, _, err := h.eng.Advance(ctx, run.RunID, stored); err == nil {
		t.Fatal("Advance succeeded on a cancelled run")
	}
}

// keyedTaskDef 是 taskDef 的变体：多一个必需参数 port，并需要凭证库里有 TOKEN。
func keyedTaskDef() *task.Definition {
	d := taskDef()
	d.ID = "deploy-keyed"
	d.Params = append(d.Params, task.FieldSpec{Name: "port", Type: task.FieldType{Kind: task.KindNumber}, Required: true})
	d.SecretKeys = []string{"TOKEN"}
	return d
}

// awaiting_input → 补参数 → 补 K → 补输入 → 运行 → 跑完。
func TestAwaitingInputThenProvideInputRunsToCompletion(t *testing.T) {
	h := newHarness(t)
	h.env.Tasks.SeedDefinition(keyedTaskDef(), true)
	h.env.Executor.PushOutput(nil)
	h.env.Executor.PushOutput(nil)
	h.env.Executor.PushOutput(map[string]any{"status": "ok"})

	run, req, err := h.eng.Start(ctx, orchestrator.StartInput{
		TaskID: "deploy-keyed", SessionID: session,
		Scope:  mreq.Scope{Project: project, Environment: envName, Targets: []string{target}},
		Params: map[string]any{"svc": "nginx"},
	})
	if err != nil || req != nil {
		t.Fatalf("Start: %v req %v", err, req)
	}
	if run.State != consts.RunAwaitingInput || !sameStrings(run.MissingParams, []string{"port"}) || !sameStrings(run.MissingKeys, []string{"TOKEN"}) {
		t.Fatalf("run = %+v", run)
	}
	if h.env.Executor.Calls() != 0 || len(h.env.Log.All()) != 0 {
		t.Fatal("awaiting_input run executed or produced events")
	}

	// 补了参数，K 还缺：仍在等输入。
	run, req, err = h.eng.ProvideInput(ctx, run.RunID, map[string]any{"port": 8080})
	if err != nil || req != nil || run.State != consts.RunAwaitingInput || len(run.MissingParams) != 0 || !sameStrings(run.MissingKeys, []string{"TOKEN"}) {
		t.Fatalf("after params: run %+v req %v err %v", run, req, err)
	}

	// K 在凭证库补好，再补一次输入（不带参数）：运行开始。
	h.env.Secrets.AddKeys(project, envName, "TOKEN")
	run, req, err = h.eng.ProvideInput(ctx, run.RunID, nil)
	if err != nil || req == nil || run.State != consts.RunRunning || run.Current != "A" {
		t.Fatalf("after keys: run %+v req %v err %v", run, req, err)
	}

	run, left := h.drive(run, req)
	if left != nil || run.State != consts.RunCompleted {
		t.Fatalf("run = %s left %v", run.State, left)
	}
	if got := stepIDs(run); !sameStrings(got, []string{"A", "B", "D", "E"}) {
		t.Fatalf("steps = %v", got)
	}
	if got := h.executedSteps(); !sameStrings(got, []string{"A", "B", "D", "E"}) {
		t.Fatalf("executor ran %v", got)
	}
	if run.Params["svc"] != "nginx" || run.Params["port"] != 8080 {
		t.Errorf("params = %v", run.Params)
	}
	wantRun := []events.Type{
		events.TaskStarted,
		events.TaskStepCompleted, events.TaskStepCompleted, events.TaskStepCompleted, events.TaskStepCompleted,
		events.TaskCompleted,
	}
	if got := streamTypes(h.env.Log.All(), events.RunStreamPrefix+run.RunID); !sameTypes(got, wantRun) {
		t.Errorf("run events = %v, want %v", got, wantRun)
	}
}

var errBarrierTimeout = errors.New("flowtest: the other racing caller never arrived")

// barrierTasks 让两个并发的 GetRun 都读到同一个版本，再一起往下走：
// 读完才等待，所以两个调用方一定持有同一个旧版本，竞争落在 SaveRun 的 CAS 上。
type barrierTasks struct {
	ports.TaskStore
	arrived atomic.Int32
	ready   chan struct{}
}

func (b *barrierTasks) GetRun(c context.Context, runID string) (*task.Run, error) {
	run, err := b.TaskStore.GetRun(c, runID)
	if err != nil {
		return nil, err
	}
	n := b.arrived.Add(1)
	if n > 2 {
		return run, nil // 只拦前两个调用
	}
	if n == 2 {
		close(b.ready)
	}
	select {
	case <-b.ready:
		return run, nil
	case <-time.After(5 * time.Second):
		return nil, errBarrierTimeout
	}
}

// 两个调用方同时推进同一个已成功的步骤：只有一个赢（CAS），另一个得到 core.cas_conflict，
// 运行里只有一个下一步，存储里也只有一个下一步的草稿申请。
func TestConcurrentAdvanceOnlyOneWins(t *testing.T) {
	h := newHarness(t)
	run, req := h.start()
	a, err := h.svc.Inspect(ctx, req.CallID)
	if err != nil || a.State != consts.RequestFrozen {
		t.Fatalf("Inspect: %v %s", err, a.State)
	}
	run, _ = h.advance(run, a)
	done := h.execute(a)
	if done.State != consts.RequestSucceeded {
		t.Fatalf("A = %s", done.State)
	}

	racing, err := orchestrator.New(orchestrator.Deps{
		Tasks:    &barrierTasks{TaskStore: h.env.Tasks, ready: make(chan struct{})},
		Requests: h.store, Registry: h.env.Registry, Secrets: h.env.Secrets, Clock: h.env.Clock, IDGen: h.env.IDs,
	})
	if err != nil {
		t.Fatal(err)
	}

	type outcome struct {
		next *mreq.ExecutionRequest
		err  error
	}
	results := make([]outcome, 2)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, next, err := racing.Advance(ctx, run.RunID, done)
			results[i] = outcome{next, err}
		}()
	}
	wg.Wait()

	var winners, conflicts int
	for _, r := range results {
		switch code, _ := errcode.CodeOf(r.err); {
		case r.err == nil && r.next != nil:
			winners++
		case code == errcode.CASConflict && r.next == nil:
			conflicts++
		default:
			t.Errorf("unexpected outcome: next %v err %v", r.next, r.err)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("winners %d conflicts %d, want 1 and 1", winners, conflicts)
	}

	stored := h.storedRun(run.RunID)
	if got := stepIDs(stored); !sameStrings(got, []string{"A", "B"}) || stored.Current != "B" || stored.State != consts.RunRunning {
		t.Fatalf("run = %s current %s steps %v", stored.State, stored.Current, got)
	}
	open, err := h.env.Requests.ListNonTerminal(ctx)
	if err != nil || len(open) != 1 || open[0].CallID != stored.Steps[1].CallID || open[0].State != consts.RequestDraft {
		t.Fatalf("open requests = %+v, %v", open, err)
	}
	wantRun := []events.Type{events.TaskStarted, events.TaskStepCompleted}
	if got := streamTypes(h.env.Log.All(), events.RunStreamPrefix+run.RunID); !sameTypes(got, wantRun) {
		t.Errorf("run events = %v, want %v", got, wantRun)
	}
}
