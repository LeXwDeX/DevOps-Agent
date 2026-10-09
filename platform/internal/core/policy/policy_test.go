package policy

import (
	"reflect"
	"strings"
	"testing"

	mpol "github.com/LeXwDeX/DevOps-Agent/platform/internal/model/policy"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/consts"
)

const (
	auto   = consts.PolicyAuto
	manual = consts.PolicyManual
	forbid = consts.PolicyForbid
)

func mode(m consts.PolicyMode) *consts.PolicyMode { return &m }
func no() *bool                                   { b := false; return &b }
func yes() *bool                                  { b := true; return &b }

func action(env string, m *consts.PolicyMode, aa *bool) mpol.Rule {
	return mpol.Rule{Layer: consts.LayerAction, Subject: "ssh", Action: "restart", Environment: env, Mode: m, AllowAuto: aa}
}
func zone(z consts.Zone, env string, m *consts.PolicyMode, aa *bool) mpol.Rule {
	return mpol.Rule{Layer: consts.LayerZone, Subject: "ssh", Zone: z, Environment: env, Mode: m, AllowAuto: aa}
}
func global(env string, m *consts.PolicyMode, aa *bool) mpol.Rule {
	return mpol.Rule{Layer: consts.LayerGlobal, Subject: "ssh", Environment: env, Mode: m, AllowAuto: aa}
}
func system(m *consts.PolicyMode, aa *bool) mpol.Rule {
	return mpol.Rule{Layer: consts.LayerSystem, Mode: m, AllowAuto: aa}
}

func query() mpol.Query {
	return mpol.Query{Subject: "ssh", Action: "restart", Zone: consts.ZoneWrite, Environment: "prod",
		SessionMode: consts.SessionAuto, Source: consts.SourceRegisteredTool}
}

func TestResolve(t *testing.T) {
	type tc struct {
		name      string
		rules     []mpol.Rule
		mut       func(q *mpol.Query)
		wantMode  consts.PolicyMode
		wantLayer consts.PolicyLayer
		wantAA    bool
	}
	cases := []tc{
		// 系统兜底
		{"empty set -> system fallback manual", nil, nil, manual, consts.LayerSystem, true},
		{"system rule gives mode", []mpol.Rule{system(mode(auto), nil)}, nil, auto, consts.LayerSystem, true},
		{"system allow_auto only -> still fallback manual", []mpol.Rule{system(nil, no())}, nil, manual, consts.LayerSystem, false},
		// 四层命中顺序
		{"action beats all", []mpol.Rule{system(mode(manual), nil), global("", mode(manual), nil), zone(consts.ZoneWrite, "", mode(manual), nil), action("", mode(auto), nil)}, nil, auto, consts.LayerAction, true},
		{"zone beats global and system", []mpol.Rule{system(mode(manual), nil), global("", mode(manual), nil), zone(consts.ZoneWrite, "", mode(auto), nil)}, nil, auto, consts.LayerZone, true},
		{"global beats system", []mpol.Rule{system(mode(manual), nil), global("", mode(auto), nil)}, nil, auto, consts.LayerGlobal, true},
		{"registration order does not matter", []mpol.Rule{action("", mode(auto), nil), global("", mode(manual), nil), system(mode(manual), nil)}, nil, auto, consts.LayerAction, true},
		// 匹配键
		{"action rule for other action ignored", []mpol.Rule{{Layer: consts.LayerAction, Subject: "ssh", Action: "stop", Mode: mode(auto)}}, nil, manual, consts.LayerSystem, true},
		{"action rule for other subject ignored", []mpol.Rule{{Layer: consts.LayerAction, Subject: "k8s", Action: "restart", Mode: mode(auto)}}, nil, manual, consts.LayerSystem, true},
		{"zone rule other zone ignored", []mpol.Rule{zone(consts.ZoneRead, "", mode(auto), nil)}, nil, manual, consts.LayerSystem, true},
		{"zone rule read matches read query", []mpol.Rule{zone(consts.ZoneRead, "", mode(auto), nil)}, func(q *mpol.Query) { q.Zone = consts.ZoneRead }, auto, consts.LayerZone, true},
		{"global other subject ignored", []mpol.Rule{{Layer: consts.LayerGlobal, Subject: "k8s", Mode: mode(auto)}}, nil, manual, consts.LayerSystem, true},
		// 环境覆盖
		{"env specific beats agnostic same layer", []mpol.Rule{action("", mode(manual), nil), action("prod", mode(auto), nil)}, nil, auto, consts.LayerAction, true},
		{"env specific listed first beats agnostic", []mpol.Rule{action("prod", mode(auto), nil), action("", mode(manual), nil)}, nil, auto, consts.LayerAction, true},
		{"other env rule ignored", []mpol.Rule{action("dev", mode(auto), nil), action("", mode(manual), nil)}, nil, manual, consts.LayerAction, true},
		{"env agnostic used when no env rule", []mpol.Rule{action("", mode(auto), nil)}, nil, auto, consts.LayerAction, true},
		{"finer layer agnostic beats coarser env specific", []mpol.Rule{global("prod", mode(manual), nil), action("", mode(auto), nil)}, nil, auto, consts.LayerAction, true},
		// 只写 allow_auto 的规则不给 Mode
		{"allow_auto-only rule does not give mode", []mpol.Rule{action("", nil, yes()), global("", mode(auto), nil)}, nil, auto, consts.LayerGlobal, true},
		// allow_auto: false
		{"allow_auto false in first layer turns auto to manual", []mpol.Rule{action("", mode(auto), no())}, nil, manual, consts.LayerAction, false},
		{"allow_auto false in coarser layer turns auto to manual", []mpol.Rule{action("", mode(auto), nil), global("", nil, no())}, nil, manual, consts.LayerAction, false},
		{"allow_auto false in system layer", []mpol.Rule{action("", mode(auto), nil), system(nil, no())}, nil, manual, consts.LayerAction, false},
		{"allow_auto false env-specific", []mpol.Rule{zone(consts.ZoneWrite, "prod", nil, no()), action("", mode(auto), nil)}, nil, manual, consts.LayerAction, false},
		{"allow_auto false other env ignored", []mpol.Rule{zone(consts.ZoneWrite, "dev", nil, no()), action("", mode(auto), nil)}, nil, auto, consts.LayerAction, true},
		{"allow_auto true does not override false", []mpol.Rule{action("", mode(auto), yes()), global("", nil, no())}, nil, manual, consts.LayerAction, false},
		{"allow_auto false does not touch manual", []mpol.Rule{action("", mode(manual), no())}, nil, manual, consts.LayerAction, false},
		// forbid 优先
		{"forbid in first layer", []mpol.Rule{action("", mode(forbid), nil), global("", mode(auto), nil)}, nil, forbid, consts.LayerAction, true},
		{"forbid in coarser layer beats finer auto", []mpol.Rule{action("", mode(auto), nil), global("", mode(forbid), nil)}, nil, forbid, consts.LayerGlobal, true},
		{"forbid in system layer beats finer auto", []mpol.Rule{action("", mode(auto), nil), system(mode(forbid), nil)}, nil, forbid, consts.LayerSystem, true},
		{"forbid beats manual", []mpol.Rule{action("", mode(manual), nil), zone(consts.ZoneWrite, "", mode(forbid), nil)}, nil, forbid, consts.LayerZone, true},
		{"forbid other env ignored", []mpol.Rule{action("", mode(auto), nil), global("dev", mode(forbid), nil)}, nil, auto, consts.LayerAction, true},
		{"forbid in session manual stays forbid", []mpol.Rule{global("", mode(forbid), nil)}, func(q *mpol.Query) { q.SessionMode = consts.SessionManual }, forbid, consts.LayerGlobal, true},
		{"forbid with pre_authorized stays forbid", []mpol.Rule{global("", mode(forbid), nil)}, func(q *mpol.Query) { q.Source = consts.SourcePreAuthorized }, forbid, consts.LayerGlobal, true},
		{"forbid with allow_auto false", []mpol.Rule{global("", mode(forbid), no())}, nil, forbid, consts.LayerGlobal, false},
		// 会话模式
		{"manual session turns auto to manual", []mpol.Rule{action("", mode(auto), nil)}, func(q *mpol.Query) { q.SessionMode = consts.SessionManual }, manual, consts.LayerAction, true},
		{"manual session, pre_authorized stays auto", []mpol.Rule{action("", mode(auto), nil)}, func(q *mpol.Query) { q.SessionMode = consts.SessionManual; q.Source = consts.SourcePreAuthorized }, auto, consts.LayerAction, true},
		{"manual session, pre_authorized but allow_auto false", []mpol.Rule{action("", mode(auto), no())}, func(q *mpol.Query) { q.SessionMode = consts.SessionManual; q.Source = consts.SourcePreAuthorized }, manual, consts.LayerAction, false},
		{"manual session keeps manual", []mpol.Rule{action("", mode(manual), nil)}, func(q *mpol.Query) { q.SessionMode = consts.SessionManual }, manual, consts.LayerAction, true},
		{"empty session mode treated as manual", []mpol.Rule{action("", mode(auto), nil)}, func(q *mpol.Query) { q.SessionMode = "" }, manual, consts.LayerAction, true},
		{"unknown session mode treated as manual", []mpol.Rule{action("", mode(auto), nil)}, func(q *mpol.Query) { q.SessionMode = "bogus" }, manual, consts.LayerAction, true},
		{"auto session keeps auto", []mpol.Rule{action("", mode(auto), nil)}, nil, auto, consts.LayerAction, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			set := mpol.Set{Version: "pv-1", Rules: c.rules}
			q := query()
			if c.mut != nil {
				c.mut(&q)
			}
			d := Resolve(set, q)
			if d.Mode != c.wantMode || d.Layer != c.wantLayer || d.AllowAuto != c.wantAA {
				t.Errorf("got mode=%s layer=%s allowAuto=%v; want %s %s %v (reasons %v)",
					d.Mode, d.Layer, d.AllowAuto, c.wantMode, c.wantLayer, c.wantAA, d.Reasons)
			}
			if d.PolicyVersion != "pv-1" {
				t.Errorf("policy version = %q", d.PolicyVersion)
			}
			if len(d.Reasons) == 0 {
				t.Error("no reasons")
			}
			// 纯函数：同样输入同样输出
			if d2 := Resolve(set, q); !reflect.DeepEqual(d, d2) {
				t.Error("not deterministic")
			}
		})
	}
}

func TestResolveDoesNotMutateSet(t *testing.T) {
	set := mpol.Set{Rules: []mpol.Rule{action("", mode(auto), nil), action("prod", mode(manual), nil)}}
	before := append([]mpol.Rule(nil), set.Rules...)
	Resolve(set, query())
	if !reflect.DeepEqual(set.Rules, before) {
		t.Error("rules reordered or changed")
	}
}

func TestResolveReasonsChinese(t *testing.T) {
	d := Resolve(mpol.Set{Rules: []mpol.Rule{action("", mode(auto), no())}}, query())
	joined := strings.Join(d.Reasons, "|")
	for _, want := range []string{"策略命中", "不允许自动", "人工"} {
		if !strings.Contains(joined, want) {
			t.Errorf("reasons %q lack %q", joined, want)
		}
	}
}

func TestZoneOf(t *testing.T) {
	for _, c := range consts.Categories() {
		want := consts.ZoneWrite
		if c == consts.CategoryReadOnly {
			want = consts.ZoneRead
		}
		if got := ZoneOf(c); got != want {
			t.Errorf("ZoneOf(%s) = %s", c, got)
		}
	}
	if ZoneOf("") != consts.ZoneWrite || ZoneOf("bogus") != consts.ZoneWrite {
		t.Error("unknown category must map to write")
	}
}

// cleanRequest 返回一个所有转人工条件都不成立的申请。
func cleanRequest() *request.ExecutionRequest {
	return &request.ExecutionRequest{
		Source:      consts.SourceRegisteredTool,
		Fingerprint: &request.Fingerprint{Status: consts.FingerprintCollected, Value: "fp"},
		Checks: request.Checks{
			Preview:    &request.PreviewResult{Status: consts.PreviewPassed},
			SelfReview: &request.SelfReview{Verdict: consts.VerdictNoRisk},
			DCG:        &request.DCGCheck{Result: consts.DCGPass},
		},
		Expectation: request.Expectation{Category: consts.CategoryWrite, Reversible: consts.Reversible},
	}
}

func TestFlagsFrom(t *testing.T) {
	if f := FlagsFrom(cleanRequest()); f != (Flags{}) {
		t.Fatalf("clean request has flags: %+v", f)
	}
	cases := []struct {
		name string
		mut  func(r *request.ExecutionRequest)
		want Flags
	}{
		{"self review risk", func(r *request.ExecutionRequest) { r.Checks.SelfReview.Verdict = consts.VerdictRisk }, Flags{SelfReviewRiskOrMissing: true}},
		{"self review missing", func(r *request.ExecutionRequest) { r.Checks.SelfReview = nil }, Flags{SelfReviewRiskOrMissing: true}},
		{"self review unknown verdict", func(r *request.ExecutionRequest) { r.Checks.SelfReview.Verdict = "bogus" }, Flags{SelfReviewRiskOrMissing: true}},
		{"not_applicable ok for task_step", func(r *request.ExecutionRequest) {
			r.Source = consts.SourceTaskStep
			r.Checks.SelfReview.Verdict = consts.VerdictNotApplicable
		}, Flags{}},
		{"not_applicable missing for task_step when nil", func(r *request.ExecutionRequest) {
			r.Source = consts.SourceTaskStep
			r.Checks.SelfReview = nil
		}, Flags{SelfReviewRiskOrMissing: true}},
		{"not_applicable rejected for other source", func(r *request.ExecutionRequest) { r.Checks.SelfReview.Verdict = consts.VerdictNotApplicable }, Flags{SelfReviewRiskOrMissing: true}},
		{"preview failed", func(r *request.ExecutionRequest) { r.Checks.Preview.Status = consts.PreviewFailed }, Flags{PreviewFailed: true}},
		{"preview none is fine", func(r *request.ExecutionRequest) { r.Checks.Preview.Status = consts.PreviewNone }, Flags{}},
		{"preview nil is fine", func(r *request.ExecutionRequest) { r.Checks.Preview = nil }, Flags{}},
		{"dcg hit", func(r *request.ExecutionRequest) { r.Checks.DCG.Result = consts.DCGHit }, Flags{DCGHitOrUnavailable: true}},
		{"dcg unavailable", func(r *request.ExecutionRequest) { r.Checks.DCG.Result = consts.DCGUnavailable }, Flags{DCGHitOrUnavailable: true}},
		{"dcg nil", func(r *request.ExecutionRequest) { r.Checks.DCG = nil }, Flags{DCGHitOrUnavailable: true}},
		{"dcg forbid", func(r *request.ExecutionRequest) { r.Checks.DCG.Result = consts.DCGForbid }, Flags{DCGHitOrUnavailable: true}},
		{"fingerprint nil", func(r *request.ExecutionRequest) { r.Fingerprint = nil }, Flags{FingerprintUnavailable: true}},
		{"fingerprint unavailable", func(r *request.ExecutionRequest) { r.Fingerprint.Status = consts.FingerprintUnavailable }, Flags{FingerprintUnavailable: true}},
		{"category unknown", func(r *request.ExecutionRequest) { r.Expectation.Category = consts.CategoryUnknown }, Flags{CategoryUnknown: true}},
		{"category empty", func(r *request.ExecutionRequest) { r.Expectation.Category = "" }, Flags{CategoryUnknown: true}},
		{"irreversible, preview none", func(r *request.ExecutionRequest) {
			r.Expectation.Reversible = consts.Irreversible
			r.Checks.Preview.Status = consts.PreviewNone
		}, Flags{IrreversibleWithoutPreview: true}},
		{"irreversible, preview nil", func(r *request.ExecutionRequest) {
			r.Expectation.Reversible = consts.Irreversible
			r.Checks.Preview = nil
		}, Flags{IrreversibleWithoutPreview: true}},
		{"irreversible, preview failed", func(r *request.ExecutionRequest) {
			r.Expectation.Reversible = consts.Irreversible
			r.Checks.Preview.Status = consts.PreviewFailed
		}, Flags{IrreversibleWithoutPreview: true, PreviewFailed: true}},
		{"irreversible, preview status unknown value", func(r *request.ExecutionRequest) {
			r.Expectation.Reversible = consts.Irreversible
			r.Checks.Preview.Status = "bogus"
		}, Flags{IrreversibleWithoutPreview: true}},
		{"reversibility unknown, no preview result", func(r *request.ExecutionRequest) {
			r.Expectation.Reversible = consts.ReversibilityUnknown
			r.Checks.Preview = nil
		}, Flags{IrreversibleWithoutPreview: true}},
		{"reversibility empty, no preview result", func(r *request.ExecutionRequest) {
			r.Expectation.Reversible = ""
			r.Checks.Preview = nil
		}, Flags{IrreversibleWithoutPreview: true}},
		// 单元声明了预演，但预演结果缺失：不能算"有预演"。
		{"irreversible, preview declared but not run", func(r *request.ExecutionRequest) {
			r.Expectation.Reversible = consts.Irreversible
			r.Expectation.HasPreview = true
			r.Checks.Preview = nil
		}, Flags{IrreversibleWithoutPreview: true}},
		{"irreversible, preview passed", func(r *request.ExecutionRequest) { r.Expectation.Reversible = consts.Irreversible }, Flags{}},
		{"unknown reversibility, preview passed", func(r *request.ExecutionRequest) { r.Expectation.Reversible = consts.ReversibilityUnknown }, Flags{}},
		{"reversible, no preview result is fine", func(r *request.ExecutionRequest) { r.Checks.Preview = nil }, Flags{}},
		{"hook requires manual", func(r *request.ExecutionRequest) { r.Submission.HookRequireManual = true }, Flags{HookRequiresManual: true}},
		{"analysis failed", func(r *request.ExecutionRequest) { r.Submission.AnalysisFailed = true }, Flags{AnalysisFailed: true}},
		{"ad hoc", func(r *request.ExecutionRequest) { r.Source = consts.SourceAdHoc }, Flags{AdHocCommand: true}},
		{"human terminal not flagged", func(r *request.ExecutionRequest) { r.Source = consts.SourceHumanTerminal }, Flags{}},
		{"combined", func(r *request.ExecutionRequest) {
			r.Source = consts.SourceAdHoc
			r.Checks.DCG = nil
			r.Fingerprint = nil
		}, Flags{AdHocCommand: true, DCGHitOrUnavailable: true, FingerprintUnavailable: true}},
	}
	for _, c := range cases {
		r := cleanRequest()
		c.mut(r)
		if got := FlagsFrom(r); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestEscalate(t *testing.T) {
	single := []Flags{
		{SelfReviewRiskOrMissing: true}, {PreviewFailed: true}, {DCGHitOrUnavailable: true},
		{FingerprintUnavailable: true}, {CategoryUnknown: true}, {IrreversibleWithoutPreview: true},
		{HookRequiresManual: true}, {AnalysisFailed: true}, {AdHocCommand: true},
	}
	autoDecision := mpol.Decision{Mode: auto, Layer: consts.LayerAction, AllowAuto: true, PolicyVersion: "v", Reasons: []string{"x"}}

	for i, f := range single {
		got := Escalate(autoDecision, f)
		if got.Mode != manual || len(got.Reasons) != 2 || got.Layer != consts.LayerAction || got.PolicyVersion != "v" || !got.AllowAuto {
			t.Errorf("flag #%d (%+v): %+v", i, f, got)
		}
		if !f.Any() || len(f.Reasons()) != 1 {
			t.Errorf("flag #%d Any/Reasons", i)
		}
		// forbid 永远不被降级
		if g := Escalate(mpol.Decision{Mode: forbid}, f); g.Mode != forbid || len(g.Reasons) != 0 {
			t.Errorf("flag #%d downgraded forbid: %+v", i, g)
		}
		// manual 保持 manual
		if g := Escalate(mpol.Decision{Mode: manual}, f); g.Mode != manual {
			t.Errorf("flag #%d: manual changed", i)
		}
	}

	// 无条件：原样返回
	if g := Escalate(autoDecision, Flags{}); g.Mode != auto || !reflect.DeepEqual(g.Reasons, []string{"x"}) {
		t.Errorf("no flags: %+v", g)
	}
	// 组合：每个条件一条理由
	all := Flags{true, true, true, true, true, true, true, true, true}
	g := Escalate(autoDecision, all)
	if g.Mode != manual || len(g.Reasons) != 1+9 || len(all.Reasons()) != 9 {
		t.Errorf("combined: %+v", g)
	}
	// 不改入参
	if autoDecision.Mode != auto || len(autoDecision.Reasons) != 1 {
		t.Error("input mutated")
	}
	g.Reasons[0] = "changed"
	if autoDecision.Reasons[0] != "x" {
		t.Error("reasons aliased")
	}
}

// 61 第 9 节：独立命令、不可逆、策略。端到端串起 Resolve + FlagsFrom + Escalate。
func TestEndToEndRules(t *testing.T) {
	autoSet := mpol.Set{Version: "v", Rules: []mpol.Rule{global("", mode(auto), nil)}}
	decide := func(set mpol.Set, r *request.ExecutionRequest, sm consts.SessionMode) mpol.Decision {
		q := mpol.Query{Subject: "ssh", Action: "restart", Zone: ZoneOf(r.Expectation.Category),
			Environment: "prod", SessionMode: sm, Source: r.Source}
		return Escalate(Resolve(set, q), FlagsFrom(r))
	}

	if d := decide(autoSet, cleanRequest(), consts.SessionAuto); d.Mode != auto {
		t.Fatalf("clean request under auto policy = %s (%v)", d.Mode, d.Reasons)
	}

	// 独立命令：任何策略和会话模式下都是人工
	for _, sm := range consts.SessionModes() {
		for _, m := range []consts.PolicyMode{auto, manual} {
			set := mpol.Set{Rules: []mpol.Rule{system(mode(m), nil), global("", mode(m), nil), action("", mode(m), nil)}}
			r := cleanRequest()
			r.Source = consts.SourceAdHoc
			if d := decide(set, r, sm); d.Mode != manual {
				t.Errorf("ad hoc, policy %s, session %s -> %s", m, sm, d.Mode)
			}
		}
	}
	// 独立命令遇到 forbid 仍是 forbid
	r := cleanRequest()
	r.Source = consts.SourceAdHoc
	if d := decide(mpol.Set{Rules: []mpol.Rule{global("", mode(forbid), nil)}}, r, consts.SessionAuto); d.Mode != forbid {
		t.Errorf("ad hoc forbid -> %s", d.Mode)
	}

	// 不可逆且预演没有通过：即使策略为 auto 也转人工；预演通过则按策略
	r = cleanRequest()
	r.Expectation.Reversible = consts.Irreversible
	r.Checks.Preview = &request.PreviewResult{Status: consts.PreviewNone}
	if d := decide(autoSet, r, consts.SessionAuto); d.Mode != manual {
		t.Errorf("irreversible w/o preview -> %s", d.Mode)
	}
	r.Expectation.HasPreview = true
	r.Checks.Preview = nil // 声明了预演但没有结果
	if d := decide(autoSet, r, consts.SessionAuto); d.Mode != manual {
		t.Errorf("irreversible, preview declared but no result -> %s", d.Mode)
	}
	r.Checks.Preview = &request.PreviewResult{Status: consts.PreviewPassed}
	if d := decide(autoSet, r, consts.SessionAuto); d.Mode != auto {
		t.Errorf("irreversible with passed preview -> %s", d.Mode)
	}
	if d := decide(mpol.Set{Rules: []mpol.Rule{global("", mode(manual), nil)}}, r, consts.SessionAuto); d.Mode != manual {
		t.Errorf("irreversible with preview, manual policy -> %s", d.Mode)
	}
	// 未知可逆性按不可逆处理
	r.Expectation.Reversible, r.Checks.Preview = consts.ReversibilityUnknown, nil
	if d := decide(autoSet, r, consts.SessionAuto); d.Mode != manual {
		t.Errorf("unknown reversibility w/o preview -> %s", d.Mode)
	}
}

// 规则里的 Mode 不合法：按 manual 处理，不会落到放行，也不会被当成 forbid。
func TestResolveInvalidModeIsManual(t *testing.T) {
	for _, bad := range []consts.PolicyMode{"", "bogus", "AUTO"} {
		m := bad
		d := Resolve(mpol.Set{Version: "v", Rules: []mpol.Rule{global("", &m, nil)}}, query())
		if d.Mode != consts.PolicyManual || d.Layer != consts.LayerGlobal {
			t.Errorf("mode %q -> %s at %s", bad, d.Mode, d.Layer)
		}
	}
	// 细层的不合法模式也挡住更粗层的 auto。
	bad := consts.PolicyMode("bogus")
	d := Resolve(mpol.Set{Rules: []mpol.Rule{action("", &bad, nil), global("", mode(consts.PolicyAuto), nil)}}, query())
	if d.Mode != consts.PolicyManual || d.Layer != consts.LayerAction {
		t.Errorf("fine layer invalid mode -> %s at %s", d.Mode, d.Layer)
	}
}

// ResolveValidated：策略集合法时等同 Resolve；不合法时丢掉坏规则、不允许自动，合法的 forbid 仍然有效。
func TestResolveValidated(t *testing.T) {
	good := mpol.Set{Version: "v", Rules: []mpol.Rule{global("", mode(consts.PolicyAuto), nil)}}
	if got, want := ResolveValidated(good, query()), Resolve(good, query()); !reflect.DeepEqual(got, want) {
		t.Errorf("valid set: %+v != %+v", got, want)
	}

	broken := mpol.Rule{Layer: consts.LayerZone, Subject: "ssh", Mode: mode(consts.PolicyAuto)} // 缺 zone
	badMode := consts.PolicyMode("bogus")
	invalidMode := global("", &badMode, nil)

	d := ResolveValidated(mpol.Set{Version: "v", Rules: []mpol.Rule{global("", mode(consts.PolicyAuto), nil), broken}}, query())
	if d.Mode != consts.PolicyManual || d.Layer != consts.LayerSystem || d.AllowAuto || d.PolicyVersion != "v" ||
		!strings.Contains(strings.Join(d.Reasons, "|"), "策略集不合法") {
		t.Errorf("auto + broken rule -> %+v", d)
	}
	d = ResolveValidated(mpol.Set{Rules: []mpol.Rule{invalidMode}}, query())
	if d.Mode != consts.PolicyManual || d.AllowAuto {
		t.Errorf("invalid mode rule -> %+v", d)
	}
	d = ResolveValidated(mpol.Set{Rules: []mpol.Rule{global("", mode(consts.PolicyForbid), nil), broken}}, query())
	if d.Mode != consts.PolicyForbid || d.Layer != consts.LayerGlobal {
		t.Errorf("forbid + broken rule -> %+v", d)
	}
}
