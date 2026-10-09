package request

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/ids"
	mreq "github.com/LeXwDeX/DevOps-Agent/platform/internal/model/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/consts"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/core"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

var now = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

// expected 是 61 第 4 节状态图的全部边，在测试里独立写一遍。
var expected = map[consts.RequestState][]consts.RequestState{
	consts.RequestDraft:            {consts.RequestChecking, consts.RequestStopped},
	consts.RequestChecking:         {consts.RequestRejected, consts.RequestAwaitingReview, consts.RequestFrozen, consts.RequestChecking, consts.RequestAwaitingRevision, consts.RequestStopped},
	consts.RequestAwaitingReview:   {consts.RequestFrozen, consts.RequestRejected, consts.RequestAwaitingRevision, consts.RequestStopped},
	consts.RequestAwaitingRevision: {consts.RequestChecking, consts.RequestStopped},
	consts.RequestFrozen:           {consts.RequestTargetChanged, consts.RequestExecuting, consts.RequestStopped},
	consts.RequestTargetChanged:    {consts.RequestChecking, consts.RequestStopped},
	consts.RequestExecuting:        {consts.RequestSucceeded, consts.RequestMismatched, consts.RequestFailed, consts.RequestUnknown},
}

func allowed(from, to consts.RequestState) bool {
	for _, t := range expected[from] {
		if t == to {
			return true
		}
	}
	return false
}

func TestTransitionTableExhaustive(t *testing.T) {
	states := consts.RequestStates()
	if len(states) != 13 {
		t.Fatalf("states = %d", len(states))
	}
	edges := 0
	for _, from := range states {
		for _, to := range states {
			want := allowed(from, to)
			if want {
				edges++
			}
			if got := CanTransition(from, to); got != want {
				t.Errorf("CanTransition(%s,%s) = %v, want %v", from, to, got, want)
			}
			r := &mreq.ExecutionRequest{CallID: "c", State: from, Version: 7}
			err := Transition(r, to, now)
			switch {
			case from.IsTerminal():
				if code, _ := errcode.CodeOf(err); code != errcode.TerminalState {
					t.Errorf("%s->%s: err = %v, want terminal_state", from, to, err)
				}
				if r.State != from {
					t.Errorf("%s->%s: state changed", from, to)
				}
			case want:
				if err != nil || r.State != to || !r.UpdatedAt.Equal(now) {
					t.Errorf("%s->%s: err=%v state=%s", from, to, err, r.State)
				}
			default:
				if code, _ := errcode.CodeOf(err); code != errcode.InvalidTransition {
					t.Errorf("%s->%s: err = %v, want invalid_transition", from, to, err)
				}
				if r.State != from || !r.UpdatedAt.IsZero() {
					t.Errorf("%s->%s: r mutated on failure", from, to)
				}
			}
			if r.Version != 7 {
				t.Errorf("%s->%s: Transition changed Version", from, to)
			}
		}
	}
	if edges != 23 {
		t.Errorf("edge count = %d, want 23", edges)
	}
}

func TestTransitionUnknownStates(t *testing.T) {
	r := &mreq.ExecutionRequest{State: "bogus"}
	if code, _ := errcode.CodeOf(Transition(r, consts.RequestChecking, now)); code != errcode.InvalidTransition {
		t.Error("unknown from")
	}
	r = &mreq.ExecutionRequest{State: consts.RequestDraft}
	if code, _ := errcode.CodeOf(Transition(r, "bogus", now)); code != errcode.InvalidTransition {
		t.Error("unknown to")
	}
	if CanTransition("bogus", "bogus") {
		t.Error("bogus pair allowed")
	}
}

func TestNextStatesIsCopy(t *testing.T) {
	n := NextStates(consts.RequestDraft)
	n[0] = "x"
	if NextStates(consts.RequestDraft)[0] == "x" {
		t.Fatal("exposes table")
	}
}

func TestRejectAndStop(t *testing.T) {
	r := &mreq.ExecutionRequest{State: consts.RequestChecking}
	if err := Reject(r, consts.RejectedByPolicy, errcode.InvalidArgument, "no", now); err != nil {
		t.Fatal(err)
	}
	if r.State != consts.RequestRejected || r.Rejection == nil || r.Rejection.Source != consts.RejectedByPolicy {
		t.Fatalf("r = %+v", r)
	}
	if err := Reject(&mreq.ExecutionRequest{State: consts.RequestChecking}, "bad", "", "", now); err == nil {
		t.Fatal("bad source accepted")
	}
	// 不允许的转换不留下 Rejection
	r2 := &mreq.ExecutionRequest{State: consts.RequestFrozen}
	if err := Reject(r2, consts.RejectedByHuman, "", "", now); err == nil || r2.Rejection != nil {
		t.Fatal("reject from frozen")
	}

	s := &mreq.ExecutionRequest{State: consts.RequestFrozen}
	if err := Stop(s, consts.StopRecoveryInterrupted, "restart", now); err != nil {
		t.Fatal(err)
	}
	if s.State != consts.RequestStopped || s.Stop.Reason != consts.StopRecoveryInterrupted || !s.Stop.At.Equal(now) {
		t.Fatalf("s = %+v", s)
	}
	if err := Stop(&mreq.ExecutionRequest{State: consts.RequestDraft}, "bad", "", now); err == nil {
		t.Fatal("bad reason accepted")
	}
	if err := Stop(&mreq.ExecutionRequest{State: consts.RequestExecuting}, consts.StopCancelledByHuman, "", now); err == nil {
		t.Fatal("stop from executing")
	}
}

func params() NewParams {
	return NewParams{
		CallID:    "call-1",
		Source:    consts.SourceRegisteredTool,
		SessionID: "s1",
		Scope:     mreq.Scope{Project: "p", Environment: "prod", Targets: []string{"b", "a", "b"}},
		Content: mreq.Content{
			Rendered: "echo hi", Params: map[string]any{"k": []any{"v"}}, SecretKeys: []string{"K"},
		},
		Expectation: mreq.Expectation{
			Intent: "say hi", Targets: []string{"a", "b"}, Category: consts.CategoryReadOnly,
			ExpectedChange:  mreq.ExpectedChange{Text: "none"},
			ExpectedOutput:  "hi",
			SuccessCriteria: []mreq.Criterion{{Expr: "exit_code == 0"}},
			Reversible:      consts.Irreversible,
		},
		Submission: mreq.Submission{SelfReview: &mreq.SelfReview{Verdict: consts.VerdictNoRisk}},
		Now:        now,
	}
}

func TestNew(t *testing.T) {
	p := params()
	r, err := New(p)
	if err != nil {
		t.Fatal(err)
	}
	if r.State != consts.RequestDraft || r.Revision != 1 || r.Version != 1 || r.EventSeq != 0 {
		t.Errorf("r = %+v", r)
	}
	if !reflect.DeepEqual(r.Scope.Targets, []string{"a", "b"}) {
		t.Errorf("targets = %v", r.Scope.Targets)
	}
	if r.Versions.SchemaID != core.ExecutionRequestV1 {
		t.Errorf("schema id = %q", r.Versions.SchemaID)
	}
	want, _ := r.ContentDigest()
	if r.Digest != want || !r.Digest.Valid() {
		t.Errorf("digest = %q want %q", r.Digest, want)
	}
	if !r.CreatedAt.Equal(now) || !r.UpdatedAt.Equal(now) {
		t.Error("times")
	}
	// 与输入隔离
	p.Content.Params["k"].([]any)[0] = "changed"
	p.Scope.Targets[0] = "zz"
	p.Submission.SelfReview.Verdict = consts.VerdictRisk
	if r.Content.Params["k"].([]any)[0] != "v" || r.Scope.Targets[0] != "a" || r.Submission.SelfReview.Verdict != consts.VerdictNoRisk {
		t.Error("New aliases input")
	}
}

func TestNewTaskStep(t *testing.T) {
	p := params()
	p.Source = consts.SourceTaskStep
	if _, err := New(p); !errors.Is(err, errcode.New(errcode.InvalidArgument, "")) {
		t.Errorf("task_step without task: %v", err)
	}
	p.Task = &mreq.TaskRef{RunID: "r1"}
	if _, err := New(p); err == nil {
		t.Error("task_step without step id accepted")
	}
	p.Task = &mreq.TaskRef{RunID: "r1", StepID: "A"}
	r, err := New(p)
	if err != nil || r.Task == nil || r.Task.StepID != "A" {
		t.Fatalf("r=%+v err=%v", r, err)
	}
	p.Task.StepID = "changed"
	if r.Task.StepID != "A" {
		t.Error("task ref aliased")
	}
}

func TestNewValidation(t *testing.T) {
	p := params()
	p.CallID = ""
	if _, err := New(p); err == nil {
		t.Error("empty call id accepted")
	}
	p = params()
	p.Source = "bogus"
	if _, err := New(p); !errors.Is(err, errcode.New(errcode.InvalidArgument, "")) {
		t.Errorf("bogus source: %v", err)
	}
	p = params()
	p.Source = consts.SourcePreAuthorized
	if _, err := New(p); err != nil {
		t.Errorf("pre_authorized can be created: %v", err)
	}
	p = params()
	p.Content.Params["bad"] = make(chan int)
	if _, err := New(p); err == nil {
		t.Error("unsupported params accepted")
	}
	for _, src := range consts.RequestSources() {
		p = params()
		p.Source = src
		p.Task = &mreq.TaskRef{RunID: "r", StepID: "s"}
		if _, err := New(p); err != nil {
			t.Errorf("source %s: %v", src, err)
		}
	}
}

// revisable 构造一个带齐检验结果、批准记录和冻结痕迹的申请。
func revisable(t *testing.T, state consts.RequestState) *mreq.ExecutionRequest {
	t.Helper()
	r, err := New(params())
	if err != nil {
		t.Fatal(err)
	}
	r.State = state
	r.Version = 5
	r.EventSeq = 3
	r.Fingerprint = &mreq.Fingerprint{Status: consts.FingerprintCollected, Value: "fp"}
	r.Checks = mreq.Checks{
		Preview:    &mreq.PreviewResult{Status: consts.PreviewPassed},
		SelfReview: &mreq.SelfReview{Verdict: consts.VerdictNoRisk},
		DCG:        &mreq.DCGCheck{Result: consts.DCGPass},
	}
	r.Reviews = []mreq.ReviewEntry{{Action: consts.ActionExecute, Actor: "u", Revision: 1, Digest: r.Digest}}
	r.Frozen = &mreq.Frozen{Digest: r.Digest}
	r.Rejection = &mreq.Rejection{Source: consts.RejectedByHuman}
	r.Execution = &mreq.Execution{}
	return r
}

func newContent() (mreq.Content, mreq.Expectation) {
	p := params()
	c := p.Content.Clone()
	c.Rendered = "echo bye"
	return c, p.Expectation.Clone()
}

func TestReviseAllowedStates(t *testing.T) {
	later := time.Hour
	for _, st := range []consts.RequestState{consts.RequestChecking, consts.RequestAwaitingRevision, consts.RequestTargetChanged} {
		for _, src := range consts.RevisionSources() {
			r := revisable(t, st)
			before := r.Clone()
			c, e := newContent()
			old, err := Revise(r, c, e, src, now.Add(later))
			if err != nil {
				t.Fatalf("%s/%s: %v", st, src, err)
			}
			if !reflect.DeepEqual(old, before) {
				t.Errorf("%s: old copy differs from pre-revision request", st)
			}
			if old == r {
				t.Error("old aliases r")
			}
			if r.Revision != 2 || r.State != consts.RequestChecking || !r.UpdatedAt.Equal(now.Add(later)) {
				t.Errorf("%s: revision=%d state=%s", st, r.Revision, r.State)
			}
			if r.Content.Rendered != "echo bye" {
				t.Error("content not replaced")
			}
			want, _ := r.ContentDigest()
			if r.Digest != want || r.Digest == before.Digest {
				t.Errorf("digest not recomputed")
			}
			if !reflect.DeepEqual(r.Checks, mreq.Checks{}) || r.Fingerprint != nil || r.Frozen != nil ||
				r.Rejection != nil || r.Execution != nil || !reflect.DeepEqual(r.Submission, mreq.Submission{}) {
				t.Errorf("%s: not cleared: %+v", st, r)
			}
			if len(r.Reviews) != 1 || r.Reviews[0].Revision != 1 {
				t.Errorf("reviews must be kept as history: %+v", r.Reviews)
			}
			if r.Version != 5 || r.EventSeq != 3 || r.CallID != before.CallID {
				t.Error("version/eventseq/callid must not change")
			}
			// 新值与入参隔离
			c.Params["k"].([]any)[0] = "x"
			if r.Content.Params["k"].([]any)[0] != "v" {
				t.Error("Revise aliases content")
			}
		}
	}
}

func TestReviseRejectedStates(t *testing.T) {
	c, e := newContent()
	for _, st := range consts.RequestStates() {
		switch st {
		case consts.RequestChecking, consts.RequestAwaitingRevision, consts.RequestTargetChanged:
			continue
		}
		r := revisable(t, st)
		before := r.Clone()
		old, err := Revise(r, c, e, consts.RevisionSelfReview, now)
		if old != nil || err == nil {
			t.Fatalf("%s: revised", st)
		}
		code, _ := errcode.CodeOf(err)
		want := errcode.InvalidTransition
		switch st {
		case consts.RequestFrozen, consts.RequestExecuting, consts.RequestSucceeded,
			consts.RequestMismatched, consts.RequestFailed, consts.RequestUnknown:
			want = errcode.FrozenImmutable
		}
		if code != want {
			t.Errorf("%s: code = %s, want %s", st, code, want)
		}
		if !reflect.DeepEqual(r, before) {
			t.Errorf("%s: r mutated on failure", st)
		}
	}
}

func TestReviseSourceAndErrors(t *testing.T) {
	c, e := newContent()
	r := revisable(t, consts.RequestChecking)
	before := r.Clone()
	for _, bad := range []consts.RevisionSource{"", "agent", "hook", "model"} {
		_, err := Revise(r, c, e, bad, now)
		if code, _ := errcode.CodeOf(err); code != errcode.InvalidRevisionSrc {
			t.Errorf("source %q: err = %v", bad, err)
		}
	}
	// 摘要失败时 r 不变
	c.Params["bad"] = make(chan int)
	if _, err := Revise(r, c, e, consts.RevisionHumanComment, now); err == nil {
		t.Error("unsupported params accepted")
	}
	if !reflect.DeepEqual(r, before) {
		t.Error("r mutated on failure")
	}
}

func TestReviseChain(t *testing.T) {
	r := revisable(t, consts.RequestChecking)
	var olds []*mreq.ExecutionRequest
	for i := 0; i < 3; i++ {
		c, e := newContent()
		c.Rendered = c.Rendered + string(rune('a'+i))
		old, err := Revise(r, c, e, consts.RevisionSelfReview, now)
		if err != nil {
			t.Fatal(err)
		}
		olds = append(olds, old)
	}
	if r.Revision != 4 || olds[0].Revision != 1 || olds[2].Revision != 3 {
		t.Errorf("revisions: r=%d olds=%d..%d", r.Revision, olds[0].Revision, olds[2].Revision)
	}
	if r.Revision != ids.Revision(4) {
		t.Fatal("type")
	}
}

func TestReviseRefusesTaskStep(t *testing.T) {
	// 登记过的步骤不由 Agent 修订（61 第 7.3 节）：否则改写后的内容会以步骤身份免去自审。
	c, e := newContent()
	for _, st := range []consts.RequestState{consts.RequestChecking, consts.RequestAwaitingRevision, consts.RequestTargetChanged} {
		r := revisable(t, st)
		r.Source = consts.SourceTaskStep
		r.Task = &mreq.TaskRef{RunID: "run-1", StepID: "A"}
		before := r.Clone()
		for _, src := range consts.RevisionSources() {
			_, err := Revise(r, c, e, src, now)
			if code, _ := errcode.CodeOf(err); code != errcode.SourceNotSupported {
				t.Errorf("%s/%s: err = %v", st, src, err)
			}
			if !reflect.DeepEqual(r, before) {
				t.Errorf("%s: r changed on refused revision", st)
			}
		}
	}
}
