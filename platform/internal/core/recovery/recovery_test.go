package recovery

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/coretest"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/events"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/ports"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/ids"
	mreq "github.com/LeXwDeX/DevOps-Agent/platform/internal/model/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/task"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/consts"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

var ctx = context.Background()

func deps(env *coretest.Env) Deps {
	return Deps{Requests: env.Requests, Tasks: env.Tasks, Events: env.Sink, Clock: env.Clock, IDGen: env.IDs}
}

func callID(s consts.RequestState) ids.CallID { return ids.CallID("call-" + string(s)) }

// seedAll 为全部 13 个申请状态和 8 个运行状态各放一个对象，版本都是 5。
func seedAll(env *coretest.Env) {
	for _, st := range consts.RequestStates() {
		r := coretest.SampleRequest(callID(st))
		r.State = st
		r.Version = 5
		r.EventSeq = 3
		env.Requests.Seed(r)
	}
	for _, st := range consts.RunStates() {
		r := coretest.SampleRun("run-" + string(st))
		r.State = st
		r.Version = 5
		r.Current = "A"
		r.EventSeq = 2
		env.Tasks.SeedRun(r)
	}
}

func snapshot(t *testing.T, env *coretest.Env) (map[ids.CallID]*mreq.ExecutionRequest, map[string]*task.Run) {
	t.Helper()
	reqs := map[ids.CallID]*mreq.ExecutionRequest{}
	for _, st := range consts.RequestStates() {
		r, err := env.Requests.Get(ctx, callID(st))
		if err != nil {
			t.Fatal(err)
		}
		reqs[r.CallID] = r
	}
	runs := map[string]*task.Run{}
	for _, st := range consts.RunStates() {
		r, err := env.Tasks.GetRun(ctx, "run-"+string(st))
		if err != nil {
			t.Fatal(err)
		}
		runs[r.RunID] = r
	}
	return reqs, runs
}

func TestOnStartupRewritesNonTerminal(t *testing.T) {
	env := coretest.NewEnv()
	seedAll(env)
	before, beforeRuns := snapshot(t, env)

	rep, err := OnStartup(ctx, deps(env))
	if err != nil {
		t.Fatal(err)
	}
	after, afterRuns := snapshot(t, env)

	wantStopped := []ids.CallID{"call-awaiting_review", "call-awaiting_revision", "call-checking", "call-draft", "call-frozen", "call-target_changed"}
	if !reflect.DeepEqual(rep.StoppedRequests, wantStopped) {
		t.Errorf("stopped = %v\nwant     %v", rep.StoppedRequests, wantStopped)
	}
	if !reflect.DeepEqual(rep.UnknownRequests, []ids.CallID{"call-executing"}) {
		t.Errorf("unknown = %v", rep.UnknownRequests)
	}
	wantRuns := []string{"run-awaiting_input", "run-awaiting_review", "run-pending", "run-running"}
	if !reflect.DeepEqual(rep.StoppedRuns, wantRuns) || len(rep.Errors) != 0 || rep.RecoveryID == "" {
		t.Errorf("report = %+v", rep)
	}

	for _, st := range consts.RequestStates() {
		id := callID(st)
		b, a := before[id], after[id]
		switch {
		case st.IsTerminal():
			if !reflect.DeepEqual(a, b) {
				t.Errorf("%s: terminal request touched", st)
			}
		case st == consts.RequestExecuting:
			if a.State != consts.RequestUnknown || a.Execution == nil || a.Execution.Outcome != consts.RequestUnknown ||
				a.Execution.Detail != "recovery_interrupted" || a.Stop != nil || a.Version != 6 {
				t.Errorf("executing -> %+v", a)
			}
		default:
			if a.State != consts.RequestStopped || a.Stop == nil || a.Stop.Reason != consts.StopRecoveryInterrupted || a.Version != 6 ||
				a.Revision != b.Revision || a.Digest != b.Digest {
				t.Errorf("%s -> %+v", st, a)
			}
		}
	}
	for _, st := range consts.RunStates() {
		id := "run-" + string(st)
		b, a := beforeRuns[id], afterRuns[id]
		if st.IsTerminal() {
			if !reflect.DeepEqual(a, b) {
				t.Errorf("%s: terminal run touched", st)
			}
			continue
		}
		if a.State != consts.RunRecoveryInterrupted || a.Stop == nil || a.Stop.Reason != consts.StopRecoveryInterrupted ||
			a.Stop.StepID != "A" || a.Version != 6 || a.TaskVersion != b.TaskVersion || a.TaskDigest != b.TaskDigest {
			t.Errorf("%s -> %+v", st, a)
		}
	}
}

func TestOnStartupEvents(t *testing.T) {
	env := coretest.NewEnv()
	seedAll(env)
	rep, err := OnStartup(ctx, deps(env))
	if err != nil {
		t.Fatal(err)
	}
	log := env.Log.All()
	// 1 started + 7 申请 + 4 运行 + 1 completed
	if len(log) != 13 {
		t.Fatalf("events = %d", len(log))
	}
	first, last := log[0], log[len(log)-1]
	if first.Type != events.RecoveryStarted || last.Type != events.RecoveryCompleted {
		t.Fatalf("first %s last %s", first.Type, last.Type)
	}
	// 恢复开始/结束走 EventSink，不伴随状态保存
	sink := env.Sink.Events()
	if len(sink) != 2 || sink[0].Type != events.RecoveryStarted || sink[1].Type != events.RecoveryCompleted {
		t.Fatalf("sink events = %+v", sink)
	}
	for i, e := range sink {
		if e.StreamID != "recovery:"+rep.RecoveryID || e.Sequence != int64(i+1) || e.SourceKind != events.SourceHostConfirmed || e.Validate() != nil {
			t.Errorf("sink event %d = %+v", i, e)
		}
	}
	if last.Data["stopped_requests"] != 6 || last.Data["unknown_requests"] != 1 || last.Data["stopped_runs"] != 4 || last.Data["errors"] != 0 {
		t.Errorf("completed data = %v", last.Data)
	}

	// 状态保存时一起带的事件
	byType := map[events.Type]int{}
	for _, e := range log[1 : len(log)-1] {
		byType[e.Type]++
		switch e.Type {
		case events.CallStopped:
			if e.Status != "stopped" || e.Data["reason"] != "recovery_interrupted" || e.Sequence != 4 {
				t.Errorf("call.stopped = %+v", e)
			}
		case events.ExecutionUnknown:
			if e.Status != "unknown" || e.CallID != "call-executing" || e.Sequence != 4 {
				t.Errorf("execution.unknown = %+v", e)
			}
		case events.TaskStopped:
			if e.Status != "recovery_interrupted" || e.Data["reason"] != "recovery_interrupted" || e.Data["step_id"] != "A" || e.Sequence != 3 {
				t.Errorf("task.stopped = %+v", e)
			}
		default:
			t.Errorf("unexpected event %s", e.Type)
		}
	}
	if byType[events.CallStopped] != 6 || byType[events.ExecutionUnknown] != 1 || byType[events.TaskStopped] != 4 {
		t.Errorf("event counts = %v", byType)
	}
	// 保存与事件同一次提交：存储记录的事件数等于改写数
	if len(env.Requests.Events()) != 7 || len(env.Tasks.Events()) != 4 {
		t.Errorf("store events: %d / %d", len(env.Requests.Events()), len(env.Tasks.Events()))
	}
	// 没有对任何东西调用执行（Deps 里本来就没有执行器）
	if env.Executor.Calls() != 0 {
		t.Error("executor called")
	}
}

func TestOnStartupIdempotent(t *testing.T) {
	env := coretest.NewEnv()
	seedAll(env)
	if _, err := OnStartup(ctx, deps(env)); err != nil {
		t.Fatal(err)
	}
	reqs1, runs1 := snapshot(t, env)
	storeEvents := len(env.Requests.Events()) + len(env.Tasks.Events())

	rep, err := OnStartup(ctx, deps(env))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.StoppedRequests)+len(rep.UnknownRequests)+len(rep.StoppedRuns)+len(rep.Errors) != 0 {
		t.Fatalf("second report not empty: %+v", rep)
	}
	reqs2, runs2 := snapshot(t, env)
	if !reflect.DeepEqual(reqs1, reqs2) || !reflect.DeepEqual(runs1, runs2) {
		t.Error("second call changed states")
	}
	if got := len(env.Requests.Events()) + len(env.Tasks.Events()); got != storeEvents {
		t.Errorf("second call saved %d more events", got-storeEvents)
	}
	// 第二次仍有 started / completed
	sink := env.Sink.Events()
	if len(sink) != 4 || sink[2].Type != events.RecoveryStarted || sink[3].Type != events.RecoveryCompleted ||
		sink[2].StreamID == sink[0].StreamID {
		t.Errorf("sink = %+v", sink)
	}
	if sink[3].Data["stopped_requests"] != 0 {
		t.Errorf("completed data = %v", sink[3].Data)
	}
}

func TestOnStartupEmptyStores(t *testing.T) {
	env := coretest.NewEnv()
	rep, err := OnStartup(ctx, deps(env))
	if err != nil || len(rep.StoppedRuns) != 0 || len(env.Sink.Events()) != 2 {
		t.Fatalf("rep %+v err %v", rep, err)
	}
}

func TestRunStepRecordFollowsRequestOutcome(t *testing.T) {
	env := coretest.NewEnv()
	r := coretest.SampleRequest("call-x")
	r.State = consts.RequestExecuting
	env.Requests.Seed(r)
	run := coretest.SampleRun("run-x")
	run.State = consts.RunRunning
	run.Current = "A"
	run.Steps = []task.StepRecord{{StepID: "A", CallID: "call-x", State: consts.RequestExecuting}}
	env.Tasks.SeedRun(run)
	if _, err := OnStartup(ctx, deps(env)); err != nil {
		t.Fatal(err)
	}
	got, _ := env.Tasks.GetRun(ctx, "run-x")
	if got.Steps[0].State != consts.RequestUnknown {
		t.Errorf("record = %s", got.Steps[0].State)
	}
}

// racyRequests 在第一次 Save 前改动已存的申请，模拟别的进程抢先写入。
type racyRequests struct {
	*coretest.MemRequestStore
	changeTo consts.RequestState
	done     bool
}

func (s *racyRequests) Save(c context.Context, r *mreq.ExecutionRequest, expected ids.Version, evts []events.Event) error {
	if !s.done {
		s.done = true
		cur, _ := s.MemRequestStore.Get(c, r.CallID)
		cur.State = s.changeTo
		cur.Version++
		s.MemRequestStore.Seed(cur)
	}
	return s.MemRequestStore.Save(c, r, expected, evts)
}

func TestCASConflictReReadsOnce(t *testing.T) {
	t.Run("raced into another non-terminal state: retried", func(t *testing.T) {
		env := coretest.NewEnv()
		r := coretest.SampleRequest("call-1")
		r.State = consts.RequestChecking
		env.Requests.Seed(r)
		d := deps(env)
		d.Requests = &racyRequests{MemRequestStore: env.Requests, changeTo: consts.RequestAwaitingReview}
		rep, err := OnStartup(ctx, d)
		if err != nil || len(rep.Errors) != 0 || len(rep.StoppedRequests) != 1 {
			t.Fatalf("rep %+v err %v", rep, err)
		}
		got, _ := env.Requests.Get(ctx, "call-1")
		if got.State != consts.RequestStopped || got.Version != 3 {
			t.Errorf("got %s v%d", got.State, got.Version)
		}
	})
	t.Run("raced into a terminal state: left alone", func(t *testing.T) {
		env := coretest.NewEnv()
		r := coretest.SampleRequest("call-1")
		r.State = consts.RequestExecuting
		env.Requests.Seed(r)
		d := deps(env)
		d.Requests = &racyRequests{MemRequestStore: env.Requests, changeTo: consts.RequestSucceeded}
		rep, err := OnStartup(ctx, d)
		if err != nil || len(rep.Errors) != 0 || len(rep.StoppedRequests)+len(rep.UnknownRequests) != 0 {
			t.Fatalf("rep %+v err %v", rep, err)
		}
		if got, _ := env.Requests.Get(ctx, "call-1"); got.State != consts.RequestSucceeded {
			t.Errorf("got %s", got.State)
		}
	})
	t.Run("conflict twice on one item: recorded, others continue", func(t *testing.T) {
		env := coretest.NewEnv()
		seedAll(env)
		env.Requests.FailNext("Save", 2, errcode.New(errcode.CASConflict, "injected"))
		env.Tasks.FailNext("SaveRun", 2, errcode.New(errcode.CASConflict, "injected"))
		rep, err := OnStartup(ctx, deps(env))
		if err != nil {
			t.Fatal(err)
		}
		if len(rep.Errors) != 2 || !strings.Contains(rep.Errors[0], "call-awaiting_review") || !strings.Contains(rep.Errors[1], "run-awaiting_input") {
			t.Fatalf("errors = %v", rep.Errors)
		}
		if len(rep.StoppedRequests) != 5 || len(rep.UnknownRequests) != 1 || len(rep.StoppedRuns) != 3 {
			t.Fatalf("report = %+v", rep)
		}
		// 失败的那一项没有被改动，再跑一次就收尾了
		if got, _ := env.Requests.Get(ctx, "call-awaiting_review"); got.State != consts.RequestAwaitingReview {
			t.Errorf("failed item changed: %s", got.State)
		}
		rep2, err := OnStartup(ctx, deps(env))
		if err != nil || len(rep2.StoppedRequests) != 1 || len(rep2.StoppedRuns) != 1 || len(rep2.Errors) != 0 {
			t.Fatalf("rep2 %+v err %v", rep2, err)
		}
		if got := env.Sink.Events()[len(env.Sink.Events())-1]; got.Type != events.RecoveryCompleted {
			t.Error("last event")
		}
	})
}

func TestSaveFailureDoesNotStopOthers(t *testing.T) {
	env := coretest.NewEnv()
	seedAll(env)
	env.Requests.FailNext("Save", 1, coretest.ErrInjected)
	rep, err := OnStartup(ctx, deps(env))
	if err != nil || len(rep.Errors) != 1 {
		t.Fatalf("rep %+v err %v", rep, err)
	}
	if len(rep.StoppedRequests)+len(rep.UnknownRequests) != 6 || len(rep.StoppedRuns) != 4 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestListFailureReturnsError(t *testing.T) {
	env := coretest.NewEnv()
	seedAll(env)
	env.Requests.FailWith("ListNonTerminal", coretest.ErrInjected)
	rep, err := OnStartup(ctx, deps(env))
	if !errors.Is(err, coretest.ErrInjected) || rep.RecoveryID == "" {
		t.Fatalf("rep %+v err %v", rep, err)
	}
	env.Requests.ClearFaults()
	env.Tasks.FailWith("ListNonTerminalRuns", coretest.ErrInjected)
	rep, err = OnStartup(ctx, deps(env))
	if !errors.Is(err, coretest.ErrInjected) || len(rep.StoppedRequests) != 6 {
		t.Fatalf("rep %+v err %v", rep, err)
	}
}

func TestEmitFailureIsReportedNotFatal(t *testing.T) {
	env := coretest.NewEnv()
	seedAll(env)
	env.Sink.FailWith("Emit", coretest.ErrInjected)
	rep, err := OnStartup(ctx, deps(env))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Errors) != 2 || len(rep.StoppedRuns) != 4 || len(rep.StoppedRequests) != 6 {
		t.Fatalf("report = %+v", rep)
	}
	if got, _ := env.Tasks.GetRun(ctx, "run-running"); got.State != consts.RunRecoveryInterrupted {
		t.Error("run not recovered after the sink failed")
	}
}

func TestDepsRequired(t *testing.T) {
	_, err := OnStartup(ctx, Deps{})
	if c, _ := errcode.CodeOf(err); c != errcode.InvalidArgument {
		t.Fatalf("err = %v", err)
	}
}

// 恢复不续跑：Deps 里不能有执行器端口。
func TestDepsHaveNoExecutor(t *testing.T) {
	exec := reflect.TypeOf((*ports.ExecutorPort)(nil)).Elem()
	typ := reflect.TypeOf(Deps{})
	for i := 0; i < typ.NumField(); i++ {
		if ft := typ.Field(i).Type; ft == exec || ft.Implements(exec) {
			t.Errorf("Deps.%s can execute", typ.Field(i).Name)
		}
	}
}
