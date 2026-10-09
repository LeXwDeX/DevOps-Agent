package request

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/ids"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/policy"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/consts"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func sample() *ExecutionRequest {
	min := 1.0
	return &ExecutionRequest{
		CallID:    "call-1",
		Revision:  1,
		Source:    consts.SourceRegisteredTool,
		SessionID: "s1",
		Scope:     Scope{Project: "p", Environment: "prod", Targets: []string{"host-b", "host-a"}},
		Content: Content{
			Unit:       &UnitRef{ID: "restart", Version: 2, Digest: ids.Digest(strings.Repeat("a", 64))},
			Rendered:   "systemctl restart nginx",
			Params:     map[string]any{"svc": "nginx", "opts": []any{"a", map[string]any{"k": "v"}}},
			SecretKeys: []string{"TOKEN_B", "TOKEN_A"},
		},
		Expectation: Expectation{
			Intent:          "restart nginx",
			Targets:         []string{"host-a", "host-b"},
			Category:        consts.CategoryStartStop,
			ExpectedChange:  ExpectedChange{Text: "nginx restarted", Ranges: []ValueRange{{Field: "rows", Min: &min}}},
			ExpectedOutput:  "active",
			SuccessCriteria: []Criterion{{Expr: "exit_code == 0"}, {Text: "page loads"}},
			Reversible:      consts.Reversible,
			Rollback:        "restart again",
			HasPreview:      true,
			RiskNote:        "brief downtime",
		},
		Submission:  Submission{SelfReview: &SelfReview{Verdict: consts.VerdictNoRisk}, HookReasons: []string{"x"}},
		Fingerprint: &Fingerprint{Method: "m", MethodVersion: "1", CollectedAt: t0, Status: consts.FingerprintCollected, Value: "fp-1"},
		Checks: Checks{
			Preview:     &PreviewResult{Status: consts.PreviewPassed, At: t0},
			SelfReview:  &SelfReview{Verdict: consts.VerdictNoRisk, At: t0},
			DCG:         &DCGCheck{Result: consts.DCGPass, RuleIDs: []string{"r1"}, At: t0},
			Policy:      &policy.Decision{Mode: consts.PolicyManual, Layer: consts.LayerSystem, PolicyVersion: "v1", Reasons: []string{"r"}, At: t0},
			Escalations: []string{"e"},
		},
		Reviews:   []ReviewEntry{{Action: consts.ActionComment, Actor: "u", At: t0, Revision: 1}},
		Execution: &Execution{Result: &ExecutionResult{Output: map[string]any{"a": []any{1}}}},
		State:     consts.RequestChecking,
		CreatedAt: t0, UpdatedAt: t0,
	}
}

func TestExpectationValidate(t *testing.T) {
	r := sample()
	if err := r.Expectation.Validate(r.Scope); err != nil {
		t.Fatalf("valid sample rejected: %v", err)
	}
	mut := func(f func(e *Expectation)) error {
		e := r.Expectation.Clone()
		f(&e)
		return e.Validate(r.Scope)
	}
	bad := map[string]func(e *Expectation){
		"intent":         func(e *Expectation) { e.Intent = " " },
		"targets":        func(e *Expectation) { e.Targets = nil },
		"category":       func(e *Expectation) { e.Category = "" },
		"unknown cat":    func(e *Expectation) { e.Category = "bogus" },
		"change":         func(e *Expectation) { e.ExpectedChange.Text = "" },
		"output":         func(e *Expectation) { e.ExpectedOutput = "" },
		"criteria":       func(e *Expectation) { e.SuccessCriteria = nil },
		"criterion both": func(e *Expectation) { e.SuccessCriteria = []Criterion{{Expr: "a", Text: "b"}} },
		"criterion none": func(e *Expectation) { e.SuccessCriteria = []Criterion{{}} },
		"reversible":     func(e *Expectation) { e.Reversible = "" },
		"unknown rev":    func(e *Expectation) { e.Reversible = "x" },
		"rollback":       func(e *Expectation) { e.Rollback = "" },
		"target subset":  func(e *Expectation) { e.Targets = []string{"host-a"} },
		"target extra":   func(e *Expectation) { e.Targets = []string{"host-a", "host-b", "host-c"} },
	}
	for name, f := range bad {
		err := mut(f)
		if code, _ := errcode.CodeOf(err); code != errcode.ExpectationInvalid {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	// 空串或空白的目标 ID 不合法，即使范围里也有同样的空白目标。
	for _, blank := range []string{"", "  ", "\t"} {
		e := r.Expectation.Clone()
		e.Targets = append(e.Targets, blank)
		sc := r.Scope.Clone()
		sc.Targets = append(sc.Targets, blank)
		err := e.Validate(sc)
		if code, _ := errcode.CodeOf(err); code != errcode.ExpectationInvalid || !strings.Contains(err.Error(), "target #") {
			t.Errorf("blank target %q: err = %v", blank, err)
		}
	}
	// 不可逆不需要 rollback；目标顺序和重复不影响一致性
	if err := mut(func(e *Expectation) { e.Reversible = consts.Irreversible; e.Rollback = "" }); err != nil {
		t.Errorf("irreversible without rollback: %v", err)
	}
	if err := mut(func(e *Expectation) { e.Reversible = consts.ReversibilityUnknown; e.Rollback = "" }); err != nil {
		t.Errorf("unknown reversible without rollback: %v", err)
	}
	if err := mut(func(e *Expectation) { e.Targets = []string{"host-b", "host-a", "host-a"} }); err != nil {
		t.Errorf("order/dup: %v", err)
	}
	// 列出全部问题
	err := mut(func(e *Expectation) { e.Intent = ""; e.ExpectedOutput = "" })
	if !strings.Contains(err.Error(), "intent") || !strings.Contains(err.Error(), "expected_output") {
		t.Errorf("want all problems listed: %v", err)
	}
}

func TestScopeNormalize(t *testing.T) {
	s := Scope{Targets: []string{"b", "a", "b"}}
	n := s.Normalize()
	if !reflect.DeepEqual(n.Targets, []string{"a", "b"}) {
		t.Fatalf("got %v", n.Targets)
	}
	if !reflect.DeepEqual(s.Targets, []string{"b", "a", "b"}) {
		t.Fatal("original mutated")
	}
}

func digests(t *testing.T, r *ExecutionRequest) (ids.Digest, ids.Digest) {
	t.Helper()
	c, err := r.ContentDigest()
	if err != nil {
		t.Fatal(err)
	}
	f, err := r.FreezeDigest()
	if err != nil {
		t.Fatal(err)
	}
	if !c.Valid() || !f.Valid() {
		t.Fatalf("invalid digests %q %q", c, f)
	}
	return c, f
}

func TestDigestOrderInsensitive(t *testing.T) {
	a := sample()
	b := sample()
	b.Scope.Targets = []string{"host-a", "host-b"}
	b.Expectation.Targets = []string{"host-b", "host-a"}
	b.Content.SecretKeys = []string{"TOKEN_A", "TOKEN_B"}
	b.Content.Params = map[string]any{"opts": []any{"a", map[string]any{"k": "v"}}, "svc": "nginx"}
	ca, fa := digests(t, a)
	cb, fb := digests(t, b)
	if ca != cb || fa != fb {
		t.Fatal("digest depends on order")
	}
	// ContentDigest 不修改原值
	if a.Content.SecretKeys[0] != "TOKEN_B" {
		t.Fatal("secret keys sorted in place")
	}
	// nil 与空等价
	c := sample()
	c.Content.Params = map[string]any{}
	d := sample()
	d.Content.Params = nil
	cc, _ := digests(t, c)
	cd, _ := digests(t, d)
	if cc != cd {
		t.Fatal("nil vs empty params differ")
	}
}

func TestDigestFieldSensitivity(t *testing.T) {
	baseC, baseF := digests(t, sample())
	min2 := 2.0
	type tc struct {
		name      string
		mut       func(r *ExecutionRequest)
		inContent bool // 是否影响 ContentDigest
		inFreeze  bool // 是否影响 FreezeDigest
	}
	cases := []tc{
		{"rendered", func(r *ExecutionRequest) { r.Content.Rendered += " " }, true, true},
		{"params", func(r *ExecutionRequest) { r.Content.Params["svc"] = "apache" }, true, true},
		{"nested params", func(r *ExecutionRequest) { r.Content.Params["opts"].([]any)[1].(map[string]any)["k"] = "w" }, true, true},
		{"secret keys", func(r *ExecutionRequest) { r.Content.SecretKeys = append(r.Content.SecretKeys, "X") }, true, true},
		{"unit id", func(r *ExecutionRequest) { r.Content.Unit.ID = "other" }, true, true},
		{"unit version", func(r *ExecutionRequest) { r.Content.Unit.Version++ }, true, true},
		{"unit digest", func(r *ExecutionRequest) { r.Content.Unit.Digest = ids.Digest(strings.Repeat("b", 64)) }, true, true},
		{"unit nil", func(r *ExecutionRequest) { r.Content.Unit = nil }, true, true},
		{"project", func(r *ExecutionRequest) { r.Scope.Project = "q" }, true, true},
		{"environment", func(r *ExecutionRequest) { r.Scope.Environment = "dev" }, true, true},
		{"targets", func(r *ExecutionRequest) { r.Scope.Targets = append(r.Scope.Targets, "host-c") }, true, true},
		{"intent", func(r *ExecutionRequest) { r.Expectation.Intent = "x" }, true, true},
		{"exp targets", func(r *ExecutionRequest) { r.Expectation.Targets = []string{"host-a"} }, true, true},
		{"category", func(r *ExecutionRequest) { r.Expectation.Category = consts.CategoryDelete }, true, true},
		{"change text", func(r *ExecutionRequest) { r.Expectation.ExpectedChange.Text = "x" }, true, true},
		{"change range", func(r *ExecutionRequest) { r.Expectation.ExpectedChange.Ranges[0].Min = &min2 }, true, true},
		{"expected output", func(r *ExecutionRequest) { r.Expectation.ExpectedOutput = "x" }, true, true},
		{"criteria", func(r *ExecutionRequest) { r.Expectation.SuccessCriteria[0].Expr = "exit_code == 1" }, true, true},
		{"reversible", func(r *ExecutionRequest) { r.Expectation.Reversible = consts.Irreversible }, true, true},
		{"rollback", func(r *ExecutionRequest) { r.Expectation.Rollback = "x" }, true, true},
		{"has preview", func(r *ExecutionRequest) { r.Expectation.HasPreview = false }, true, true},
		{"risk note", func(r *ExecutionRequest) { r.Expectation.RiskNote = "x" }, true, true},
		{"fingerprint value", func(r *ExecutionRequest) { r.Fingerprint.Value = "fp-2" }, false, true},
		{"fingerprint nil", func(r *ExecutionRequest) { r.Fingerprint = nil }, false, true},
		{"policy mode", func(r *ExecutionRequest) { r.Checks.Policy.Mode = consts.PolicyAuto }, false, true},
		{"policy version", func(r *ExecutionRequest) { r.Checks.Policy.PolicyVersion = "v2" }, false, true},
		{"policy layer", func(r *ExecutionRequest) { r.Checks.Policy.Layer = consts.LayerGlobal }, false, true},
		{"policy allow_auto", func(r *ExecutionRequest) { r.Checks.Policy.AllowAuto = !r.Checks.Policy.AllowAuto }, false, true},
		{"policy reasons changed", func(r *ExecutionRequest) { r.Checks.Policy.Reasons[0] = "other" }, false, true},
		{"policy reasons added", func(r *ExecutionRequest) { r.Checks.Policy.Reasons = append(r.Checks.Policy.Reasons, "more") }, false, true},
		{"policy at", func(r *ExecutionRequest) { r.Checks.Policy.At = t0.Add(time.Second) }, false, true},
		{"policy at zero", func(r *ExecutionRequest) { r.Checks.Policy.At = time.Time{} }, false, true},
		{"policy nil", func(r *ExecutionRequest) { r.Checks.Policy = nil }, false, true},
		{"revision", func(r *ExecutionRequest) { r.Revision = 2 }, false, true},
		// 不在冻结摘要内的字段
		{"reviews", func(r *ExecutionRequest) { r.Reviews = nil }, false, false},
		{"updated at", func(r *ExecutionRequest) { r.UpdatedAt = t0.Add(time.Hour) }, false, false},
		{"state", func(r *ExecutionRequest) { r.State = consts.RequestAwaitingReview }, false, false},
		{"version", func(r *ExecutionRequest) { r.Version = 9 }, false, false},
		{"event seq", func(r *ExecutionRequest) { r.EventSeq = 9 }, false, false},
		{"fingerprint time", func(r *ExecutionRequest) { r.Fingerprint.CollectedAt = t0.Add(time.Hour) }, false, false},
		{"checks preview", func(r *ExecutionRequest) { r.Checks.Preview = nil }, false, false},
		{"checks dcg", func(r *ExecutionRequest) { r.Checks.DCG = nil }, false, false},
		{"checks selfreview at", func(r *ExecutionRequest) { r.Checks.SelfReview.At = t0.Add(time.Hour) }, false, false},
		{"checks selfreview", func(r *ExecutionRequest) { r.Checks.SelfReview = nil }, false, false},
		{"escalations", func(r *ExecutionRequest) { r.Checks.Escalations = nil }, false, false},
		{"submission", func(r *ExecutionRequest) { r.Submission.HookRequireManual = true }, false, false},
		{"source", func(r *ExecutionRequest) { r.Source = consts.SourceAdHoc }, false, false},
		{"session", func(r *ExecutionRequest) { r.SessionID = "other" }, false, false},
		{"execution", func(r *ExecutionRequest) { r.Execution = nil }, false, false},
		{"call id", func(r *ExecutionRequest) { r.CallID = "call-2" }, false, false},
	}
	for _, c := range cases {
		r := sample()
		c.mut(r)
		gc, gf := digests(t, r)
		if (gc != baseC) != c.inContent {
			t.Errorf("%s: content digest changed = %v, want %v", c.name, gc != baseC, c.inContent)
		}
		if (gf != baseF) != c.inFreeze {
			t.Errorf("%s: freeze digest changed = %v, want %v", c.name, gf != baseF, c.inFreeze)
		}
	}
}

// 同一时刻用不同时区表示（例如数据库按会话时区返回），冻结摘要不变。
func TestFreezeDigestIgnoresTimeZone(t *testing.T) {
	_, base := digests(t, sample())
	r := sample()
	r.Checks.Policy.At = t0.In(time.FixedZone("UTC+8", 8*3600))
	if r.Checks.Policy.At.Format(time.RFC3339) == t0.Format(time.RFC3339) {
		t.Fatal("test setup: zone did not change the text form")
	}
	if _, got := digests(t, r); got != base {
		t.Fatal("freeze digest depends on the time zone of the policy time")
	}
}

func TestDigestRejectsUnsupportedParams(t *testing.T) {
	r := sample()
	r.Content.Params["bad"] = make(chan int)
	if _, err := r.ContentDigest(); err == nil {
		t.Fatal("want error")
	}
}

func TestClone(t *testing.T) {
	r := sample()
	code := 0
	r.Execution.Result.ExitCode = &code
	r.Task = &TaskRef{RunID: "r", StepID: "s"}
	r.Rejection = &Rejection{Source: consts.RejectedByHuman}
	r.Stop = &Stop{Reason: consts.StopCancelledByHuman}
	r.Frozen = &Frozen{Digest: "x"}
	c := r.Clone()
	if !reflect.DeepEqual(r, c) {
		t.Fatal("clone differs")
	}
	// 修改克隆不影响原值
	c.Content.Params["svc"] = "x"
	c.Content.Params["opts"].([]any)[1].(map[string]any)["k"] = "changed"
	c.Content.SecretKeys[0] = "Z"
	c.Content.Unit.ID = "z"
	c.Scope.Targets[0] = "z"
	c.Expectation.Targets[0] = "z"
	c.Expectation.SuccessCriteria[0].Expr = "z"
	*c.Expectation.ExpectedChange.Ranges[0].Min = 99
	c.Submission.SelfReview.Reason = "z"
	c.Submission.HookReasons[0] = "z"
	c.Fingerprint.Value = "z"
	c.Checks.Preview.Output = "z"
	c.Checks.DCG.RuleIDs[0] = "z"
	c.Checks.Policy.Reasons[0] = "z"
	c.Checks.Escalations[0] = "z"
	c.Reviews[0].Comment = "z"
	c.Execution.Result.Output["a"].([]any)[0] = 99
	*c.Execution.Result.ExitCode = 7
	c.Task.RunID = "z"
	c.Rejection.Reason = "z"
	c.Stop.Detail = "z"
	c.Frozen.Digest = "z"
	if !reflect.DeepEqual(r, sample2()) {
		t.Fatal("original mutated through clone")
	}
}

func sample2() *ExecutionRequest {
	r := sample()
	code := 0
	r.Execution.Result.ExitCode = &code
	r.Task = &TaskRef{RunID: "r", StepID: "s"}
	r.Rejection = &Rejection{Source: consts.RejectedByHuman}
	r.Stop = &Stop{Reason: consts.StopCancelledByHuman}
	r.Frozen = &Frozen{Digest: "x"}
	return r
}

func TestCloneNilAndZero(t *testing.T) {
	var r *ExecutionRequest
	if r.Clone() != nil {
		t.Fatal("nil clone")
	}
	z := &ExecutionRequest{}
	if !reflect.DeepEqual(z, z.Clone()) {
		t.Fatal("zero clone differs")
	}
}

func TestCloneValue(t *testing.T) {
	in := map[string]any{
		"a": []string{"x"}, "b": map[string]int{"k": 1}, "c": []any{[]any{1}},
		"d": &struct{ N []int }{N: []int{1}}, "e": nil, "f": 1.5, "g": []map[string]any{{"z": "y"}},
	}
	out := CloneMap(in)
	if !reflect.DeepEqual(in, out) {
		t.Fatal("not equal")
	}
	out["a"].([]string)[0] = "y"
	out["b"].(map[string]int)["k"] = 2
	out["c"].([]any)[0].([]any)[0] = 2
	out["g"].([]map[string]any)[0]["z"] = "q"
	if in["a"].([]string)[0] != "x" || in["b"].(map[string]int)["k"] != 1 || in["c"].([]any)[0].([]any)[0] != 1 ||
		in["g"].([]map[string]any)[0]["z"] != "y" {
		t.Fatal("aliasing")
	}
	if CloneMap(nil) != nil {
		t.Fatal("nil map")
	}
}
