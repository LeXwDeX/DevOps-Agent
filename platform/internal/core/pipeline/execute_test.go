package pipeline_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/coretest"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/events"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/pipeline"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/ports"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/consts"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

// 指纹：执行前不一致 → target_changed，执行端口调用次数为 0。
func TestFingerprintChangedBlocksExecution(t *testing.T) {
	rows := []struct {
		name  string
		setup func(f *fixture)
	}{
		{"value differs", func(f *fixture) { f.env.Fingerprint.PushValue("fp-other") }},
		{"unavailable", func(f *fixture) { f.env.Fingerprint.PushUnavailable() }},
		{"port error", func(f *fixture) { f.env.Fingerprint.Push(request.Fingerprint{}, coretest.ErrInjected) }},
		{"collected without value", func(f *fixture) {
			f.env.Fingerprint.Push(request.Fingerprint{Status: consts.FingerprintCollected}, nil)
		}},
	}
	for _, rw := range rows {
		for _, viaPre := range []bool{true, false} {
			name := rw.name + "/Execute"
			if viaPre {
				name = rw.name + "/PreExecute"
			}
			t.Run(name, func(t *testing.T) {
				f := newFixture(t)
				r := f.inspect(input())
				wantState(t, r, consts.RequestFrozen)
				rw.setup(f)
				var err error
				var out *request.ExecutionRequest
				if viaPre {
					err = f.svc.PreExecute(bg, r.CallID)
				} else {
					out, err = f.svc.Execute(bg, r.CallID)
				}
				wantCode(t, err, errcode.FingerprintChanged)
				if out != nil {
					wantState(t, out, consts.RequestTargetChanged)
				}
				wantState(t, f.get(r.CallID), consts.RequestTargetChanged)
				if f.env.Executor.Calls() != 0 {
					t.Fatalf("executor called %d times", f.env.Executor.Calls())
				}
				e := lastEvent(t, f)
				if e.Type != events.CallTargetChanged || e.Status != "target_changed" || e.Data["code"] != "core.fingerprint_changed" {
					t.Errorf("event = %+v", e)
				}
				if got := f.store.types(); len(got[len(got)-1]) != 1 {
					t.Errorf("batches = %v", got)
				}
				if _, ok := hasType(f.env.Requests.Events(), events.ExecutionStarted); ok {
					t.Error("started event before verification")
				}
				// 退回 Agent：可以再次修订、检验并执行。
				if _, err := f.svc.Revise(bg, r.CallID, reviseOf(input(), consts.RevisionSelfReview)); err != nil {
					t.Fatal(err)
				}
				r2, err := f.svc.Inspect(bg, r.CallID)
				if err != nil {
					t.Fatal(err)
				}
				wantState(t, r2, consts.RequestFrozen)
				done, err := f.svc.Execute(bg, r.CallID)
				if err != nil {
					t.Fatal(err)
				}
				wantState(t, done, consts.RequestSucceeded)
			})
		}
	}
}

func TestPreExecuteOKDoesNotChangeState(t *testing.T) {
	f := newFixture(t)
	r := f.inspect(input())
	before := len(f.env.Requests.Events())
	if err := f.svc.PreExecute(bg, r.CallID); err != nil {
		t.Fatal(err)
	}
	if got := f.get(r.CallID); got.State != consts.RequestFrozen || got.Version != r.Version {
		t.Errorf("PreExecute changed the request: %+v", got)
	}
	if len(f.env.Requests.Events()) != before || f.env.Executor.Calls() != 0 {
		t.Error("PreExecute saved events or executed")
	}
}

func TestPreExecuteDigestMismatch(t *testing.T) {
	tamper := map[string]func(r *request.ExecutionRequest){
		"rendered":  func(r *request.ExecutionRequest) { r.Content.Rendered = "rm -rf /" },
		"params":    func(r *request.ExecutionRequest) { r.Content.Params["svc"] = "other" },
		"secret":    func(r *request.ExecutionRequest) { r.Content.SecretKeys = []string{"OTHER"} },
		"scope":     func(r *request.ExecutionRequest) { r.Scope.Targets = []string{"host-z"} },
		"fp value":  func(r *request.ExecutionRequest) { r.Fingerprint.Value = "forged" },
		"policy":    func(r *request.ExecutionRequest) { r.Checks.Policy.Mode = consts.PolicyManual },
		"frozen":    func(r *request.ExecutionRequest) { r.Frozen.Digest = r.Digest },
		"no frozen": func(r *request.ExecutionRequest) { r.Frozen = nil },
	}
	for name, mod := range tamper {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			r := f.inspect(input())
			stored := f.get(r.CallID)
			mod(stored)
			f.env.Requests.Seed(stored)
			err := f.svc.PreExecute(bg, r.CallID)
			wantCode(t, err, errcode.DigestMismatch)
			out, err := f.svc.Execute(bg, r.CallID)
			wantCode(t, err, errcode.DigestMismatch)
			if out != nil {
				t.Error("Execute returned a request on digest mismatch")
			}
			if got := f.get(r.CallID); got.State != consts.RequestFrozen {
				t.Errorf("state = %s", got.State)
			}
			if f.env.Executor.Calls() != 0 {
				t.Error("executor called")
			}
			if _, ok := hasType(f.env.Requests.Events(), events.CallTargetChanged); ok {
				t.Error("digest mismatch must not be reported as target change")
			}
			if len(f.env.Audit.Records()) != 0 {
				t.Error("audited a non-execution")
			}
		})
	}
}

func TestExecuteRequiresFrozen(t *testing.T) {
	f := newFixture(t)
	sub := f.submit(input())
	for _, st := range consts.RequestStates() {
		if st == consts.RequestFrozen {
			continue
		}
		r := f.get(sub.CallID)
		r.State = st
		f.env.Requests.Seed(r)
		_, err := f.svc.Execute(bg, sub.CallID)
		want := errcode.InvalidTransition
		if st.IsTerminal() {
			want = errcode.TerminalState
		}
		wantCode(t, err, want)
		wantCode(t, f.svc.PreExecute(bg, sub.CallID), want)
	}
	if f.env.Executor.Calls() != 0 {
		t.Error("executor called")
	}
	_, err := f.svc.Execute(bg, "ghost")
	wantCode(t, err, errcode.NotFound)
}

func TestExecuteOutcomes(t *testing.T) {
	boom := errors.New("connection reset by peer")
	rows := []struct {
		name       string
		push       func(f *fixture)
		want       consts.RequestState
		event      events.Type
		detailHas  string
		resultKept bool
	}{
		{"success", func(f *fixture) { f.env.Executor.PushOutput(map[string]any{"rows": 3}) }, consts.RequestSucceeded, events.ExecutionCompleted, "matches", true},
		{"criterion false", func(f *fixture) {
			f.env.Executor.Push(request.ExecutionResult{ExitCode: intp(2)}, nil)
		}, consts.RequestMismatched, events.ExecutionMismatched, "exit_code == 0", true},
		{"result error", func(f *fixture) {
			f.env.Executor.Push(request.ExecutionResult{ExitCode: intp(0), Err: "permission denied: SECRET-ERR"}, nil)
		}, consts.RequestFailed, events.ExecutionFailed, "executor reported", true},
		{"executor go error", func(f *fixture) { f.env.Executor.Push(request.ExecutionResult{}, boom) }, consts.RequestUnknown, events.ExecutionUnknown, boom.Error(), false},
		{"missing exit code", func(f *fixture) {
			f.env.Executor.Push(request.ExecutionResult{Output: map[string]any{"exit_code": 0}}, nil)
		}, consts.RequestUnknown, events.ExecutionUnknown, "exit_code", true},
	}
	for _, rw := range rows {
		t.Run(rw.name, func(t *testing.T) {
			f := newFixture(t)
			r := f.inspect(input())
			rw.push(f)
			out, err := f.svc.Execute(bg, r.CallID)
			if err != nil {
				t.Fatal(err)
			}
			wantState(t, out, rw.want)
			if out.Execution == nil || out.Execution.Outcome != rw.want || !strings.Contains(out.Execution.Detail, rw.detailHas) {
				t.Errorf("execution = %+v", out.Execution)
			}
			if (out.Execution.Result != nil) != rw.resultKept {
				t.Errorf("result kept = %v", out.Execution.Result != nil)
			}
			if out.Execution.StartedAt.IsZero() || out.Execution.EndedAt.IsZero() {
				t.Errorf("times = %+v", out.Execution)
			}
			evts := f.env.Requests.Events()
			last := evts[len(evts)-1]
			if last.Type != rw.event || last.Status != string(rw.want) || last.Data["outcome"] != string(rw.want) {
				t.Errorf("last event = %+v", last)
			}
			if evts[len(evts)-2].Type != events.ExecutionStarted {
				t.Errorf("events = %v", typesOf(evts))
			}
			assertNoLeak(t, evts, "SECRET-ERR", "connection reset")
			if got := f.get(r.CallID); got.State != rw.want {
				t.Errorf("stored = %s", got.State)
			}
			if f.env.Executor.Calls() != 1 {
				t.Errorf("executor calls = %d", f.env.Executor.Calls())
			}
			recs := f.env.Audit.Records()
			if len(recs) != 2 || recs[0].Action != "execution.started" || recs[1].Action != "execution.finished" || recs[1].Detail["outcome"] != string(rw.want) {
				t.Errorf("audit = %+v", recs)
			}
			// 终态不再变。
			if _, err := f.svc.Execute(bg, r.CallID); err == nil {
				t.Error("terminal request executed again")
			}
		})
	}
}

// probeExecutor 在执行器被调用的那一刻，查看存储里的状态和已保存的事件。
type probeExecutor struct {
	f        *fixture
	inner    ports.ExecutorPort
	state    consts.RequestState
	lastType events.Type
	runs     int
	onRun    func()
}

func (p *probeExecutor) Execute(ctx context.Context, r *request.ExecutionRequest) (request.ExecutionResult, error) {
	p.runs++
	stored, _ := p.f.env.Requests.Get(context.Background(), r.CallID)
	p.state = stored.State
	evts := p.f.env.Requests.Events()
	p.lastType = evts[len(evts)-1].Type
	if r.State != consts.RequestExecuting {
		p.state = r.State
	}
	if p.onRun != nil {
		p.onRun()
	}
	return p.inner.Execute(ctx, r)
}

func TestStartRecordIsSavedBeforeExecutorRuns(t *testing.T) {
	probe := &probeExecutor{}
	f := newFixture(t, func(d *pipeline.Deps) {
		probe.inner = d.Executor
		d.Executor = probe
	})
	probe.f = f
	r := f.inspect(input())
	if _, err := f.svc.Execute(bg, r.CallID); err != nil {
		t.Fatal(err)
	}
	if probe.runs != 1 || probe.state != consts.RequestExecuting || probe.lastType != events.ExecutionStarted {
		t.Errorf("probe = state %s last %s runs %d", probe.state, probe.lastType, probe.runs)
	}
}

func TestExecuteStartSaveFailureMeansNoExecution(t *testing.T) {
	f := newFixture(t)
	r := f.inspect(input())
	f.env.Requests.FailNext("Save", 1, coretest.ErrInjected)
	if _, err := f.svc.Execute(bg, r.CallID); !errors.Is(err, coretest.ErrInjected) {
		t.Fatalf("err = %v", err)
	}
	if f.env.Executor.Calls() != 0 {
		t.Error("executor ran although the start record was not saved")
	}
	wantState(t, f.get(r.CallID), consts.RequestFrozen)
}

func TestExecuteStartAuditFailureDoesNotStrandRequest(t *testing.T) {
	f := newFixture(t)
	r := f.inspect(input())
	f.env.Audit.FailNext("Append", 1, coretest.ErrInjected)
	out, err := f.svc.Execute(bg, r.CallID)
	if !errors.Is(err, coretest.ErrInjected) {
		t.Fatalf("err = %v", err)
	}
	if f.env.Executor.Calls() != 0 {
		t.Error("executor ran without a start audit record")
	}
	if out == nil || out.State != consts.RequestFailed {
		t.Fatalf("out = %+v", out)
	}
	if e := lastEvent(t, f); e.Type != events.ExecutionFailed {
		t.Errorf("event = %v", e.Type)
	}
}

func TestExecuteEndAuditFailureReturnsSavedResult(t *testing.T) {
	f := newFixture(t)
	r := f.inspect(input())
	// 第二次审计（结束记录）失败。
	failSecond := &secondFails{AuditSink: f.env.Audit}
	svc, err := pipeline.New(withAudit(f.deps, failSecond))
	if err != nil {
		t.Fatal(err)
	}
	out, err := svc.Execute(bg, r.CallID)
	if !errors.Is(err, coretest.ErrInjected) || out == nil || out.State != consts.RequestSucceeded {
		t.Fatalf("out=%v err=%v", out, err)
	}
	if got := f.get(r.CallID); got.State != consts.RequestSucceeded {
		t.Errorf("stored = %s", got.State)
	}
}

type secondFails struct {
	ports.AuditSink
	n int
}

func (s *secondFails) Append(ctx context.Context, rec ports.AuditRecord) error {
	s.n++
	if s.n == 2 {
		return coretest.ErrInjected
	}
	return s.AuditSink.Append(ctx, rec)
}

func withExecutor(d pipeline.Deps, e ports.ExecutorPort) pipeline.Deps {
	d.Executor = e
	return d
}

func withAudit(d pipeline.Deps, a ports.AuditSink) pipeline.Deps {
	d.Audit = a
	return d
}

// 调用方取消 context 之后，执行器已经跑完的结果仍然被保存（不丢结果）。
func TestResultIsSavedEvenIfCallerContextIsCancelled(t *testing.T) {
	f := newFixture(t)
	r := f.inspect(input())
	ctx, cancel := context.WithCancel(bg)
	svc, err := pipeline.New(withExecutor(f.deps, cancelingExecutor{cancel: cancel}))
	if err != nil {
		t.Fatal(err)
	}
	out, err := svc.Execute(ctx, r.CallID)
	if err != nil {
		t.Fatal(err)
	}
	wantState(t, out, consts.RequestSucceeded)
	if got := f.get(r.CallID); got.State != consts.RequestSucceeded {
		t.Errorf("stored = %s", got.State)
	}
}

type cancelingExecutor struct{ cancel func() }

func (c cancelingExecutor) Execute(context.Context, *request.ExecutionRequest) (request.ExecutionResult, error) {
	c.cancel()
	return request.ExecutionResult{ExitCode: intp(0)}, nil
}

// 执行期间申请被别人改动（版本推进）：终态保存冲突，错误向上暴露，不会写出成功。
func TestExecuteTerminalSaveConflictSurfaces(t *testing.T) {
	probe := &probeExecutor{}
	f := newFixture(t, func(d *pipeline.Deps) {
		probe.inner = d.Executor
		d.Executor = probe
	})
	probe.f = f
	r := f.inspect(input())
	probe.onRun = func() {
		cur, _ := f.env.Requests.Get(bg, r.CallID)
		cur.Version++
		f.env.Requests.Seed(cur)
	}
	_, err := f.svc.Execute(bg, r.CallID)
	wantCode(t, err, errcode.CASConflict)
	if got := f.get(r.CallID); got.State != consts.RequestExecuting {
		t.Errorf("stored = %s", got.State)
	}
}

// 检验时指纹不可用 → 转人工；人工 ▶ 之后可以执行，不做指纹比对，事件和审计记"指纹不可用，人工已确认"。
func TestUnavailableFingerprintApprovedByHumanExecutes(t *testing.T) {
	for _, viaPre := range []bool{true, false} {
		name := "Execute"
		if viaPre {
			name = "PreExecute"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.env.Fingerprint.PushUnavailable()
			r := f.inspect(input())
			wantState(t, r, consts.RequestAwaitingReview)
			if r.Fingerprint == nil || r.Fingerprint.Status != consts.FingerprintUnavailable {
				t.Fatalf("fingerprint = %+v", r.Fingerprint)
			}
			frozen, err := f.svc.Review(bg, r.CallID, reviewOf(r, consts.ActionExecute))
			if err != nil {
				t.Fatal(err)
			}
			wantState(t, frozen, consts.RequestFrozen)
			calls := f.env.Fingerprint.Calls()
			if viaPre {
				if err := f.svc.PreExecute(bg, r.CallID); err != nil {
					t.Fatal(err)
				}
				wantState(t, f.get(r.CallID), consts.RequestFrozen)
				return
			}
			out, err := f.svc.Execute(bg, r.CallID)
			if err != nil {
				t.Fatal(err)
			}
			wantState(t, out, consts.RequestSucceeded)
			if f.env.Fingerprint.Calls() != calls {
				t.Error("fingerprint compared although it was unavailable at inspection")
			}
			if f.env.Executor.Calls() != 1 {
				t.Errorf("executor calls = %d", f.env.Executor.Calls())
			}
			started, ok := hasType(f.env.Requests.Events(), events.ExecutionStarted)
			if !ok || started.Data["fingerprint_unavailable_human_confirmed"] != true || started.Data["note"] != "指纹不可用，人工已确认" {
				t.Errorf("started event = %+v", started)
			}
			var started2 *ports.AuditRecord
			for _, rec := range f.env.Audit.Records() {
				if rec.Action == "execution.started" {
					rec := rec
					started2 = &rec
				}
			}
			if started2 == nil || started2.Detail["fingerprint_unavailable_human_confirmed"] != true || started2.Detail["note"] != "指纹不可用，人工已确认" {
				t.Errorf("audit = %+v", f.env.Audit.Records())
			}
		})
	}
}

// 检验时采集到了指纹、人工也批准了，执行前指纹变了仍然是 target_changed，事件不带"人工已确认"标记。
func TestCapturedFingerprintChangedAfterHumanApprovalIsTargetChanged(t *testing.T) {
	f := newFixture(t)
	f.setPolicy(consts.PolicyManual)
	r := f.inspect(input())
	wantState(t, r, consts.RequestAwaitingReview)
	if _, err := f.svc.Review(bg, r.CallID, reviewOf(r, consts.ActionExecute)); err != nil {
		t.Fatal(err)
	}
	f.env.Fingerprint.PushValue("fp-other")
	out, err := f.svc.Execute(bg, r.CallID)
	wantCode(t, err, errcode.FingerprintChanged)
	wantState(t, out, consts.RequestTargetChanged)
	if f.env.Executor.Calls() != 0 {
		t.Error("executor called")
	}
}

// 指纹不可用且没有人工批准就冻结（不会由正常流程产生）：仍然是 target_changed，不执行。
func TestUnavailableFingerprintWithoutApprovalIsTargetChanged(t *testing.T) {
	f := newFixture(t)
	r := f.inspect(input())
	wantState(t, r, consts.RequestFrozen)
	stored := f.get(r.CallID)
	stored.Fingerprint = &request.Fingerprint{Status: consts.FingerprintUnavailable}
	// 冻结摘要包含指纹，重算后保存，模拟"没有人工批准的不可用指纹冻结"。
	fd, err := stored.FreezeDigest()
	if err != nil {
		t.Fatal(err)
	}
	stored.Frozen.Digest = fd
	f.env.Requests.Seed(stored)
	out, err := f.svc.Execute(bg, r.CallID)
	wantCode(t, err, errcode.FingerprintChanged)
	wantState(t, out, consts.RequestTargetChanged)
	if f.env.Executor.Calls() != 0 {
		t.Error("executor called")
	}
}
