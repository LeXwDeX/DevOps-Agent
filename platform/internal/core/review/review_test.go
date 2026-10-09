package review

import (
	"reflect"
	"testing"
	"time"

	corereq "github.com/LeXwDeX/DevOps-Agent/platform/internal/core/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/ids"
	mpol "github.com/LeXwDeX/DevOps-Agent/platform/internal/model/policy"
	mreq "github.com/LeXwDeX/DevOps-Agent/platform/internal/model/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/consts"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

var now = time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)

func newReq(t *testing.T, state consts.RequestState) *mreq.ExecutionRequest {
	t.Helper()
	r, err := corereq.New(corereq.NewParams{
		CallID: "c1", Source: consts.SourceRegisteredTool, SessionID: "s",
		Scope:   mreq.Scope{Project: "p", Environment: "prod", Targets: []string{"a"}},
		Content: mreq.Content{Rendered: "echo hi"},
		Expectation: mreq.Expectation{
			Intent: "hi", Targets: []string{"a"}, Category: consts.CategoryWrite,
			ExpectedChange: mreq.ExpectedChange{Text: "x"}, ExpectedOutput: "hi",
			SuccessCriteria: []mreq.Criterion{{Expr: "exit_code == 0"}}, Reversible: consts.Irreversible,
		},
		Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.State = state
	r.Fingerprint = &mreq.Fingerprint{Status: consts.FingerprintCollected, Value: "fp"}
	r.Checks.Policy = &mpol.Decision{Mode: consts.PolicyManual, Layer: consts.LayerSystem}
	return r
}

func code(err error) errcode.Code { c, _ := errcode.CodeOf(err); return c }

func TestActExecute(t *testing.T) {
	r := newReq(t, consts.RequestAwaitingReview)
	want, _ := r.FreezeDigest()
	if err := Act(r, consts.ActionExecute, r.Revision, r.Digest, "alice", "ok", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if r.State != consts.RequestFrozen || r.Frozen == nil || r.Frozen.Digest != want || !r.Frozen.At.Equal(now.Add(time.Minute)) {
		t.Fatalf("r = %+v", r)
	}
	if len(r.Reviews) != 1 || r.Reviews[0].Action != consts.ActionExecute || r.Reviews[0].Actor != "alice" ||
		r.Reviews[0].Revision != 1 || r.Reviews[0].Digest != r.Digest || r.Reviews[0].Comment != "ok" {
		t.Fatalf("reviews = %+v", r.Reviews)
	}
	if !ApprovedFor(r) {
		t.Error("not approved")
	}
	if r.Version != 1 {
		t.Error("Act changed Version")
	}
}

func TestActReject(t *testing.T) {
	r := newReq(t, consts.RequestAwaitingReview)
	if err := Act(r, consts.ActionReject, r.Revision, r.Digest, "alice", "too risky", now); err != nil {
		t.Fatal(err)
	}
	if r.State != consts.RequestRejected || r.Rejection == nil || r.Rejection.Source != consts.RejectedByHuman ||
		r.Rejection.Reason != "too risky" || r.Frozen != nil || ApprovedFor(r) {
		t.Fatalf("r = %+v", r)
	}
	// 拒绝可以没有点评
	r = newReq(t, consts.RequestAwaitingReview)
	if err := Act(r, consts.ActionReject, r.Revision, r.Digest, "alice", "", now); err != nil || r.State != consts.RequestRejected {
		t.Fatalf("reject w/o comment: %v", err)
	}
}

func TestActComment(t *testing.T) {
	r := newReq(t, consts.RequestAwaitingReview)
	if err := Act(r, consts.ActionComment, r.Revision, r.Digest, "alice", "use --dry-run", now); err != nil {
		t.Fatal(err)
	}
	if r.State != consts.RequestAwaitingRevision || len(r.Reviews) != 1 || r.Reviews[0].Comment != "use --dry-run" {
		t.Fatalf("r = %+v", r)
	}
	for _, empty := range []string{"", "   \n"} {
		r = newReq(t, consts.RequestAwaitingReview)
		before := r.Clone()
		if err := Act(r, consts.ActionComment, r.Revision, r.Digest, "alice", empty, now); code(err) != errcode.InvalidArgument {
			t.Errorf("empty comment %q: %v", empty, err)
		}
		if !reflect.DeepEqual(r, before) {
			t.Error("r mutated on failure")
		}
	}
}

func TestActStateAndArgs(t *testing.T) {
	for _, st := range consts.RequestStates() {
		if st == consts.RequestAwaitingReview {
			continue
		}
		r := newReq(t, st)
		before := r.Clone()
		if err := Act(r, consts.ActionExecute, r.Revision, r.Digest, "a", "", now); code(err) != errcode.InvalidTransition {
			t.Errorf("%s: %v", st, err)
		}
		if !reflect.DeepEqual(r, before) {
			t.Errorf("%s: mutated", st)
		}
	}
	r := newReq(t, consts.RequestAwaitingReview)
	if err := Act(r, "bogus", r.Revision, r.Digest, "a", "x", now); code(err) != errcode.InvalidArgument {
		t.Errorf("bogus action: %v", err)
	}
	if err := Act(r, consts.ActionExecute, r.Revision, r.Digest, " ", "", now); code(err) != errcode.InvalidArgument {
		t.Errorf("empty actor: %v", err)
	}
	if r.State != consts.RequestAwaitingReview || len(r.Reviews) != 0 {
		t.Error("failed Act changed r")
	}
}

func TestActStale(t *testing.T) {
	for _, a := range consts.HumanActions() {
		r := newReq(t, consts.RequestAwaitingReview)
		before := r.Clone()
		if err := Act(r, a, r.Revision+1, r.Digest, "a", "c", now); code(err) != errcode.ReviewStale {
			t.Errorf("%s wrong revision: %v", a, err)
		}
		if err := Act(r, a, r.Revision, ids.Digest("0000"), "a", "c", now); code(err) != errcode.ReviewStale {
			t.Errorf("%s wrong digest: %v", a, err)
		}
		if !reflect.DeepEqual(r, before) {
			t.Errorf("%s: mutated on stale", a)
		}
	}
}

// 修订后，针对旧修订的批准失效：既不能再用旧 Revision/Digest 提交，已有的批准记录也不再算数。
func TestStaleApprovalAfterRevise(t *testing.T) {
	r := newReq(t, consts.RequestAwaitingReview)
	oldRev, oldDigest := r.Revision, r.Digest
	// 模拟一条绑定旧修订的批准（例如审核页面打开时记录的），随后点评、修订。
	r.Reviews = append(r.Reviews, mreq.ReviewEntry{Action: consts.ActionExecute, Actor: "a", Revision: oldRev, Digest: oldDigest})
	if !ApprovedFor(r) {
		t.Fatal("setup: should be approved")
	}
	r.State = consts.RequestChecking
	c := r.Content.Clone()
	c.Rendered = "echo changed"
	if _, err := corereq.Revise(r, c, r.Expectation, consts.RevisionSelfReview, now); err != nil {
		t.Fatal(err)
	}
	if ApprovedFor(r) {
		t.Fatal("old approval still effective after Revise")
	}
	if len(r.Reviews) != 1 {
		t.Fatal("history must be kept")
	}
	// 回到 awaiting_review 后，用旧编号提交会被拒绝
	r.State = consts.RequestAwaitingReview
	if err := Act(r, consts.ActionExecute, oldRev, oldDigest, "a", "", now); code(err) != errcode.ReviewStale {
		t.Errorf("stale execute: %v", err)
	}
	// 且不能靠旧批准绕过人工：直接 Freeze 也不行
	if err := Freeze(r, now); code(err) != errcode.ReviewStale {
		t.Errorf("Freeze with stale approval: %v", err)
	}
	if r.State != consts.RequestAwaitingReview || r.Frozen != nil {
		t.Error("state changed")
	}
	// 用新编号批准成功
	if err := Act(r, consts.ActionExecute, r.Revision, r.Digest, "a", "", now); err != nil {
		t.Fatal(err)
	}
	if !ApprovedFor(r) || r.State != consts.RequestFrozen {
		t.Error("new approval failed")
	}
}

func TestApprovedFor(t *testing.T) {
	r := newReq(t, consts.RequestAwaitingReview)
	if ApprovedFor(r) {
		t.Error("empty reviews approved")
	}
	for _, a := range []consts.HumanAction{consts.ActionReject, consts.ActionComment} {
		r.Reviews = []mreq.ReviewEntry{{Action: a, Revision: r.Revision, Digest: r.Digest}}
		if ApprovedFor(r) {
			t.Errorf("%s counts as approval", a)
		}
	}
	r.Reviews = []mreq.ReviewEntry{{Action: consts.ActionExecute, Revision: r.Revision, Digest: "x"}}
	if ApprovedFor(r) {
		t.Error("wrong digest approved")
	}
	r.Reviews = []mreq.ReviewEntry{{Action: consts.ActionExecute, Revision: r.Revision + 1, Digest: r.Digest}}
	if ApprovedFor(r) {
		t.Error("wrong revision approved")
	}
}

func TestFreezeFromChecking(t *testing.T) {
	r := newReq(t, consts.RequestChecking)
	want, _ := r.FreezeDigest()
	if err := Freeze(r, now); err != nil {
		t.Fatal(err)
	}
	if r.State != consts.RequestFrozen || r.Frozen.Digest != want {
		t.Fatalf("r = %+v", r)
	}
	// 冻结后不能再冻结，也不能修订
	if err := Freeze(r, now); code(err) != errcode.InvalidTransition {
		t.Errorf("double freeze: %v", err)
	}
	if _, err := corereq.Revise(r, r.Content, r.Expectation, consts.RevisionSelfReview, now); code(err) != errcode.FrozenImmutable {
		t.Errorf("revise after freeze: %v", err)
	}
}

func TestFreezeWrongStates(t *testing.T) {
	for _, st := range consts.RequestStates() {
		if st == consts.RequestChecking || st == consts.RequestAwaitingReview {
			continue
		}
		r := newReq(t, st)
		before := r.Clone()
		err := Freeze(r, now)
		want := errcode.InvalidTransition
		if st.IsTerminal() {
			want = errcode.TerminalState
		}
		if code(err) != want {
			t.Errorf("%s: %v, want %s", st, err, want)
		}
		if !reflect.DeepEqual(r, before) {
			t.Errorf("%s: mutated", st)
		}
	}
}

func TestFreezeDetectsTamperedContent(t *testing.T) {
	r := newReq(t, consts.RequestChecking)
	r.Content.Rendered = "rm -rf /" // 绕过 Revise 直接改
	before := r.Clone()
	if err := Freeze(r, now); code(err) != errcode.DigestMismatch {
		t.Fatalf("err = %v", err)
	}
	if !reflect.DeepEqual(r, before) {
		t.Error("mutated")
	}
}

func TestFreezeDigestCoversFingerprintAndPolicy(t *testing.T) {
	a := newReq(t, consts.RequestChecking)
	b := newReq(t, consts.RequestChecking)
	b.Fingerprint.Value = "other"
	if err := Freeze(a, now); err != nil {
		t.Fatal(err)
	}
	if err := Freeze(b, now); err != nil {
		t.Fatal(err)
	}
	if a.Frozen.Digest == b.Frozen.Digest {
		t.Error("fingerprint not covered by freeze digest")
	}
}
