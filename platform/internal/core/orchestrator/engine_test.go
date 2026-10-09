package orchestrator

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/events"
	mreq "github.com/LeXwDeX/DevOps-Agent/platform/internal/model/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/task"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/consts"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

func TestNewRequiresDeps(t *testing.T) {
	_, err := New(Deps{})
	mustCode(t, err, errcode.InvalidArgument)
}

func TestStartCreatesEntryRequest(t *testing.T) {
	f := newFixture(t)
	run, req := f.start()

	if run.State != consts.RunRunning || run.Current != "A" || run.TaskVersion != 1 || run.TaskDigest != diamondDef(1).Digest {
		t.Fatalf("run = %+v", run)
	}
	if len(run.Steps) != 1 || run.Steps[0].CallID != req.CallID || run.Steps[0].State != consts.RequestDraft {
		t.Fatalf("steps = %+v", run.Steps)
	}
	if req.State != consts.RequestDraft || req.Source != consts.SourceTaskStep || req.Task.RunID != run.RunID || req.Task.StepID != "A" ||
		req.SessionID != "s1" || req.Revision != 1 || req.Version != 1 {
		t.Fatalf("req = %+v", req)
	}
	if sr := req.Submission.SelfReview; sr == nil || sr.Verdict != consts.VerdictNotApplicable {
		t.Fatalf("self review = %+v", sr)
	}
	if req.Content.Unit == nil || req.Content.Unit.ID != "ua" {
		t.Fatalf("unit not taken from the definition: %+v", req.Content.Unit)
	}
	// 持久化的内容与返回值一致
	if !reflect.DeepEqual(f.storedRun(run.RunID), run) || !reflect.DeepEqual(f.storedReq(req.CallID), req) {
		t.Fatal("stored copies differ from returned values")
	}
	// 事件：运行流 task.started，申请流 call.proposed；运行先于申请
	got := f.env.Log.All()
	if len(got) != 2 || got[0].Type != events.TaskStarted || got[0].StreamID != "run:"+run.RunID || got[0].Sequence != 1 ||
		got[1].Type != events.CallProposed || got[1].StreamID != "call:"+string(req.CallID) || got[1].Sequence != 1 {
		t.Fatalf("events = %+v", got)
	}
	if got[0].Data["task_digest"] != string(run.TaskDigest) || got[0].Data["task_version"] != int64(1) {
		t.Errorf("started data = %v", got[0].Data)
	}
	if run.EventSeq != 1 || req.EventSeq != 1 {
		t.Errorf("event seq: run %d req %d", run.EventSeq, req.EventSeq)
	}
	// 渲染收到的是任务参数，还没有前序输出
	if r := f.lastRender(); r.step != "A" || r.params["svc"] != "nginx" || len(r.outputs) != 0 {
		t.Errorf("render = %+v", r)
	}
}

func TestStartErrors(t *testing.T) {
	f := newFixture(t)
	in := input()
	in.SessionID = ""
	_, _, err := f.eng.Start(ctx, in)
	mustCode(t, err, errcode.InvalidArgument)
	in = input()
	in.TaskID = "ghost"
	_, _, err = f.eng.Start(ctx, in)
	mustCode(t, err, errcode.NotFound)
	in = input()
	in.Version = 9
	_, _, err = f.eng.Start(ctx, in)
	mustCode(t, err, errcode.NotFound)

	// 登记里混进了不合格的定义：启动前再检一次
	bad := diamondDef(1)
	bad.ID = "bad"
	bad.Steps[3].Branches[0].When = `D.status == 1`
	f.env.Tasks.SeedDefinition(bad, true)
	in = input()
	in.TaskID = "bad"
	_, _, err = f.eng.Start(ctx, in)
	mustCode(t, err, errcode.TaskInvalid)

	if n := len(f.env.Log.All()); n != 0 {
		t.Errorf("failed starts produced %d events", n)
	}
}

func TestStartParamValidation(t *testing.T) {
	f := newFixture(t)
	cases := map[string]map[string]any{
		"unknown param": {"svc": "x", "extra": 1},
		"wrong kind":    {"svc": 5},
		"nil value":     {"svc": nil},
		"bad list elem": {"svc": "x", "hosts": []any{"a", 1}},
		"list not list": {"svc": "x", "hosts": "a"},
	}
	for name, p := range cases {
		in := input()
		in.Params = p
		_, _, err := f.eng.Start(ctx, in)
		if codeOf(err) != errcode.InvalidArgument {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	// 合法的列表写法
	for _, hosts := range []any{[]any{"a", "b"}, []string{"a"}, []any{}} {
		in := input()
		in.Params = map[string]any{"svc": "x", "hosts": hosts}
		if _, _, err := f.eng.Start(ctx, in); err != nil {
			t.Errorf("hosts %v: %v", hosts, err)
		}
	}
}

func TestBranchRoutingOKPath(t *testing.T) {
	f := newFixture(t)
	run, req := f.start()
	run, path := f.walk(run, req, map[string]map[string]any{
		"A": {"n": 3},
		"D": {"status": "ok", "exit_code": 0},
	})
	if got := strings.Join(path, ""); got != "ABCDEFG" {
		t.Fatalf("path = %s", got)
	}
	if run.State != consts.RunCompleted || run.Stop != nil {
		t.Fatalf("run = %+v", run)
	}
	if !reflect.DeepEqual(f.storedRun(run.RunID), run) {
		t.Fatal("stored run differs")
	}
	// 渲染 E 时能看到前序输出
	var seenD map[string]any
	for _, r := range f.renders {
		if r.step == "E" {
			seenD = r.outputs["D"]
		}
	}
	if seenD["status"] != "ok" {
		t.Errorf("render E saw outputs %v", seenD)
	}

	// 运行流事件：started, 7 个 step_completed, completed；序号连续
	var runEvts []events.Event
	for _, e := range f.env.Log.All() {
		if e.StreamID == "run:"+run.RunID {
			runEvts = append(runEvts, e)
		}
	}
	if len(runEvts) != 9 || runEvts[0].Type != events.TaskStarted || runEvts[8].Type != events.TaskCompleted {
		t.Fatalf("run events: %d", len(runEvts))
	}
	for i, e := range runEvts {
		if e.Sequence != int64(i+1) {
			t.Errorf("event %d has sequence %d", i, e.Sequence)
		}
		if i > 0 && i < 8 && e.Type != events.TaskStepCompleted {
			t.Errorf("event %d = %s", i, e.Type)
		}
	}
	if runEvts[4].Data["step_id"] != "D" || runEvts[4].Data["next"] != "E" || runEvts[4].Data["state"] != "succeeded" {
		t.Errorf("D completed data = %v", runEvts[4].Data)
	}
	if runEvts[8].Status != "completed" || run.EventSeq != 9 {
		t.Errorf("completed status %q seq %d", runEvts[8].Status, run.EventSeq)
	}
	// 事件里只有输出字段名，没有值
	if f := runEvts[4].Data["output_fields"].([]any); len(f) != 2 || f[0] != "exit_code" || f[1] != "status" {
		t.Errorf("output_fields = %v", f)
	}
	if v, has := runEvts[4].Data["output"]; has || v != nil || strings.Contains(fmt.Sprint(runEvts[4].Data["output_fields"]), "ok") {
		t.Error("output values leaked into the event")
	}
}

func TestBranchRoutingDegradedPath(t *testing.T) {
	f := newFixture(t)
	run, req := f.start()
	run, path := f.walk(run, req, map[string]map[string]any{"D": {"status": "degraded", "exit_code": 2}})
	if got := strings.Join(path, ""); got != "ABCDHLM" || run.State != consts.RunCompleted {
		t.Fatalf("path = %s state = %s", got, run.State)
	}
}

func TestNoBranchMatched(t *testing.T) {
	f := newFixture(t)
	run, req := f.start()
	run, path := f.walk(run, req, map[string]map[string]any{"D": {"status": "weird", "exit_code": 0}})
	if strings.Join(path, "") != "ABCD" {
		t.Fatalf("path = %v", path)
	}
	if run.State != consts.RunStopped || run.Stop.Reason != consts.StopNoBranchMatched || run.Stop.StepID != "D" {
		t.Fatalf("run = %+v stop = %+v", run, run.Stop)
	}
	evts := types(f.env.Tasks.Events(), "run:"+run.RunID)
	n := len(evts)
	if evts[n-2] != events.TaskStepCompleted || evts[n-1] != events.TaskStopped {
		t.Fatalf("events = %v", evts)
	}
	last := f.env.Tasks.Events()
	if last[len(last)-1].Data["reason"] != "no_branch_matched" || last[len(last)-1].Status != "stopped" {
		t.Errorf("stopped event = %+v", last[len(last)-1])
	}
	// 没有再创建下一步申请
	if len(f.env.Requests.Events()) != 4 {
		t.Errorf("request events = %d, want 4 proposals", len(f.env.Requests.Events()))
	}
}

func TestBranchEvalErrorStopsRun(t *testing.T) {
	cases := map[string]struct {
		out  map[string]any
		code errcode.Code
	}{
		"missing field": {map[string]any{"status": "ok"}, errcode.ExprMissingField}, // 没有 exit_code
		"no output":     {nil, errcode.ExprMissingField},
		"wrong type":    {map[string]any{"status": 5, "exit_code": 0}, errcode.ExprType},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			run, req := f.start()
			run, _ = f.walk(run, req, map[string]map[string]any{"D": c.out})
			if run.State != consts.RunStopped || run.Stop.Reason != consts.StopNoBranchMatched || run.Stop.StepID != "D" {
				t.Fatalf("run = %+v", run)
			}
			if !strings.Contains(run.Stop.Detail, string(c.code)) {
				t.Errorf("detail %q lacks %s", run.Stop.Detail, c.code)
			}
			if len(f.env.Requests.Events()) != 4 {
				t.Error("a next step was created")
			}
		})
	}
}

func TestBranchStopsAtFirstEvalError(t *testing.T) {
	// 第一条分支缺字段：即使第二条为真也不选，缺字段不是"假"
	d := diamondDef(1)
	d.Steps[3].Branches = []task.Branch{
		{When: `D.exit_code == 0`, To: "E"},
		{When: `D.status == "ok"`, To: "H"},
	}
	f := newFixture(t, d)
	run, req := f.start()
	run, _ = f.walk(run, req, map[string]map[string]any{"D": {"status": "ok"}})
	if run.State != consts.RunStopped || !strings.Contains(run.Stop.Detail, "core.expr_missing_field") {
		t.Fatalf("run = %+v", run.Stop)
	}
}

func TestStepFailureStopsRun(t *testing.T) {
	cases := []struct {
		state  consts.RequestState
		reason consts.StopReason
		mut    func(*mreq.ExecutionRequest)
		detail string
	}{
		{consts.RequestMismatched, consts.StopMismatched, func(r *mreq.ExecutionRequest) { r.Execution = &mreq.Execution{Detail: "rows differ"} }, "rows differ"},
		{consts.RequestUnknown, consts.StopUnknown, nil, ""},
		{consts.RequestFailed, consts.StopFailed, func(r *mreq.ExecutionRequest) {
			r.Execution = &mreq.Execution{Result: &mreq.ExecutionResult{Err: "exit 3"}}
		}, "exit 3"},
		{consts.RequestRejected, consts.StopRejected, func(r *mreq.ExecutionRequest) {
			r.Rejection = &mreq.Rejection{Source: consts.RejectedByHuman, Reason: "no"}
		}, "no"},
	}
	for _, c := range cases {
		t.Run(string(c.state), func(t *testing.T) {
			f := newFixture(t)
			run, req := f.start()
			run, req = f.succeed(run, req, map[string]any{"n": 1}) // A 成功，到 B
			r := f.setState(req, c.state, c.mut)
			run, next, err := f.eng.Advance(ctx, run.RunID, r)
			if err != nil || next != nil {
				t.Fatalf("err = %v next = %v", err, next)
			}
			if run.State != consts.RunStopped || run.Stop.Reason != c.reason || run.Stop.StepID != "B" || !strings.Contains(run.Stop.Detail, c.detail) {
				t.Fatalf("stop = %+v", run.Stop)
			}
			if run.Steps[1].State != c.state {
				t.Errorf("step record = %s", run.Steps[1].State)
			}
			if !reflect.DeepEqual(f.storedRun(run.RunID), run) {
				t.Error("not saved")
			}
			evts := f.env.Tasks.Events()
			if last := evts[len(evts)-1]; last.Type != events.TaskStopped || last.Data["reason"] != string(c.reason) || last.Data["step_id"] != "B" {
				t.Errorf("last event = %+v", last)
			}
			// 失败的步骤没有 step_completed
			for _, e := range evts {
				if e.Type == events.TaskStepCompleted && e.Data["step_id"] == "B" {
					t.Error("step_completed for a failed step")
				}
			}
		})
	}
}

func TestStepStoppedStopsRunWithStepReason(t *testing.T) {
	for _, reason := range []consts.StopReason{consts.StopCancelledByHuman, consts.StopRecoveryInterrupted} {
		f := newFixture(t)
		run, req := f.start()
		r := f.setState(req, consts.RequestStopped, func(r *mreq.ExecutionRequest) {
			r.Stop = &mreq.Stop{Reason: reason, Detail: "d"}
		})
		run, _, err := f.eng.Advance(ctx, run.RunID, r)
		if err != nil || run.State != consts.RunStopped || run.Stop.Reason != reason || run.Stop.Detail != "d" {
			t.Fatalf("%s: err %v run %+v", reason, err, run.Stop)
		}
	}
	// 没有停止原因的 stopped 申请是脏数据
	f := newFixture(t)
	run, req := f.start()
	_, _, err := f.eng.Advance(ctx, run.RunID, f.setState(req, consts.RequestStopped, nil))
	mustCode(t, err, errcode.InvalidArgument)
}

func TestTargetChangedStopsRunAndRequest(t *testing.T) {
	f := newFixture(t)
	run, req := f.start()
	r := f.setState(req, consts.RequestTargetChanged, nil)
	got, next, err := f.eng.Advance(ctx, run.RunID, r)
	if err != nil || next != nil {
		t.Fatalf("err = %v", err)
	}
	if got.State != consts.RunStopped || got.Stop.Reason != consts.StopTargetChanged || got.Stop.StepID != "A" {
		t.Fatalf("run stop = %+v", got.Stop)
	}
	sr := f.storedReq(req.CallID)
	if sr.State != consts.RequestStopped || sr.Stop == nil || sr.Stop.Reason != consts.StopTargetChanged || sr.Version != r.Version+1 {
		t.Fatalf("stored request = %+v", sr)
	}
	// 申请流的事件：proposed 之后是 call.stopped；先停申请，再写运行
	log := f.env.Log.All()
	tail := log[len(log)-2:]
	if tail[0].Type != events.CallStopped || tail[0].StreamID != "call:"+string(req.CallID) || tail[0].Sequence != 2 ||
		tail[1].Type != events.TaskStopped {
		t.Fatalf("tail events = %+v", tail)
	}
	if tail[0].Status != "stopped" || tail[0].Data["reason"] != "target_changed" {
		t.Errorf("call.stopped = %+v", tail[0])
	}
	// 运行已终态，再推进被拒
	_, _, err = f.eng.Advance(ctx, run.RunID, sr)
	mustCode(t, err, errcode.RunInvalidState)
}

func TestTargetChangedRequestCASConflictLeavesRunAlone(t *testing.T) {
	f := newFixture(t)
	run, req := f.start()
	r := f.setState(req, consts.RequestTargetChanged, nil)
	stale := r.Clone()
	stale.Version-- // 调用方手里的版本落后了
	_, _, err := f.eng.Advance(ctx, run.RunID, stale)
	mustCode(t, err, errcode.CASConflict)
	if f.storedRun(run.RunID).State != consts.RunRunning {
		t.Error("run changed although the request stop failed")
	}
}

func TestRunBoundToVersion(t *testing.T) {
	v2 := diamondDef(2)
	v2.Steps[0].Next = "G" // v2: A 之后直接到 G
	f := newFixture(t, diamondDef(1), v2)
	run, req := f.start()
	if run.TaskVersion != 1 {
		t.Fatal("started on wrong version")
	}
	f.env.Tasks.SetActive("deploy", 2) // 在途时切换启用版本
	run, next := f.succeed(run, req, map[string]any{"n": 1})
	if next.Task.StepID != "B" || run.TaskVersion != 1 {
		t.Fatalf("in-flight run followed the new version: next = %s", next.Task.StepID)
	}
	// 新运行用 v2
	run2, req2, err := f.eng.Start(ctx, input())
	if err != nil || run2.TaskVersion != 2 || run2.TaskDigest != v2.Digest {
		t.Fatalf("run2 = %+v err %v", run2, err)
	}
	_, next2 := f.succeed(run2, req2, map[string]any{"n": 1})
	if next2.Task.StepID != "G" {
		t.Fatalf("v2 next = %s", next2.Task.StepID)
	}
	// 显式指定旧版本
	in := input()
	in.Version = 1
	run3, _, err := f.eng.Start(ctx, in)
	if err != nil || run3.TaskVersion != 1 {
		t.Fatalf("explicit version: %+v %v", run3, err)
	}
}

func TestRunDigestChangeIsDetected(t *testing.T) {
	f := newFixture(t)
	run, req := f.start()
	changed := diamondDef(1)
	changed.Digest = diamondDef(2).Digest
	f.env.Tasks.SeedDefinition(changed, true) // 同版本被篡改
	r := f.setState(req, consts.RequestSucceeded, nil)
	_, _, err := f.eng.Advance(ctx, run.RunID, r)
	mustCode(t, err, errcode.DigestMismatch)
}

func TestAwaitingInputThenProvide(t *testing.T) {
	d := diamondDef(1)
	d.Params = append(d.Params, task.FieldSpec{Name: "port", Type: num(), Required: true})
	f := newFixture(t, d)

	in := input() // 只给了 svc
	run, req, err := f.eng.Start(ctx, in)
	if err != nil || req != nil {
		t.Fatalf("err %v req %v", err, req)
	}
	if run.State != consts.RunAwaitingInput || !reflect.DeepEqual(run.MissingInputs, []string{"port"}) || run.Current != "" {
		t.Fatalf("run = %+v", run)
	}
	if len(f.env.Log.All()) != 0 || f.renderCount() != 0 {
		t.Fatal("awaiting_input run produced events or rendered a step")
	}
	if !reflect.DeepEqual(f.storedRun(run.RunID), run) {
		t.Fatal("not stored")
	}
	// 在 awaiting_input 时推进、取消以外的操作
	_, _, err = f.eng.Advance(ctx, run.RunID, &mreq.ExecutionRequest{CallID: "x", State: consts.RequestDraft})
	mustCode(t, err, errcode.RunInvalidState)

	// 补错：未知名、类型不符
	_, _, err = f.eng.ProvideInput(ctx, run.RunID, map[string]any{"nope": 1})
	mustCode(t, err, errcode.InvalidArgument)
	_, _, err = f.eng.ProvideInput(ctx, run.RunID, map[string]any{"port": "80"})
	mustCode(t, err, errcode.InvalidArgument)
	if f.storedRun(run.RunID).Version != 1 {
		t.Error("failed ProvideInput wrote the run")
	}

	// 补齐
	run, req, err = f.eng.ProvideInput(ctx, run.RunID, map[string]any{"port": 80})
	if err != nil || req == nil {
		t.Fatalf("err %v req %v", err, req)
	}
	if run.State != consts.RunRunning || run.Current != "A" || len(run.MissingInputs) != 0 || run.Params["svc"] != "nginx" || run.Params["port"] != 80 {
		t.Fatalf("run = %+v", run)
	}
	if run.Version != 2 || !reflect.DeepEqual(f.storedRun(run.RunID), run) {
		t.Fatalf("version %d / not stored", run.Version)
	}
	log := f.env.Log.All()
	if len(log) != 2 || log[0].Type != events.TaskStarted || log[0].Sequence != 1 || log[1].Type != events.CallProposed {
		t.Fatalf("events = %+v", log)
	}
	// 已在运行时再补
	_, _, err = f.eng.ProvideInput(ctx, run.RunID, map[string]any{"port": 81})
	mustCode(t, err, errcode.RunInvalidState)
}

func TestProvideInputPartial(t *testing.T) {
	d := diamondDef(1)
	d.Params = append(d.Params,
		task.FieldSpec{Name: "port", Type: num(), Required: true},
		task.FieldSpec{Name: "zone", Type: str(), Required: true})
	f := newFixture(t, d)
	run, _, err := f.eng.Start(ctx, input())
	if err != nil || !reflect.DeepEqual(run.MissingInputs, []string{"port", "zone"}) {
		t.Fatalf("run %+v err %v", run, err)
	}
	run, req, err := f.eng.ProvideInput(ctx, run.RunID, map[string]any{"port": 1})
	if err != nil || req != nil || run.State != consts.RunAwaitingInput || !reflect.DeepEqual(run.MissingInputs, []string{"zone"}) {
		t.Fatalf("run %+v req %v err %v", run, req, err)
	}
	if got := f.storedRun(run.RunID); got.Params["port"] != 1 || got.Version != 2 {
		t.Fatalf("stored = %+v", got)
	}
	if len(f.env.Log.All()) != 0 {
		t.Error("partial input produced events")
	}
	_, req, err = f.eng.ProvideInput(ctx, run.RunID, map[string]any{"zone": "z"})
	if err != nil || req == nil {
		t.Fatalf("complete: %v", err)
	}
}

func TestAwaitingReviewMapping(t *testing.T) {
	f := newFixture(t)
	run, req := f.start()
	advance := func(st consts.RequestState) *task.Run {
		t.Helper()
		got, next, err := f.eng.Advance(ctx, run.RunID, f.setState(req, st, nil))
		if err != nil || next != nil {
			t.Fatalf("%s: %v", st, err)
		}
		return got
	}
	ver := func() int64 { return int64(f.storedRun(run.RunID).Version) }

	if got := advance(consts.RequestChecking); got.State != consts.RunRunning || ver() != 1 {
		t.Fatalf("checking: state %s version %d (running -> running must not write)", got.State, ver())
	}
	if got := advance(consts.RequestAwaitingReview); got.State != consts.RunAwaitingReview || ver() != 2 {
		t.Fatalf("awaiting_review: %s v%d", got.State, ver())
	}
	if got := advance(consts.RequestAwaitingRevision); got.State != consts.RunAwaitingReview || ver() != 2 {
		t.Fatalf("awaiting_revision: %s v%d (no write expected)", got.State, ver())
	}
	if got := advance(consts.RequestChecking); got.State != consts.RunRunning || ver() != 3 {
		t.Fatalf("back to checking: %s v%d", got.State, ver())
	}
	if got := advance(consts.RequestFrozen); got.State != consts.RunRunning || ver() != 3 {
		t.Fatalf("frozen: %s v%d", got.State, ver())
	}
	if got := advance(consts.RequestExecuting); got.State != consts.RunRunning || ver() != 3 {
		t.Fatalf("executing: %s v%d", got.State, ver())
	}
	// 这些保存都不带事件
	if n := len(types(f.env.Log.All(), "run:"+run.RunID)); n != 1 {
		t.Errorf("run events = %d, want only task.started", n)
	}
	if got := f.storedRun(run.RunID); got.EventSeq != 1 {
		t.Errorf("event seq = %d", got.EventSeq)
	}
}

func TestAdvanceRejectsWrongRequest(t *testing.T) {
	f := newFixture(t)
	run, req := f.start()
	other := req.Clone()
	other.CallID = "someone-else"
	wrongStep := req.Clone()
	wrongStep.Task = &mreq.TaskRef{RunID: run.RunID, StepID: "B"}
	wrongRun := req.Clone()
	wrongRun.Task = &mreq.TaskRef{RunID: "other-run", StepID: "A"}
	noTask := req.Clone()
	noTask.Task = nil
	badState := req.Clone()
	badState.State = "bogus"
	for name, r := range map[string]*mreq.ExecutionRequest{
		"nil": nil, "call id": other, "step": wrongStep, "run": wrongRun, "no task": noTask, "bad state": badState,
	} {
		_, _, err := f.eng.Advance(ctx, run.RunID, r)
		if codeOf(err) != errcode.InvalidArgument {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	_, _, err := f.eng.Advance(ctx, "ghost", req)
	mustCode(t, err, errcode.NotFound)

	// 运行前进后，旧步骤的申请过期
	run, next := f.succeed(run, req, map[string]any{"n": 1})
	_, _, err = f.eng.Advance(ctx, run.RunID, f.storedReq(req.CallID))
	mustCode(t, err, errcode.InvalidArgument)
	if next.Task.StepID != "B" {
		t.Fatal("did not advance to B")
	}
	// 终态运行
	run, _ = f.walk(run, next, map[string]map[string]any{"D": {"status": "ok", "exit_code": 0}})
	_, _, err = f.eng.Advance(ctx, run.RunID, f.storedReq(next.CallID))
	mustCode(t, err, errcode.RunInvalidState)
}

func TestRenderFailureWritesNothingAndRetries(t *testing.T) {
	f := newFixture(t)
	run, req := f.start()
	r := f.setState(req, consts.RequestSucceeded, nil)
	f.env.Registry.FailNext("RenderStep", 1, errcode.New(errcode.NotFound, "boom"))
	_, _, err := f.eng.Advance(ctx, run.RunID, r)
	mustCode(t, err, errcode.NotFound)
	if got := f.storedRun(run.RunID); got.Version != 1 || got.Current != "A" {
		t.Fatalf("render failure wrote the run: %+v", got)
	}
	if n := len(f.env.Requests.Events()); n != 1 {
		t.Fatalf("request events = %d", n)
	}
	got, next, err := f.eng.Advance(ctx, run.RunID, r)
	if err != nil || next == nil || got.Current != "B" {
		t.Fatalf("retry: %v", err)
	}
}

func TestStepRequestCreateFailureStopsRun(t *testing.T) {
	// 入口步骤
	f := newFixture(t)
	f.env.Requests.FailNext("Create", 1, errcode.New(errcode.CASConflict, "db down"))
	_, _, err := f.eng.Start(ctx, input())
	mustCode(t, err, errcode.CASConflict)
	runs, _ := f.env.Tasks.ListNonTerminalRuns(ctx)
	if len(runs) != 0 {
		t.Fatalf("dangling run: %+v", runs)
	}

	// 后续步骤
	f = newFixture(t)
	run, req := f.start()
	r := f.setState(req, consts.RequestSucceeded, nil)
	f.env.Requests.FailNext("Create", 1, errcode.New(errcode.CASConflict, "db down"))
	_, _, err = f.eng.Advance(ctx, run.RunID, r)
	mustCode(t, err, errcode.CASConflict)
	got := f.storedRun(run.RunID)
	if got.State != consts.RunStopped || got.Stop.Reason != consts.StopFailed || got.Stop.StepID != "B" ||
		!strings.Contains(got.Stop.Detail, "could not be created") {
		t.Fatalf("run = %+v", got.Stop)
	}
	if got.Steps[len(got.Steps)-1].StepID != "A" {
		t.Errorf("record of the missing request kept: %+v", got.Steps)
	}
}

func TestSaveRunCASConflictSurfaces(t *testing.T) {
	f := newFixture(t)
	run, req := f.start()
	f.env.Tasks.FailNext("SaveRun", 1, errcode.New(errcode.CASConflict, "race"))
	_, _, err := f.eng.Advance(ctx, run.RunID, f.setState(req, consts.RequestAwaitingReview, nil))
	mustCode(t, err, errcode.CASConflict)
	if f.storedRun(run.RunID).State != consts.RunRunning {
		t.Error("run changed")
	}
}

func TestRegistryUnitMustMatchDefinition(t *testing.T) {
	f := newFixture(t)
	f.env.Registry.RenderFn = func(c2 context.Context, def *task.Definition, step task.Step, p map[string]any, o map[string]map[string]any) (mreq.Content, mreq.Expectation, error) {
		c, e, err := f.render(c2, def, step, p, o)
		c.Unit = &mreq.UnitRef{ID: step.Unit.ID, Version: step.Unit.Version + 1}
		return c, e, err
	}
	_, _, err := f.eng.Start(ctx, input())
	mustCode(t, err, errcode.UnitMismatch)
	if len(f.env.Log.All()) != 0 {
		t.Error("events after failed start")
	}
}

func TestCancelDraftStep(t *testing.T) {
	f := newFixture(t)
	run, req := f.start()
	got, err := f.eng.Cancel(ctx, run.RunID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != consts.RunCancelled || got.Stop.Reason != consts.StopCancelledByHuman || got.Stop.StepID != "A" ||
		!strings.Contains(got.Stop.Detail, "alice") {
		t.Fatalf("run = %+v", got)
	}
	sr := f.storedReq(req.CallID)
	if sr.State != consts.RequestStopped || sr.Stop.Reason != consts.StopCancelledByHuman {
		t.Fatalf("request = %+v", sr)
	}
	if got.Steps[0].State != consts.RequestStopped || !reflect.DeepEqual(f.storedRun(run.RunID), got) {
		t.Errorf("record / store: %+v", got.Steps)
	}
	log := f.env.Log.All()
	tail := log[len(log)-2:]
	if tail[0].Type != events.CallStopped || tail[0].Data["reason"] != "cancelled_by_human" ||
		tail[1].Type != events.TaskStopped || tail[1].Data["reason"] != "cancelled_by_human" || tail[1].Data["actor"] != "alice" || tail[1].Status != "cancelled" {
		t.Fatalf("tail = %+v", tail)
	}
	// 终态后不能再取消，也不能再推进
	_, err = f.eng.Cancel(ctx, run.RunID, "alice")
	mustCode(t, err, errcode.RunInvalidState)
	_, _, err = f.eng.Advance(ctx, run.RunID, sr)
	mustCode(t, err, errcode.RunInvalidState)
	_, err = f.eng.Cancel(ctx, "ghost", "alice")
	mustCode(t, err, errcode.NotFound)
}

func TestCancelAwaitingReviewStep(t *testing.T) {
	f := newFixture(t)
	run, req := f.start()
	f.setState(req, consts.RequestAwaitingReview, nil)
	if _, err := f.eng.Cancel(ctx, run.RunID, "bob"); err != nil {
		t.Fatal(err)
	}
	if sr := f.storedReq(req.CallID); sr.State != consts.RequestStopped {
		t.Fatalf("request = %s", sr.State)
	}
}

func TestCancelExecutingStepLeavesRequest(t *testing.T) {
	f := newFixture(t)
	run, req := f.start()
	exec := f.setState(req, consts.RequestExecuting, nil)
	reqEventsBefore := len(f.env.Requests.Events())
	got, err := f.eng.Cancel(ctx, run.RunID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != consts.RunCancelled || !strings.Contains(got.Stop.Detail, "executing") || got.Steps[0].State != consts.RequestExecuting {
		t.Fatalf("run = %+v", got)
	}
	sr := f.storedReq(req.CallID)
	if sr.State != consts.RequestExecuting || sr.Version != exec.Version || len(f.env.Requests.Events()) != reqEventsBefore {
		t.Fatalf("executing request was touched: %+v", sr)
	}
}

func TestCancelAwaitingInputAndTerminalStep(t *testing.T) {
	d := diamondDef(1)
	d.Params = append(d.Params, task.FieldSpec{Name: "port", Type: num(), Required: true})
	f := newFixture(t, d)
	run, _, _ := f.eng.Start(ctx, input())
	got, err := f.eng.Cancel(ctx, run.RunID, "alice")
	if err != nil || got.State != consts.RunCancelled || got.Stop.StepID != "" {
		t.Fatalf("got %+v err %v", got, err)
	}

	// 当前步骤已是终态（成功但还没推进）：不动申请
	f2 := newFixture(t)
	run2, req2 := f2.start()
	f2.setState(req2, consts.RequestSucceeded, nil)
	got, err = f2.eng.Cancel(ctx, run2.RunID, "alice")
	if err != nil || got.State != consts.RunCancelled || got.Steps[0].State != consts.RequestSucceeded {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestCancelRequestCASConflictLeavesRunRunning(t *testing.T) {
	f := newFixture(t)
	run, _ := f.start()
	f.env.Requests.FailNext("Save", 1, errcode.New(errcode.CASConflict, "race"))
	_, err := f.eng.Cancel(ctx, run.RunID, "alice")
	mustCode(t, err, errcode.CASConflict)
	if f.storedRun(run.RunID).State != consts.RunRunning {
		t.Error("run cancelled although the step could not be stopped")
	}
	if _, err := f.eng.Cancel(ctx, run.RunID, "alice"); err != nil { // 重试成功
		t.Fatal(err)
	}
}

func TestSaveRunEventsMatchStateSaves(t *testing.T) {
	// 每次 SaveRun 的事件序号接着上一次，且事件与保存一起进入存储
	f := newFixture(t)
	run, req := f.start()
	run, req = f.succeed(run, req, nil)
	run, req = f.succeed(run, req, nil)
	f.setState(req, consts.RequestAwaitingReview, nil)
	got, _, _ := f.eng.Advance(ctx, run.RunID, f.storedReq(req.CallID))
	if got.State != consts.RunAwaitingReview {
		t.Fatal(got.State)
	}
	stored := f.storedRun(run.RunID)
	evts := f.env.Tasks.Events()
	if stored.EventSeq != int64(len(evts)) || stored.EventSeq != 3 { // started + 2 step_completed
		t.Fatalf("event seq %d, events %d", stored.EventSeq, len(evts))
	}
	for i, e := range evts {
		if e.Sequence != int64(i+1) || e.EventID == "" || e.Validate() != nil {
			t.Errorf("event %d = %+v", i, e)
		}
	}
}

func TestConcurrentStartsShareCache(t *testing.T) {
	f := newFixture(t)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := f.eng.Start(ctx, input()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	runs, _ := f.env.Tasks.ListNonTerminalRuns(ctx)
	if len(runs) != 16 {
		t.Fatalf("runs = %d", len(runs))
	}
}

func TestAdvanceUsesStoredRequestNotCallersCopy(t *testing.T) {
	// 调用方的副本只是线索：存储里仍是 draft 时，副本说"succeeded"也不能推进。
	f := newFixture(t)
	run, req := f.start()
	forged := f.storedReq(req.CallID)
	forged.State = consts.RequestSucceeded
	forged.Execution = &mreq.Execution{Outcome: consts.RequestSucceeded, Result: &mreq.ExecutionResult{Output: map[string]any{"n": 1}}}
	_, next, err := f.eng.Advance(ctx, run.RunID, forged)
	mustCode(t, err, errcode.InvalidArgument)
	if next != nil {
		t.Fatal("forged copy created the next step")
	}
	if got := f.storedRun(run.RunID); got.State != consts.RunRunning || got.Current != "A" || got.Version != 1 {
		t.Fatalf("run changed by a forged copy: %+v", got)
	}
	// 修订号对不上也一样
	wrongRev := f.storedReq(req.CallID)
	wrongRev.Revision++
	_, _, err = f.eng.Advance(ctx, run.RunID, wrongRev)
	mustCode(t, err, errcode.InvalidArgument)
	// 版本落后：cas_conflict，重读后重试即可
	stored := f.setState(req, consts.RequestSucceeded, func(r *mreq.ExecutionRequest) {
		r.Execution = &mreq.Execution{Outcome: consts.RequestSucceeded, Result: &mreq.ExecutionResult{Output: map[string]any{"n": 7}}}
	})
	stale := stored.Clone()
	stale.Version--
	stale.Execution.Result.Output = map[string]any{"n": 99}
	_, _, err = f.eng.Advance(ctx, run.RunID, stale)
	mustCode(t, err, errcode.CASConflict)
	// 以存储里的输出为准，不是副本里的
	fresh := f.storedReq(req.CallID)
	fresh.Execution.Result.Output = map[string]any{"n": 99}
	got, next, err := f.eng.Advance(ctx, run.RunID, fresh)
	if err != nil || next == nil || next.Task.StepID != "B" {
		t.Fatalf("advance: %v next %+v", err, next)
	}
	if out := got.Steps[0].Output; out["n"] != 7 {
		t.Fatalf("recorded output %v, want the stored output", out)
	}
}
