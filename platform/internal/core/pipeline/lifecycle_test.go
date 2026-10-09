package pipeline_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/coretest"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/events"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/pipeline"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/ids"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/consts"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

// manualInput 是一定会转人工的提交（自审有风险）。
func manualInput() pipeline.SubmitInput {
	in := input()
	in.Submission.SelfReview = &request.SelfReview{Verdict: consts.VerdictRisk, Reason: "touches prod"}
	return in
}

func reviewOf(r *request.ExecutionRequest, a consts.HumanAction) pipeline.ReviewInput {
	return pipeline.ReviewInput{Action: a, Revision: r.Revision, Digest: r.Digest, Actor: "alice", Comment: "looks fine"}
}

func reviseOf(in pipeline.SubmitInput, src consts.RevisionSource) pipeline.ReviseInput {
	return pipeline.ReviseInput{Content: in.Content, Expectation: in.Expectation, Submission: in.Submission, Source: src}
}

func TestReviewExecuteFreezesAndAudits(t *testing.T) {
	f := newFixture(t)
	r := f.inspect(manualInput())
	wantState(t, r, consts.RequestAwaitingReview)
	out, err := f.svc.Review(bg, r.CallID, reviewOf(r, consts.ActionExecute))
	if err != nil {
		t.Fatal(err)
	}
	wantState(t, out, consts.RequestFrozen)
	if out.Frozen == nil || len(out.Reviews) != 1 || out.Reviews[0].Actor != "alice" {
		t.Errorf("out = %+v", out)
	}
	batches := f.store.types()
	if got := batches[len(batches)-1]; !reflect.DeepEqual(got, []events.Type{events.ReviewApproved, events.CallFrozen}) {
		t.Errorf("last batch = %v", got)
	}
	recs := f.env.Audit.Records()
	if len(recs) != 1 || recs[0].Action != "review.execute" || recs[0].Actor != "alice" || recs[0].CallID != r.CallID || recs[0].Detail["digest"] != string(r.Digest) {
		t.Errorf("audit = %+v", recs)
	}
	assertNoLeak(t, f.env.Requests.Events())
	// 冻结后可以执行。
	done, err := f.svc.Execute(bg, r.CallID)
	if err != nil {
		t.Fatal(err)
	}
	wantState(t, done, consts.RequestSucceeded)
}

func TestReviewRejectAndComment(t *testing.T) {
	f := newFixture(t)
	r := f.inspect(manualInput())
	out, err := f.svc.Review(bg, r.CallID, reviewOf(r, consts.ActionReject))
	if err != nil {
		t.Fatal(err)
	}
	wantState(t, out, consts.RequestRejected)
	if out.Rejection.Source != consts.RejectedByHuman {
		t.Errorf("rejection = %+v", out.Rejection)
	}
	if e := lastEvent(t, f); e.Type != events.ReviewRejected || e.Status != "rejected" {
		t.Errorf("event = %+v", e)
	}

	r2 := f.inspect(manualInput())
	out2, err := f.svc.Review(bg, r2.CallID, reviewOf(r2, consts.ActionComment))
	if err != nil {
		t.Fatal(err)
	}
	wantState(t, out2, consts.RequestAwaitingRevision)
	if e := lastEvent(t, f); e.Type != events.ReviewCommented || e.Status != "awaiting_revision" {
		t.Errorf("event = %+v", e)
	}
	recs := f.env.Audit.Records()
	if len(recs) != 2 || recs[0].Action != "review.reject" || recs[1].Action != "review.comment" {
		t.Errorf("audit = %+v", recs)
	}
	assertNoLeak(t, f.env.Requests.Events(), "looks fine") // 点评原文只进审计，不进事件
}

func TestReviewErrors(t *testing.T) {
	f := newFixture(t)
	r := f.inspect(manualInput())
	// 修订或摘要不是当前值
	bad := reviewOf(r, consts.ActionExecute)
	bad.Revision = 7
	_, err := f.svc.Review(bg, r.CallID, bad)
	wantCode(t, err, errcode.ReviewStale)
	bad = reviewOf(r, consts.ActionExecute)
	bad.Digest = ids.Digest("ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
	_, err = f.svc.Review(bg, r.CallID, bad)
	wantCode(t, err, errcode.ReviewStale)
	// 空操作者、未知动作、空点评
	noActor := reviewOf(r, consts.ActionExecute)
	noActor.Actor = " "
	_, err = f.svc.Review(bg, r.CallID, noActor)
	wantCode(t, err, errcode.InvalidArgument)
	_, err = f.svc.Review(bg, r.CallID, pipeline.ReviewInput{Action: "approve", Revision: r.Revision, Digest: r.Digest, Actor: "a"})
	wantCode(t, err, errcode.InvalidArgument)
	emptyComment := reviewOf(r, consts.ActionComment)
	emptyComment.Comment = ""
	_, err = f.svc.Review(bg, r.CallID, emptyComment)
	wantCode(t, err, errcode.InvalidArgument)
	// 不在待人工状态
	auto := newFixture(t)
	ar := auto.inspect(input())
	_, err = auto.svc.Review(bg, ar.CallID, reviewOf(ar, consts.ActionExecute))
	wantCode(t, err, errcode.InvalidTransition)
	_, err = f.svc.Review(bg, "nope", reviewOf(r, consts.ActionExecute))
	wantCode(t, err, errcode.NotFound)
	if got := f.get(r.CallID); got.State != consts.RequestAwaitingReview || len(got.Reviews) != 0 {
		t.Errorf("failed reviews changed state: %+v", got)
	}
	if len(f.env.Audit.Records()) != 0 {
		t.Error("failed review audited")
	}
}

func TestReviewAuditFailureAfterSave(t *testing.T) {
	f := newFixture(t)
	r := f.inspect(manualInput())
	f.env.Audit.FailNext("Append", 1, coretest.ErrInjected)
	out, err := f.svc.Review(bg, r.CallID, reviewOf(r, consts.ActionExecute))
	if !errors.Is(err, coretest.ErrInjected) || out == nil || out.State != consts.RequestFrozen {
		t.Fatalf("out=%v err=%v", out, err)
	}
	if got := f.get(r.CallID); got.State != consts.RequestFrozen {
		t.Errorf("stored state = %s", got.State)
	}
}

// 修订：点评 → 待修订 → 新修订 → 重新检验；旧批准失效；旧修订进日志。
func TestReviseAfterCommentInvalidatesOldApproval(t *testing.T) {
	f := newFixture(t)
	r1 := f.inspect(manualInput())
	wantState(t, r1, consts.RequestAwaitingReview)
	if _, err := f.svc.Review(bg, r1.CallID, reviewOf(r1, consts.ActionComment)); err != nil {
		t.Fatal(err)
	}
	in := manualInput()
	in.Content.Rendered = secretRendered + " --safe"
	r2, err := f.svc.Revise(bg, r1.CallID, reviseOf(in, consts.RevisionHumanComment))
	if err != nil {
		t.Fatal(err)
	}
	wantState(t, r2, consts.RequestChecking)
	if r2.Revision != 2 || r2.Digest == r1.Digest || r2.Version <= r1.Version {
		t.Errorf("r2 = rev %d digest %s version %d", r2.Revision, r2.Digest, r2.Version)
	}
	if r2.Checks.DCG != nil || r2.Fingerprint != nil {
		t.Error("old checks survived the revision")
	}
	if r2.Submission.SelfReview == nil || r2.Submission.SelfReview.Verdict != consts.VerdictRisk {
		t.Errorf("submission = %+v", r2.Submission)
	}
	// 旧修订完整进了日志
	logged, err := f.env.Revisions.List(bg, r1.CallID)
	if err != nil || len(logged) != 1 || logged[0].Revision != 1 || logged[0].Digest != r1.Digest {
		t.Fatalf("log = %+v err=%v", logged, err)
	}
	if logged[0].Content.Rendered != secretRendered {
		t.Errorf("logged content = %q", logged[0].Content.Rendered)
	}
	if e := lastEvent(t, f); e.Type != events.CallRevised || e.Data["source"] != "human_comment" || e.Revision != 2 || e.Data["digest"] != string(r2.Digest) {
		t.Errorf("event = %+v", e)
	}
	if got := f.store.types(); !reflect.DeepEqual(got[len(got)-1], []events.Type{events.CallRevised}) {
		t.Errorf("batches = %v", got)
	}
	assertNoLeak(t, f.env.Requests.Events(), " --safe")

	r3, err := f.svc.Inspect(bg, r1.CallID)
	if err != nil {
		t.Fatal(err)
	}
	wantState(t, r3, consts.RequestAwaitingReview)
	// 绑定旧修订或旧摘要的批准不能用。
	_, err = f.svc.Review(bg, r1.CallID, reviewOf(r1, consts.ActionExecute))
	wantCode(t, err, errcode.ReviewStale)
	stale := reviewOf(r3, consts.ActionExecute)
	stale.Digest = r1.Digest
	_, err = f.svc.Review(bg, r1.CallID, stale)
	wantCode(t, err, errcode.ReviewStale)
	out, err := f.svc.Review(bg, r1.CallID, reviewOf(r3, consts.ActionExecute))
	if err != nil {
		t.Fatal(err)
	}
	wantState(t, out, consts.RequestFrozen)
	if out.Revision != 2 {
		t.Errorf("revision = %d", out.Revision)
	}
}

func TestReviseSelfReviewFromChecking(t *testing.T) {
	f := newFixture(t)
	sub := f.submit(input())
	// draft 不能修订
	_, err := f.svc.Revise(bg, sub.CallID, reviseOf(input(), consts.RevisionSelfReview))
	wantCode(t, err, errcode.InvalidTransition)
	// 构造 checking 状态：dcg 命中使检验转人工后点评，再进入 checking 由 Revise 完成；
	// 这里直接 seed 一个 checking 申请。
	r := f.get(sub.CallID)
	r.State = consts.RequestChecking
	f.env.Requests.Seed(r)
	out, err := f.svc.Revise(bg, sub.CallID, reviseOf(input(), consts.RevisionSelfReview))
	if err != nil {
		t.Fatal(err)
	}
	if out.Revision != 2 || out.State != consts.RequestChecking {
		t.Errorf("out = %+v", out)
	}
}

func TestReviseErrors(t *testing.T) {
	f := newFixture(t)
	frozen := f.inspect(input())
	wantState(t, frozen, consts.RequestFrozen)
	_, err := f.svc.Revise(bg, frozen.CallID, reviseOf(input(), consts.RevisionSelfReview))
	wantCode(t, err, errcode.FrozenImmutable)

	f2 := newFixture(t)
	r := f2.inspect(manualInput())
	_, err = f2.svc.Revise(bg, r.CallID, reviseOf(manualInput(), consts.RevisionSelfReview)) // awaiting_review
	wantCode(t, err, errcode.InvalidTransition)
	if _, err := f2.svc.Review(bg, r.CallID, reviewOf(r, consts.ActionComment)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []consts.RevisionSource{"", "agent", "hook"} {
		_, err = f2.svc.Revise(bg, r.CallID, reviseOf(manualInput(), bad))
		wantCode(t, err, errcode.InvalidRevisionSrc)
	}
	if logged, _ := f2.env.Revisions.List(bg, r.CallID); len(logged) != 0 {
		t.Errorf("failed revisions logged: %d", len(logged))
	}
	// 日志写入失败：不保存新修订。
	f2.env.Revisions.FailNext("Append", 1, coretest.ErrInjected)
	if _, err := f2.svc.Revise(bg, r.CallID, reviseOf(manualInput(), consts.RevisionHumanComment)); !errors.Is(err, coretest.ErrInjected) {
		t.Fatalf("err = %v", err)
	}
	if got := f2.get(r.CallID); got.Revision != 1 || got.State != consts.RequestAwaitingRevision {
		t.Errorf("state after log failure: rev %d %s", got.Revision, got.State)
	}
}

func TestReviseLogsBeforeSave(t *testing.T) {
	f := newFixture(t)
	r := f.inspect(manualInput())
	if _, err := f.svc.Review(bg, r.CallID, reviewOf(r, consts.ActionComment)); err != nil {
		t.Fatal(err)
	}
	f.env.Requests.FailNext("Save", 1, coretest.ErrInjected)
	if _, err := f.svc.Revise(bg, r.CallID, reviseOf(manualInput(), consts.RevisionHumanComment)); !errors.Is(err, coretest.ErrInjected) {
		t.Fatalf("err = %v", err)
	}
	// 先写日志再保存：保存失败时日志里已有旧修订（宁可多一份，不丢）。
	logged, _ := f.env.Revisions.List(bg, r.CallID)
	if len(logged) != 1 {
		t.Errorf("log entries = %d", len(logged))
	}
	if got := f.get(r.CallID); got.Revision != 1 {
		t.Errorf("revision = %d", got.Revision)
	}
}

func TestCancel(t *testing.T) {
	cancellable := []consts.RequestState{
		consts.RequestDraft, consts.RequestChecking, consts.RequestAwaitingReview,
		consts.RequestAwaitingRevision, consts.RequestFrozen, consts.RequestTargetChanged,
	}
	for _, st := range cancellable {
		t.Run(string(st), func(t *testing.T) {
			f := newFixture(t)
			r := coretest.SampleRequest("c1")
			r.State = st
			f.env.Requests.Seed(r)
			out, err := f.svc.Cancel(bg, "c1", "alice")
			if err != nil {
				t.Fatal(err)
			}
			wantState(t, out, consts.RequestStopped)
			if out.Stop == nil || out.Stop.Reason != consts.StopCancelledByHuman {
				t.Errorf("stop = %+v", out.Stop)
			}
			evts := f.env.Requests.Events()
			if len(evts) != 1 || evts[0].Type != events.CallStopped || evts[0].Status != "stopped" || evts[0].Data["reason"] != "cancelled_by_human" {
				t.Errorf("events = %+v", evts)
			}
			recs := f.env.Audit.Records()
			if len(recs) != 1 || recs[0].Action != "call.cancel" || recs[0].Actor != "alice" {
				t.Errorf("audit = %+v", recs)
			}
		})
	}
	// executing 不能取消；终态不能取消。
	for _, st := range consts.RequestStates() {
		ok := false
		for _, c := range cancellable {
			ok = ok || c == st
		}
		if ok {
			continue
		}
		t.Run("not "+string(st), func(t *testing.T) {
			f := newFixture(t)
			r := coretest.SampleRequest("c1")
			r.State = st
			f.env.Requests.Seed(r)
			_, err := f.svc.Cancel(bg, "c1", "alice")
			want := errcode.InvalidTransition
			if st.IsTerminal() {
				want = errcode.TerminalState
			}
			wantCode(t, err, want)
			if got := f.get("c1"); got.State != st {
				t.Errorf("state changed to %s", got.State)
			}
			if len(f.env.Requests.Events()) != 0 || len(f.env.Audit.Records()) != 0 {
				t.Error("failed cancel left events or audit")
			}
		})
	}
	f := newFixture(t)
	f.env.Requests.Seed(coretest.SampleRequest("c1"))
	_, err := f.svc.Cancel(bg, "c1", "")
	wantCode(t, err, errcode.InvalidArgument)
	_, err = f.svc.Cancel(bg, "ghost", "a")
	wantCode(t, err, errcode.NotFound)
}

// CAS 冲突以 core.cas_conflict 暴露，且不留下事件。
func TestCASConflictSurfaces(t *testing.T) {
	f := newFixture(t)
	r := f.inspect(manualInput())
	before := len(f.env.Requests.Events())
	f.env.Requests.FailNext("Save", 1, errcode.New(errcode.CASConflict, "injected"))
	_, err := f.svc.Review(bg, r.CallID, reviewOf(r, consts.ActionExecute))
	wantCode(t, err, errcode.CASConflict)
	if len(f.env.Requests.Events()) != before || len(f.env.Audit.Records()) != 0 {
		t.Error("conflicting review left events or audit")
	}
}

// staleReads 让每次读取之后存储里的版本立刻被别人推进，模拟读到旧版本。
type staleReads struct {
	*recStore
	env *coretest.Env
}

func (s *staleReads) Get(ctx context.Context, id ids.CallID) (*request.ExecutionRequest, error) {
	r, err := s.RequestStore.Get(ctx, id)
	if err == nil {
		bumped := r.Clone()
		bumped.Version++
		s.env.Requests.Seed(bumped)
	}
	return r, err
}

func TestRealCASConflictOnStaleRead(t *testing.T) {
	f := newFixture(t)
	r := f.inspect(manualInput())
	stale := &staleReads{recStore: f.store, env: f.env}
	deps := f.deps
	deps.Requests = stale
	svc, err := pipeline.New(deps)
	if err != nil {
		t.Fatal(err)
	}
	for name, call := range map[string]func() error{
		"review": func() error { _, e := svc.Review(bg, r.CallID, reviewOf(r, consts.ActionExecute)); return e },
		"cancel": func() error { _, e := svc.Cancel(bg, r.CallID, "a"); return e },
	} {
		err := call()
		if code, _ := errcode.CodeOf(err); code != errcode.CASConflict {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

// 并发：两个 goroutine 审核同一申请，恰好一个成功，另一个得到 cas_conflict 或 invalid_transition。
func TestConcurrentReviewExactlyOneWins(t *testing.T) {
	for i := 0; i < 40; i++ {
		f := newFixture(t)
		r := f.inspect(manualInput())
		start := make(chan struct{})
		errs := make([]error, 2)
		var wg sync.WaitGroup
		for g := 0; g < 2; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				<-start
				_, errs[g] = f.svc.Review(bg, r.CallID, reviewOf(r, consts.ActionExecute))
			}(g)
		}
		close(start)
		wg.Wait()
		wins := 0
		for _, err := range errs {
			if err == nil {
				wins++
				continue
			}
			code, _ := errcode.CodeOf(err)
			if code != errcode.CASConflict && code != errcode.InvalidTransition {
				t.Fatalf("loser error = %v", err)
			}
		}
		if wins != 1 {
			t.Fatalf("iteration %d: %d winners (%v)", i, wins, errs)
		}
		if got := f.get(r.CallID); got.State != consts.RequestFrozen || len(got.Reviews) != 1 {
			t.Fatalf("state=%s reviews=%d", got.State, len(got.Reviews))
		}
		// 恰好一份 approved 事件。
		n := 0
		for _, e := range f.env.Requests.Events() {
			if e.Type == events.ReviewApproved {
				n++
			}
		}
		if n != 1 || len(f.env.Audit.Records()) != 1 {
			t.Fatalf("approved events = %d audit = %d", n, len(f.env.Audit.Records()))
		}
	}
}

// 并发执行：同一冻结申请被两个 goroutine 执行，执行器只被调用一次。
func TestConcurrentExecuteRunsOnce(t *testing.T) {
	for i := 0; i < 40; i++ {
		f := newFixture(t)
		r := f.inspect(input())
		start := make(chan struct{})
		errs := make([]error, 2)
		var wg sync.WaitGroup
		for g := 0; g < 2; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				<-start
				_, errs[g] = f.svc.Execute(bg, r.CallID)
			}(g)
		}
		close(start)
		wg.Wait()
		if f.env.Executor.Calls() != 1 {
			t.Fatalf("iteration %d: executor calls = %d (%v)", i, f.env.Executor.Calls(), errs)
		}
		wins := 0
		for _, err := range errs {
			if err == nil {
				wins++
			}
		}
		if wins != 1 {
			t.Fatalf("winners = %d (%v)", wins, errs)
		}
	}
}
