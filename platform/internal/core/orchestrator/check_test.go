package orchestrator

import (
	"errors"
	"strings"
	"testing"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/task"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

func TestCheckDefinitionOK(t *testing.T) {
	if err := CheckDefinition(diamondDef(1)); err != nil {
		t.Fatal(err)
	}
	progs, err := CompileBranches(diamondDef(1))
	if err != nil {
		t.Fatal(err)
	}
	if len(progs["C"]) != 1 || len(progs["D"]) != 2 || progs["A"] != nil {
		t.Fatalf("progs = %v", progs)
	}
}

func TestCheckDefinitionRejects(t *testing.T) {
	cases := []struct {
		name   string
		mut    func(d *task.Definition)
		inMsg  string // 消息里应能看到的表达式错误码或原因
		isExpr errcode.Code
	}{
		{"syntax", func(d *task.Definition) { d.Steps[3].Branches[0].When = `D.status ==` }, "core.expr_syntax", errcode.ExprSyntax},
		{"type mismatch", func(d *task.Definition) { d.Steps[3].Branches[0].When = `D.exit_code == "x"` }, "core.expr_type", errcode.ExprType},
		{"not bool", func(d *task.Definition) { d.Steps[3].Branches[0].When = `D.status` }, "core.expr_type", errcode.ExprType},
		{"unknown path", func(d *task.Definition) { d.Steps[3].Branches[0].When = `Z.q == 1` }, "unknown path", errcode.ExprType},
		{"undeclared output", func(d *task.Definition) { d.Steps[3].Branches[0].When = `D.nope == 1` }, "unknown path", errcode.ExprType},
		{"unknown param", func(d *task.Definition) { d.Steps[2].Branches[0].When = `params.zzz == "a"` }, "unknown path", errcode.ExprType},
		{"cycle", func(d *task.Definition) { d.Steps[6].Next = "A" }, "cycle", ""},
		{"missing target", func(d *task.Definition) { d.Steps[1].Next = "nope" }, "does not exist", ""},
		{"reserved step id", func(d *task.Definition) { d.Steps[9].ID = "params"; d.Steps[8].Next = "params" }, "reserved", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := diamondDef(1)
			c.mut(d)
			err := CheckDefinition(d)
			mustCode(t, err, errcode.TaskInvalid)
			if !strings.Contains(err.Error(), c.inMsg) {
				t.Errorf("message %q lacks %q", err, c.inMsg)
			}
			if c.isExpr != "" && !errors.Is(err, errcode.New(c.isExpr, "")) {
				t.Errorf("expr code %s not reachable through the error chain: %v", c.isExpr, err)
			}
		})
	}
	mustCode(t, CheckDefinition(nil), errcode.TaskInvalid)
}

func TestConditionSchemaTypes(t *testing.T) {
	d := diamondDef(1)
	s := ConditionSchema(d)
	for path, want := range map[string]string{
		"params.svc": "string", "params.hosts": "list of string", "D.status": "string", "D.exit_code": "number", "A.n": "number",
	} {
		if got := s[path].String(); got != want {
			t.Errorf("%s = %s, want %s", path, got, want)
		}
	}
	// 列表参数可以用于 in
	d.Steps[2].Branches[0].When = `"a" in params.hosts`
	if err := CheckDefinition(d); err != nil {
		t.Fatal(err)
	}
}
