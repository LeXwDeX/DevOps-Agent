package pipeline_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/coretest"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/events"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/pipeline"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/ports"
	corereq "github.com/LeXwDeX/DevOps-Agent/platform/internal/core/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/consts"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

func TestNewRequiresAllPorts(t *testing.T) {
	f := newFixture(t)
	v := reflect.ValueOf(f.deps)
	for i := 0; i < v.NumField(); i++ {
		d := f.deps
		reflect.ValueOf(&d).Elem().Field(i).SetZero()
		if _, err := pipeline.New(d); err == nil {
			t.Errorf("missing %s accepted", v.Type().Field(i).Name)
		} else {
			wantCode(t, err, errcode.InvalidArgument)
		}
	}
}

func TestSubmit(t *testing.T) {
	f := newFixture(t)
	r := f.submit(input())
	if r.CallID != "call-1" || r.State != consts.RequestDraft || r.Revision != 1 || !r.Digest.Valid() {
		t.Fatalf("r = %+v", r)
	}
	evts := f.env.Requests.Events()
	if len(evts) != 1 || evts[0].Type != events.CallProposed || evts[0].Sequence != 1 || evts[0].Status != "draft" {
		t.Fatalf("events = %+v", evts)
	}
	if evts[0].StreamID != "call:call-1" || evts[0].Data["source"] != "registered_tool" {
		t.Errorf("event = %+v", evts[0])
	}
	assertNoLeak(t, evts)
	if got := f.store.types(); !reflect.DeepEqual(got, [][]events.Type{{events.CallProposed}}) {
		t.Errorf("batches = %v", got)
	}
}

func TestSubmitRejectsBadInput(t *testing.T) {
	f := newFixture(t)
	for _, src := range []consts.RequestSource{consts.SourcePreAuthorized, consts.SourceHumanTerminal} {
		in := input()
		in.Source = src
		_, err := f.svc.Submit(bg, in)
		wantCode(t, err, errcode.SourceNotSupported)
	}
	in := input()
	in.Source = "bogus"
	_, err := f.svc.Submit(bg, in)
	wantCode(t, err, errcode.InvalidArgument)
	in = input()
	in.Source = consts.SourceTaskStep // 步骤申请只由编排引擎创建，不经 Submit
	in.Task = &request.TaskRef{RunID: "run-1", StepID: "A"}
	_, err = f.svc.Submit(bg, in)
	wantCode(t, err, errcode.SourceNotSupported)
	if n := len(f.env.Requests.Events()); n != 0 {
		t.Errorf("failed submits saved %d events", n)
	}
	// 存储故障原样返回
	f.env.Requests.FailNext("Create", 1, coretest.ErrInjected)
	if _, err := f.svc.Submit(bg, input()); !errors.Is(err, coretest.ErrInjected) {
		t.Errorf("err = %v", err)
	}
}

func TestAutoPathEventSequence(t *testing.T) {
	f := newFixture(t)
	r := f.inspect(input())
	wantState(t, r, consts.RequestFrozen)
	out, err := f.svc.Execute(bg, r.CallID)
	if err != nil {
		t.Fatal(err)
	}
	wantState(t, out, consts.RequestSucceeded)

	evts := f.env.Requests.Events()
	want := []events.Type{
		events.CallProposed, events.InspectionStarted, events.InspectionPassed, events.PolicyDecided,
		events.CallFrozen, events.ExecutionStarted, events.ExecutionCompleted,
	}
	if got := typesOf(evts); !sameTypes(got, want) {
		t.Fatalf("events = %v\nwant %v", got, want)
	}
	for i, e := range evts {
		if e.Sequence != int64(i+1) || e.StreamID != "call:"+string(r.CallID) || e.SourceKind != events.SourceHostConfirmed {
			t.Errorf("event %d = %+v", i, e)
		}
		if err := e.Validate(); err != nil {
			t.Errorf("event %d invalid: %v", i, err)
		}
	}
	// 事件的 Status 是该事件之后的状态。
	wantStatus := []string{"draft", "checking", "checking", "checking", "frozen", "executing", "succeeded"}
	for i, e := range evts {
		if e.Status != wantStatus[i] {
			t.Errorf("event %s status = %s, want %s", e.Type, e.Status, wantStatus[i])
		}
	}
	// 每次保存都带着本次转换的事件。
	wantBatches := [][]events.Type{
		{events.CallProposed},
		{events.InspectionStarted, events.InspectionPassed, events.PolicyDecided, events.CallFrozen},
		{events.ExecutionStarted},
		{events.ExecutionCompleted},
	}
	if got := f.store.types(); !reflect.DeepEqual(got, wantBatches) {
		t.Errorf("batches = %v\nwant %v", got, wantBatches)
	}
	assertNoLeak(t, evts)
	if out.EventSeq != 7 {
		t.Errorf("EventSeq = %d", out.EventSeq)
	}
	// 版本：create 1，之后每次保存加一。
	if out.Version != 4 {
		t.Errorf("Version = %d, want 4", out.Version)
	}
}

func TestInspectRecordsVersionsAndChecks(t *testing.T) {
	f := newFixture(t)
	r := f.inspect(input())
	wantState(t, r, consts.RequestFrozen)
	if r.Versions.PolicyVersion != "p1" || r.Versions.PolicyLayer != consts.LayerGlobal ||
		r.Versions.DCGRulesetVersion != "fake-1" || r.Versions.FingerprintMethodVersion != "v1" {
		t.Errorf("versions = %+v", r.Versions)
	}
	c := r.Checks
	if c.Policy == nil || c.Policy.Mode != consts.PolicyAuto || c.Preview.Status != consts.PreviewPassed ||
		c.DCG.Result != consts.DCGPass || c.SelfReview.Verdict != consts.VerdictNoRisk || len(c.Escalations) != 0 {
		t.Errorf("checks = %+v", c)
	}
	if r.Fingerprint == nil || r.Fingerprint.Value != coretest.DefaultFingerprintValue {
		t.Errorf("fingerprint = %+v", r.Fingerprint)
	}
	if r.Frozen == nil || !r.Frozen.Digest.Valid() {
		t.Errorf("frozen = %+v", r.Frozen)
	}
	if got := f.get(r.CallID); !reflect.DeepEqual(got, r) {
		t.Error("returned request differs from stored")
	}
}

func TestInspectStateGuards(t *testing.T) {
	f := newFixture(t)
	r := f.inspect(input())
	_, err := f.svc.Inspect(bg, r.CallID) // frozen
	wantCode(t, err, errcode.InvalidTransition)
	if _, err := f.svc.Cancel(bg, r.CallID, "u"); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.Inspect(bg, r.CallID) // stopped
	wantCode(t, err, errcode.TerminalState)
	_, err = f.svc.Inspect(bg, "nope")
	wantCode(t, err, errcode.NotFound)
	_, err = f.svc.Inspect(bg, "")
	wantCode(t, err, errcode.InvalidArgument)
}

// 61 第 9 节：转人工、dcg、不可逆、独立命令。每行是一个单独触发的条件。
func TestInspectOutcomes(t *testing.T) {
	type row struct {
		name   string
		setup  func(f *fixture, in *pipeline.SubmitInput)
		want   consts.RequestState
		reason string // Checks.Escalations 里应包含的片段（转人工时）
	}
	rows := []row{
		{"all clear is auto", func(*fixture, *pipeline.SubmitInput) {}, consts.RequestFrozen, ""},
		{"dcg hit", func(f *fixture, _ *pipeline.SubmitInput) { f.env.DCG.PushResult(consts.DCGHit, "rule-1") }, consts.RequestAwaitingReview, "dcg"},
		{"dcg unavailable result", func(f *fixture, _ *pipeline.SubmitInput) { f.env.DCG.PushResult(consts.DCGUnavailable) }, consts.RequestAwaitingReview, "dcg"},
		{"dcg port error", func(f *fixture, _ *pipeline.SubmitInput) { f.env.DCG.Push(request.DCGCheck{}, coretest.ErrInjected) }, consts.RequestAwaitingReview, "dcg"},
		{"dcg garbage result", func(f *fixture, _ *pipeline.SubmitInput) { f.env.DCG.Push(request.DCGCheck{Result: "???"}, nil) }, consts.RequestAwaitingReview, "dcg"},
		{"dcg forbid", func(f *fixture, _ *pipeline.SubmitInput) { f.env.DCG.PushResult(consts.DCGForbid, "rule-9") }, consts.RequestRejected, ""},
		{"preview failed", func(f *fixture, _ *pipeline.SubmitInput) {
			f.env.Preview.Push(request.PreviewResult{Status: consts.PreviewFailed, Output: "boom"}, nil)
		}, consts.RequestAwaitingReview, "预演"},
		{"preview port error", func(f *fixture, _ *pipeline.SubmitInput) {
			f.env.Preview.Push(request.PreviewResult{}, coretest.ErrInjected)
		}, consts.RequestAwaitingReview, "预演"},
		{"preview garbage status", func(f *fixture, _ *pipeline.SubmitInput) {
			f.env.Preview.Push(request.PreviewResult{Status: consts.PreviewNone}, nil)
		}, consts.RequestAwaitingReview, "预演"},
		{"fingerprint unavailable", func(f *fixture, _ *pipeline.SubmitInput) { f.env.Fingerprint.PushUnavailable() }, consts.RequestAwaitingReview, "指纹"},
		{"fingerprint port error", func(f *fixture, _ *pipeline.SubmitInput) {
			f.env.Fingerprint.Push(request.Fingerprint{}, coretest.ErrInjected)
		}, consts.RequestAwaitingReview, "指纹"},
		{"fingerprint collected without value", func(f *fixture, _ *pipeline.SubmitInput) {
			f.env.Fingerprint.Push(request.Fingerprint{Status: consts.FingerprintCollected}, nil)
		}, consts.RequestAwaitingReview, "指纹"},
		{"self review missing", func(_ *fixture, in *pipeline.SubmitInput) { in.Submission.SelfReview = nil }, consts.RequestAwaitingReview, "自审"},
		{"self review risk", func(_ *fixture, in *pipeline.SubmitInput) {
			in.Submission.SelfReview = &request.SelfReview{Verdict: consts.VerdictRisk}
		}, consts.RequestAwaitingReview, "自审"},
		{"self review not_applicable outside task_step", func(_ *fixture, in *pipeline.SubmitInput) {
			in.Submission.SelfReview = &request.SelfReview{Verdict: consts.VerdictNotApplicable}
		}, consts.RequestAwaitingReview, "自审"},
		{"hook requires manual", func(_ *fixture, in *pipeline.SubmitInput) { in.Submission.HookRequireManual = true }, consts.RequestAwaitingReview, "Hook"},
		{"analysis failed", func(_ *fixture, in *pipeline.SubmitInput) { in.Submission.AnalysisFailed = true }, consts.RequestAwaitingReview, "无法分析"},
		{"category unknown by registration", func(f *fixture, _ *pipeline.SubmitInput) {
			f.setUnit(func(u *ports.UnitInfo) { u.Category = consts.CategoryUnknown })
		}, consts.RequestAwaitingReview, "类别"},
		{"category empty by registration is unknown", func(f *fixture, _ *pipeline.SubmitInput) {
			f.setUnit(func(u *ports.UnitInfo) { u.Category = "" })
		}, consts.RequestAwaitingReview, "类别"},
		{"irreversible without preview, policy auto", func(f *fixture, _ *pipeline.SubmitInput) {
			f.setUnit(func(u *ports.UnitInfo) { u.Reversible, u.HasPreview = consts.Irreversible, false })
		}, consts.RequestAwaitingReview, "不可逆"},
		{"reversibility unknown without preview", func(f *fixture, _ *pipeline.SubmitInput) {
			f.setUnit(func(u *ports.UnitInfo) { u.Reversible, u.HasPreview = consts.ReversibilityUnknown, false })
		}, consts.RequestAwaitingReview, "不可逆"},
		{"reversibility empty by registration without preview", func(f *fixture, _ *pipeline.SubmitInput) {
			f.setUnit(func(u *ports.UnitInfo) { u.Reversible, u.HasPreview = "", false })
		}, consts.RequestAwaitingReview, "不可逆"},
		{"irreversible with passing preview follows policy", func(f *fixture, _ *pipeline.SubmitInput) {
			f.setUnit(func(u *ports.UnitInfo) { u.Reversible, u.HasPreview = consts.Irreversible, true })
		}, consts.RequestFrozen, ""},
		{"unknown reversibility with passing preview follows policy", func(f *fixture, _ *pipeline.SubmitInput) {
			f.setUnit(func(u *ports.UnitInfo) { u.Reversible, u.HasPreview = consts.ReversibilityUnknown, true })
		}, consts.RequestFrozen, ""},
		{"policy manual", func(f *fixture, _ *pipeline.SubmitInput) { f.setPolicy(consts.PolicyManual) }, consts.RequestAwaitingReview, ""},
		{"policy forbid", func(f *fixture, _ *pipeline.SubmitInput) { f.setPolicy(consts.PolicyForbid) }, consts.RequestRejected, ""},
		{"session in manual mode", func(f *fixture, _ *pipeline.SubmitInput) { f.setSessionMode(consts.SessionManual) }, consts.RequestAwaitingReview, ""},
	}
	for _, rw := range rows {
		t.Run(rw.name, func(t *testing.T) {
			f := newFixture(t)
			in := input()
			rw.setup(f, &in)
			r := f.inspect(in)
			wantState(t, r, rw.want)
			evts := f.env.Requests.Events()
			switch rw.want {
			case consts.RequestFrozen:
				if _, ok := hasType(evts, events.CallFrozen); !ok || r.Frozen == nil {
					t.Error("no frozen event")
				}
			case consts.RequestAwaitingReview:
				if _, ok := hasType(evts, events.ReviewRequested); !ok || r.Frozen != nil {
					t.Error("manual path must request review and not freeze")
				}
				if rw.reason != "" && !strings.Contains(strings.Join(r.Checks.Escalations, "|"), rw.reason) {
					t.Errorf("escalations %v lack %q", r.Checks.Escalations, rw.reason)
				}
				if r.Checks.Policy.Mode != consts.PolicyManual {
					t.Errorf("policy mode = %s", r.Checks.Policy.Mode)
				}
			case consts.RequestRejected:
				if r.Rejection == nil {
					t.Fatal("no rejection")
				}
				e, ok := hasType(evts, events.PolicyDecided)
				if !ok || e.Data["mode"] != "forbid" || e.Status != "rejected" {
					t.Errorf("policy.decided = %+v", e)
				}
			}
			assertNoLeak(t, evts)
			if f.env.Executor.Calls() != 0 {
				t.Error("inspection must never execute")
			}
		})
	}
}

func TestDCGForbidIsRejectedWithRuleAndNoExecution(t *testing.T) {
	f := newFixture(t)
	f.env.DCG.PushResult(consts.DCGForbid, "rule-9")
	r := f.inspect(input())
	wantState(t, r, consts.RequestRejected)
	if r.Rejection.Source != consts.RejectedByDCG || !strings.Contains(r.Rejection.Reason, "rule-9") {
		t.Errorf("rejection = %+v", r.Rejection)
	}
	want := []events.Type{events.CallProposed, events.InspectionStarted, events.InspectionPassed, events.PolicyDecided}
	if got := eventTypes(f); !sameTypes(got, want) {
		t.Errorf("events = %v", got)
	}
	if _, err := f.svc.Execute(bg, r.CallID); err == nil {
		t.Error("rejected request executed")
	}
	if f.env.Executor.Calls() != 0 {
		t.Error("executor called")
	}
}

func TestPolicyForbidIsRejected(t *testing.T) {
	f := newFixture(t)
	f.setPolicy(consts.PolicyForbid)
	r := f.inspect(input())
	wantState(t, r, consts.RequestRejected)
	if r.Rejection.Source != consts.RejectedByPolicy || r.Versions.PolicyLayer != consts.LayerGlobal {
		t.Errorf("rejection = %+v versions=%+v", r.Rejection, r.Versions)
	}
	if got := eventTypes(f); got[len(got)-1] != events.PolicyDecided {
		t.Errorf("events = %v", got)
	}
}

// 独立命令在任何策略和会话模式下都是 manual（K-10）；forbid 不被降级。
func TestAdHocIsAlwaysManual(t *testing.T) {
	for _, pm := range []consts.PolicyMode{consts.PolicyAuto, consts.PolicyManual} {
		for _, sm := range []consts.SessionMode{consts.SessionAuto, consts.SessionManual} {
			t.Run(string(pm)+"/"+string(sm), func(t *testing.T) {
				f := newFixture(t)
				f.setPolicy(pm)
				f.setSessionMode(sm)
				r := f.inspect(adHocInput())
				wantState(t, r, consts.RequestAwaitingReview)
				if !strings.Contains(strings.Join(r.Checks.Escalations, "|"), "独立命令") {
					t.Errorf("escalations = %v", r.Checks.Escalations)
				}
			})
		}
	}
	f := newFixture(t)
	f.setPolicy(consts.PolicyForbid)
	wantState(t, f.inspect(adHocInput()), consts.RequestRejected)
}

func TestAdHocUsesAdHocSubjectAndZoneFromCategory(t *testing.T) {
	f := newFixture(t)
	// 只有 read 区的 ad_hoc 规则为 forbid：读类命令被禁止，写类命令不受影响（走系统兜底后转人工）。
	f.env.Policy.Set(policyZone("ad_hoc", consts.ZoneRead, consts.PolicyForbid))
	in := adHocInput()
	in.Expectation.Category = consts.CategoryReadOnly
	wantState(t, f.inspect(in), consts.RequestRejected)
	wantState(t, f.inspect(adHocInput()), consts.RequestAwaitingReview)
}

func TestRegisteredUnitUsesRegisteredZoneAndAction(t *testing.T) {
	f := newFixture(t)
	f.env.Policy.Set(policyAction("svc", "restart", consts.PolicyForbid))
	wantState(t, f.inspect(input()), consts.RequestRejected)
	f.setUnit(func(u *ports.UnitInfo) { u.Action = "status" })
	wantState(t, f.inspect(input()), consts.RequestAwaitingReview) // 没有规则命中，系统兜底 manual
}

func TestUnitOverridesExpectationAndRecomputesDigest(t *testing.T) {
	f := newFixture(t)
	f.setUnit(func(u *ports.UnitInfo) {
		u.Category, u.Reversible, u.HasPreview = consts.CategoryDelete, consts.Irreversible, true
	})
	in := input()
	in.Expectation.Category = consts.CategoryReadOnly // 提交方谎报
	in.Expectation.Reversible = consts.Reversible
	sub := f.submit(in)
	r, err := f.svc.Inspect(bg, sub.CallID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Expectation.Category != consts.CategoryDelete || r.Expectation.Reversible != consts.Irreversible || !r.Expectation.HasPreview {
		t.Errorf("expectation = %+v", r.Expectation)
	}
	if r.Digest == sub.Digest {
		t.Error("digest not recomputed after override")
	}
	if cd, _ := r.ContentDigest(); cd != r.Digest {
		t.Error("digest does not match the inspected content")
	}
	wantState(t, r, consts.RequestFrozen)
}

func TestScopeAndUnitFailures(t *testing.T) {
	type row struct {
		name  string
		setup func(f *fixture, in *pipeline.SubmitInput)
		code  errcode.Code
	}
	rows := []row{
		{"target outside scope", func(_ *fixture, in *pipeline.SubmitInput) {
			in.Scope.Targets = []string{"host-a", "host-z"}
			in.Expectation.Targets = []string{"host-a", "host-z"}
		}, errcode.ScopeViolation},
		{"unknown session", func(_ *fixture, in *pipeline.SubmitInput) { in.SessionID = "ghost" }, errcode.ScopeViolation},
		{"project differs from session", func(_ *fixture, in *pipeline.SubmitInput) { in.Scope.Project = "other" }, errcode.ScopeViolation},
		{"environment differs from session", func(_ *fixture, in *pipeline.SubmitInput) { in.Scope.Environment = "dev" }, errcode.ScopeViolation},
		{"unit digest differs", func(_ *fixture, in *pipeline.SubmitInput) {
			in.Content.Unit.Digest = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
		}, errcode.UnitMismatch},
		{"unit version not registered", func(_ *fixture, in *pipeline.SubmitInput) { in.Content.Unit.Version = 9 }, errcode.UnitMismatch},
		{"registered source without unit", func(_ *fixture, in *pipeline.SubmitInput) { in.Content.Unit = nil }, errcode.UnitMismatch},
		{"ad_hoc with unit", func(_ *fixture, in *pipeline.SubmitInput) { in.Source = consts.SourceAdHoc }, errcode.UnitMismatch},
	}
	for _, rw := range rows {
		t.Run(rw.name, func(t *testing.T) {
			f := newFixture(t)
			in := input()
			rw.setup(f, &in)
			r := f.inspect(in)
			wantState(t, r, consts.RequestRejected)
			if r.Rejection.Source != consts.RejectedByInspection || r.Rejection.Code != rw.code || r.Rejection.Reason == "" {
				t.Errorf("rejection = %+v", r.Rejection)
			}
			evts := f.env.Requests.Events()
			want := []events.Type{events.CallProposed, events.InspectionStarted, events.InspectionFailed}
			if got := typesOf(evts); !sameTypes(got, want) {
				t.Fatalf("events = %v", got)
			}
			last := evts[2]
			if last.Data["code"] != string(rw.code) || last.Status != "rejected" {
				t.Errorf("failed event = %+v", last)
			}
			// 失败发生在预演、指纹、dcg 之前。
			if f.env.Preview.Calls() != 0 || f.env.Fingerprint.Calls() != 0 || f.env.DCG.Calls() != 0 {
				t.Error("later steps ran after a step 1 failure")
			}
			assertNoLeak(t, evts)
		})
	}
}

func TestExpectationInvalidGoesToAwaitingRevision(t *testing.T) {
	f := newFixture(t)
	in := input()
	in.Expectation.Intent = ""
	in.Expectation.SuccessCriteria = nil
	r := f.inspect(in)
	wantState(t, r, consts.RequestAwaitingRevision)
	if r.Rejection != nil {
		t.Error("expectation_invalid must not reject")
	}
	if len(r.Checks.Escalations) != 1 || !strings.HasPrefix(r.Checks.Escalations[0], "core.expectation_invalid: ") {
		t.Errorf("escalations = %v", r.Checks.Escalations)
	}
	last := lastEvent(t, f)
	if last.Type != events.InspectionFailed || last.Data["code"] != "core.expectation_invalid" || last.Status != "awaiting_revision" {
		t.Errorf("last event = %+v", last)
	}
	if f.env.Preview.Calls() != 0 || f.env.DCG.Calls() != 0 {
		t.Error("later steps ran")
	}
	// 修订后重新检验，走到冻结。
	good := input()
	r2, err := f.svc.Revise(bg, r.CallID, pipeline.ReviseInput{
		Content: good.Content, Expectation: good.Expectation, Submission: good.Submission, Source: consts.RevisionSelfReview,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantState(t, r2, consts.RequestChecking)
	r3, err := f.svc.Inspect(bg, r.CallID)
	if err != nil {
		t.Fatal(err)
	}
	wantState(t, r3, consts.RequestFrozen)
	if r3.Revision != 2 || len(r3.Checks.Escalations) != 0 {
		t.Errorf("r3 revision=%d escalations=%v", r3.Revision, r3.Checks.Escalations)
	}
}

func TestExpectationTargetsMustMatchScope(t *testing.T) {
	f := newFixture(t)
	in := input()
	in.Expectation.Targets = []string{"host-a", "host-b"}
	wantState(t, f.inspect(in), consts.RequestAwaitingRevision)
}

func TestTaskStepSelfReviewNotApplicable(t *testing.T) {
	f := newFixture(t)
	in := input()
	in.Source = consts.SourceTaskStep
	in.Task = &request.TaskRef{RunID: "run-1", StepID: "A"}
	in.Submission.SelfReview = nil
	// task_step 不经 Submit（只由编排引擎创建）：这里像引擎一样直接建草稿写入存储，再检验。
	if _, err := f.svc.Submit(bg, in); err == nil {
		t.Fatal("Submit accepted a task_step request")
	} else {
		wantCode(t, err, errcode.SourceNotSupported)
	}
	draft, err := corereq.New(corereq.NewParams{
		CallID: "call-step", Source: in.Source, SessionID: in.SessionID, Task: in.Task,
		Scope: in.Scope, Content: in.Content, Expectation: in.Expectation, Submission: in.Submission, Now: f.env.Clock.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.env.Requests.Create(bg, draft, nil); err != nil {
		t.Fatal(err)
	}
	r, err := f.svc.Inspect(bg, draft.CallID)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	wantState(t, r, consts.RequestFrozen) // not_applicable 不算缺失
	if r.Checks.SelfReview == nil || r.Checks.SelfReview.Verdict != consts.VerdictNotApplicable {
		t.Errorf("self review = %+v", r.Checks.SelfReview)
	}
	if e, _ := hasType(f.env.Requests.Events(), events.InspectionPassed); e.Data["self_review"] != "not_applicable" || e.RunID != "run-1" {
		t.Errorf("passed event = %+v", e)
	}
	// 同样的提交换成已登记工具，自审缺失就转人工。
	f2 := newFixture(t)
	in2 := input()
	in2.Submission.SelfReview = nil
	r2 := f2.inspect(in2)
	wantState(t, r2, consts.RequestAwaitingReview)
	if r2.Checks.SelfReview != nil {
		t.Error("missing self review must stay nil")
	}
}

func TestTaskLaunchSourceWorks(t *testing.T) {
	f := newFixture(t)
	in := input()
	in.Source = consts.SourceTaskLaunch
	wantState(t, f.inspect(in), consts.RequestFrozen)
}

func TestInspectPortFailuresLeaveStateUnchanged(t *testing.T) {
	rows := []struct {
		name string
		fail func(f *fixture)
	}{
		{"policy source", func(f *fixture) { f.env.Policy.FailWith("Load", coretest.ErrInjected) }},
		{"session lookup", func(f *fixture) { f.env.Scope.FailWith("Session", coretest.ErrInjected) }},
		{"outside scope lookup", func(f *fixture) { f.env.Scope.FailWith("OutsideScope", coretest.ErrInjected) }},
		{"registry lookup", func(f *fixture) { f.env.Registry.FailWith("Unit", coretest.ErrInjected) }},
		{"store save", func(f *fixture) { f.env.Requests.FailNext("Save", 1, coretest.ErrInjected) }},
	}
	for _, rw := range rows {
		t.Run(rw.name, func(t *testing.T) {
			f := newFixture(t)
			sub := f.submit(input())
			before := f.get(sub.CallID)
			evtsBefore := len(f.env.Requests.Events())
			rw.fail(f)
			if _, err := f.svc.Inspect(bg, sub.CallID); !errors.Is(err, coretest.ErrInjected) {
				t.Fatalf("err = %v", err)
			}
			if got := f.get(sub.CallID); !reflect.DeepEqual(got, before) {
				t.Errorf("state changed: %s -> %s", before.State, got.State)
			}
			if len(f.env.Requests.Events()) != evtsBefore {
				t.Error("events saved despite failure")
			}
		})
	}
}

func TestReinspectOverwritesOldChecks(t *testing.T) {
	f := newFixture(t)
	f.env.DCG.PushResult(consts.DCGHit, "r")
	r := f.inspect(input()) // dcg hit → awaiting_review
	wantState(t, r, consts.RequestAwaitingReview)
	if _, err := f.svc.Review(bg, r.CallID, pipeline.ReviewInput{Action: consts.ActionComment, Revision: r.Revision, Digest: r.Digest, Actor: "u", Comment: "retry"}); err != nil {
		t.Fatal(err)
	}
	good := input()
	if _, err := f.svc.Revise(bg, r.CallID, pipeline.ReviseInput{Content: good.Content, Expectation: good.Expectation, Submission: good.Submission, Source: consts.RevisionHumanComment}); err != nil {
		t.Fatal(err)
	}
	r2, err := f.svc.Inspect(bg, r.CallID) // dcg 现在放行
	if err != nil {
		t.Fatal(err)
	}
	wantState(t, r2, consts.RequestFrozen)
	if r2.Checks.DCG.Result != consts.DCGPass {
		t.Errorf("dcg = %+v", r2.Checks.DCG)
	}
}

func policyZone(subject string, zone consts.Zone, mode consts.PolicyMode) policySet {
	return policySet{Version: "pz", Rules: []policyRule{{Layer: consts.LayerZone, Subject: subject, Zone: zone, Mode: policyModePtr(mode)}}}
}

func policyAction(subject, action string, mode consts.PolicyMode) policySet {
	return policySet{Version: "pa", Rules: []policyRule{{Layer: consts.LayerAction, Subject: subject, Action: action, Mode: policyModePtr(mode)}}}
}
