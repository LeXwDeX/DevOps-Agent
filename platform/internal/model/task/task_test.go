package task

import (
	"reflect"
	"strings"
	"testing"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/ids"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/consts"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

func step(id, next string, branches ...Branch) Step {
	return Step{ID: id, Unit: request.UnitRef{ID: "u-" + id, Version: 1}, Next: next, Branches: branches}
}

func goodDef() *Definition {
	return &Definition{
		ID: "deploy", Version: 3, Digest: ids.Digest(strings.Repeat("a", 64)),
		Params:     []FieldSpec{{Name: "svc", Type: FieldType{Kind: KindString}, Required: true}, {Name: "hosts", Type: FieldType{Kind: KindList, Elem: KindString}}},
		SecretKeys: []string{"TOKEN"},
		Entry:      "A",
		Steps: []Step{
			step("A", "B"),
			step("B", "", Branch{When: "B.exit_code == 0", To: "C"}, Branch{When: "true", To: "D"}),
			{ID: "C", Unit: request.UnitRef{ID: "uc"}, Outputs: []FieldSpec{{Name: "rows", Type: FieldType{Kind: KindNumber}}}},
			step("D", ""),
		},
	}
}

func TestDefinitionValidate(t *testing.T) {
	if err := goodDef().Validate(); err != nil {
		t.Fatalf("good def rejected: %v", err)
	}
	bad := map[string]func(d *Definition){
		"no id":          func(d *Definition) { d.ID = "" },
		"no steps":       func(d *Definition) { d.Steps = nil; d.Entry = "" },
		"entry missing":  func(d *Definition) { d.Entry = "Z" },
		"entry empty":    func(d *Definition) { d.Entry = "" },
		"dup step":       func(d *Definition) { d.Steps = append(d.Steps, step("A", "")) },
		"empty step id":  func(d *Definition) { d.Steps = append(d.Steps, step("", "")) },
		"next missing":   func(d *Definition) { d.Steps[0].Next = "Z" },
		"branch missing": func(d *Definition) { d.Steps[1].Branches[0].To = "Z" },
		"next and br":    func(d *Definition) { d.Steps[1].Next = "C" },
		"empty when":     func(d *Definition) { d.Steps[1].Branches[0].When = " " },
		"no unit":        func(d *Definition) { d.Steps[3].Unit.ID = "" },
		"self loop":      func(d *Definition) { d.Steps[3].Next = "D" },
		"cycle":          func(d *Definition) { d.Steps[2].Next = "A" },
		"cycle via br":   func(d *Definition) { d.Steps[3].Branches = []Branch{{When: "x", To: "B"}} },
		"bad kind":       func(d *Definition) { d.Params[0].Type.Kind = "map" },
		"list no elem":   func(d *Definition) { d.Params[1].Type.Elem = "" },
		"list bad elem":  func(d *Definition) { d.Params[1].Type.Elem = KindList },
		"scalar w elem":  func(d *Definition) { d.Params[0].Type.Elem = KindString },
		"dup param":      func(d *Definition) { d.Params[1].Name = "svc" },
		"empty param":    func(d *Definition) { d.Params[1].Name = "" },
		"bad output":     func(d *Definition) { d.Steps[2].Outputs[0].Type.Kind = "x" },
		"dup secret":     func(d *Definition) { d.SecretKeys = []string{"A", "A"} },
		"empty secret":   func(d *Definition) { d.SecretKeys = []string{""} },
		"version zero":   func(d *Definition) { d.Version = 0 },
		"version neg":    func(d *Definition) { d.Version = -2 },
		"no digest":      func(d *Definition) { d.Digest = "" },
		"short digest":   func(d *Definition) { d.Digest = "abc" },
		"upper digest":   func(d *Definition) { d.Digest = ids.Digest(strings.Repeat("A", 64)) },
		"dotted step":    func(d *Definition) { d.Steps[3].ID = "D.x"; d.Steps[1].Branches[1].To = "D.x" },
		"dotted param":   func(d *Definition) { d.Params[0].Name = "a.b" },
		"dotted output":  func(d *Definition) { d.Steps[2].Outputs[0].Name = "b.c" },
		"digit start":    func(d *Definition) { d.Params[0].Name = "1svc" },
		"dash in param":  func(d *Definition) { d.Params[0].Name = "my-svc" },
		"space in step":  func(d *Definition) { d.Steps[3].ID = "D 1"; d.Steps[1].Branches[1].To = "D 1" },
		"non-ascii name": func(d *Definition) { d.Params[0].Name = "服务" },
	}
	for name, f := range bad {
		d := goodDef()
		f(d)
		err := d.Validate()
		if code, _ := errcode.CodeOf(err); code != errcode.TaskInvalid {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	// 合法的菱形图（汇合不是环）
	d := goodDef()
	d.Steps[2].Next = "D"
	if err := d.Validate(); err != nil {
		t.Errorf("diamond: %v", err)
	}
	// 列出全部问题
	d = goodDef()
	d.ID = ""
	d.Entry = "Z"
	err := d.Validate()
	if !strings.Contains(err.Error(), "task id") || !strings.Contains(err.Error(), "entry") {
		t.Errorf("want all problems: %v", err)
	}
}

func TestValidIdent(t *testing.T) {
	for _, ok := range []string{"a", "_", "A1", "_x_9", "deploy_step"} {
		if !ValidIdent(ok) {
			t.Errorf("%q rejected", ok)
		}
	}
	for _, bad := range []string{"", "1a", "a.b", "a-b", "a b", "a\n", "é", ".a"} {
		if ValidIdent(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestDefinitionStepAndClone(t *testing.T) {
	d := goodDef()
	if s, ok := d.Step("B"); !ok || len(s.Branches) != 2 {
		t.Fatal("Step lookup")
	}
	if _, ok := d.Step("Z"); ok {
		t.Fatal("found missing step")
	}
	c := d.Clone()
	if !reflect.DeepEqual(d, c) {
		t.Fatal("clone differs")
	}
	c.Steps[1].Branches[0].To = "X"
	c.Steps[2].Outputs[0].Name = "x"
	c.Params[0].Name = "x"
	c.SecretKeys[0] = "x"
	if !reflect.DeepEqual(d, goodDef()) {
		t.Fatal("aliasing")
	}
	var nd *Definition
	if nd.Clone() != nil {
		t.Fatal("nil clone")
	}
}

func TestRunClone(t *testing.T) {
	r := &Run{
		RunID: "r1", TaskID: "t", Scope: request.Scope{Targets: []string{"a"}},
		Params: map[string]any{"l": []any{"x"}}, State: consts.RunRunning,
		Steps:         []StepRecord{{StepID: "A", Output: map[string]any{"n": []any{1}}}},
		MissingParams: []string{"k"}, MissingKeys: []string{"K1"}, Stop: &RunStop{Reason: consts.StopFailed},
	}
	c := r.Clone()
	if !reflect.DeepEqual(r, c) {
		t.Fatal("clone differs")
	}
	c.Scope.Targets[0] = "z"
	c.Params["l"].([]any)[0] = "z"
	c.Steps[0].Output["n"].([]any)[0] = 9
	c.MissingParams[0] = "z"
	c.MissingKeys[0] = "Z"
	c.Stop.Detail = "z"
	if r.Scope.Targets[0] != "a" || r.Params["l"].([]any)[0] != "x" || r.Steps[0].Output["n"].([]any)[0] != 1 ||
		r.MissingParams[0] != "k" || r.MissingKeys[0] != "K1" || r.Stop.Detail != "" {
		t.Fatal("aliasing")
	}
	var nr *Run
	if nr.Clone() != nil {
		t.Fatal("nil clone")
	}
}
