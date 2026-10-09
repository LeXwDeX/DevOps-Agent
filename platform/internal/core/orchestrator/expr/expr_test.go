package expr

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

func wantCode(t *testing.T, err error, code errcode.Code) {
	t.Helper()
	got, ok := errcode.CodeOf(err)
	if !ok || got != code {
		t.Fatalf("error = %v, want code %s", err, code)
	}
}

var testSchema = Schema{
	"D.exit_code":  TNumber,
	"D.status":     TString,
	"D.ok":         TBool,
	"D.tags":       ListOf(TString),
	"D.codes":      ListOf(TNumber),
	"exit_code":    TNumber,
	"rows":         TNumber,
	"params.env":   TString,
	"params.dry":   TBool,
	"params.names": ListOf(TString),
}

func TestParseOK(t *testing.T) {
	for _, src := range []string{
		`D.exit_code == 0`,
		`D.exit_code == 0 && D.status == "ok"`,
		`!(a || b) && c`,
		`x in ["a", "b", 1]`,
		`x in []`,
		`x >= -1.5`,
		`  ( ( x ) )  `,
		`"a\né😀\/" == s`,
		`true`,
		`a.b.c_d1 != false`,
		"x\n==\t1",
	} {
		if _, err := Parse(src); err != nil {
			t.Errorf("Parse(%q): %v", src, err)
		}
	}
}

func TestParseSyntaxErrors(t *testing.T) {
	for _, src := range []string{
		``, `   `, `a ==`, `== a`, `a = b`, `a & b`, `a | b`, `(a`, `a)`, `a b`,
		`a == b == c`, `a < b < c`, `f(x)`, `a.`, `.a`, `a..b`, `a.1`,
		`"abc`, `"a\qb"`, `"a\u12"`, `"\ud800"`, `"\ude00"`, "\"a\nb\"",
		`1.`, `1.5.2`, `1e5`, `12abc`, `-`, `- 1`, `a - 1`, `a + 1`,
		`x in [1,]`, `x in [,1]`, `x in [a]`, `x in [[1]]`, `x in [1 2]`, `x in [1`,
		`null`, `#`, "a\xffb", `a ? b : c`, `a == 1 ;`,
	} {
		_, err := Parse(src)
		if err == nil {
			t.Errorf("Parse(%q) should fail", src)
			continue
		}
		wantCode(t, err, errcode.ExprSyntax)
		if !strings.Contains(err.Error(), "offset ") {
			t.Errorf("no position in %v", err)
		}
	}
}

func TestSyntaxErrorPosition(t *testing.T) {
	_, err := Parse(`a == 1 && # b`)
	if err == nil || !strings.Contains(err.Error(), "offset 10") {
		t.Fatalf("err = %v", err)
	}
}

func TestLimits(t *testing.T) {
	ok := func(src string) {
		t.Helper()
		if _, err := Parse(src); err != nil {
			t.Errorf("Parse(len=%d) unexpected: %v", len(src), err)
		}
	}
	bad := func(src string) {
		t.Helper()
		_, err := Parse(src)
		wantCode(t, err, errcode.ExprLimit)
	}
	// 长度：恰好 4096 字节通过，4097 超限
	pad := "a" + strings.Repeat(" ", MaxSourceBytes-1)
	ok(pad)
	bad(pad + " ")
	// 嵌套
	ok(strings.Repeat("(", MaxDepth) + "x" + strings.Repeat(")", MaxDepth))
	bad(strings.Repeat("(", MaxDepth+1) + "x" + strings.Repeat(")", MaxDepth+1))
	ok(strings.Repeat("!", MaxDepth) + "x")
	bad(strings.Repeat("!", MaxDepth+1) + "x")
	bad(strings.Repeat("!(", 17) + "x" + strings.Repeat(")", 17)) // 34 层
	// 超深输入不能栈溢出
	bad(strings.Repeat("(", 4000))
	// 列表
	list := func(n int) string {
		return "x in [" + strings.TrimSuffix(strings.Repeat("1,", n), ",") + "]"
	}
	ok(list(MaxListLen))
	bad(list(MaxListLen + 1))
	// 长链不算嵌套
	ok(strings.Repeat("a || ", 500) + "a")
}

func TestPrecedence(t *testing.T) {
	cases := []struct {
		src  string
		want bool
	}{
		{`true || false && false`, true},
		{`(true || false) && false`, false},
		{`!false && false`, false},
		{`!true == false`, true}, // (!true) == false
		{`1 < 2 && 2 < 3`, true},
		{`"a" < "b"`, true},
		{`"b" <= "a"`, false},
		{`1 in [1, 2]`, true},
		{`3 in [1, 2]`, false},
		{`"x" in ["x"]`, true},
		{`true in [false]`, false},
		{`1 in []`, false},
		{`true != false`, true},
		{`1.0 == 1`, true},
		{`-2 < -1`, true},
	}
	for _, c := range cases {
		p, err := Compile(c.src, nil)
		if err != nil {
			t.Errorf("Compile(%q): %v", c.src, err)
			continue
		}
		got, err := p.Eval(nil)
		if err != nil || got != c.want {
			t.Errorf("Eval(%q) = %v, %v; want %v", c.src, got, err, c.want)
		}
	}
}

func TestCheckErrors(t *testing.T) {
	for _, src := range []string{
		`D.nope == 1`,                 // 未知路径
		`D`,                           // 只有前缀的路径
		`D.exit_code == "0"`,          // 类型不符
		`D.status == 1`,               //
		`D.exit_code`,                 // 结果非 bool
		`1`,                           //
		`"a"`,                         //
		`D.ok == 1`,                   //
		`D.ok < true`,                 // bool 不可排序
		`D.status < 1`,                //
		`D.tags == D.tags`,            // 列表不可比较
		`["a"] == D.status`,           // 列表字面量不在 in 右侧
		`["a"]`,                       //
		`D.status in "abc"`,           // 右侧不是列表
		`D.status in D.exit_code`,     //
		`D.status in [1, 2]`,          // 元素类型不符
		`D.exit_code in D.tags`,       //
		`D.tags in D.tags`,            // 左侧是列表
		`["a"] in ["a"]`,              //
		`D.status in ["a", 1]`,        // 混合类型
		`!D.exit_code`,                // 非 bool 取反
		`D.ok && D.exit_code`,         //
		`D.ok || 1`,                   //
		`(D.exit_code == 0) == 1`,     //
		`D.exit_code in D.codes && 1`, //
	} {
		p, err := Compile(src, testSchema)
		if err == nil {
			t.Errorf("Compile(%q) should fail, got %v", src, p)
			continue
		}
		if c, _ := errcode.CodeOf(err); c != errcode.ExprType && c != errcode.ExprSyntax {
			t.Errorf("Compile(%q) code = %v", src, err)
		}
	}
	// 明确要求 expr_type 的几个
	for _, src := range []string{`D.nope == 1`, `D.exit_code`, `D.status == 1`, `["a"] == D.status`, `D.status in "abc"`} {
		_, err := Compile(src, testSchema)
		wantCode(t, err, errcode.ExprType)
	}
	// Check 单独可用
	e, _ := Parse(`D.exit_code == 0`)
	if err := Check(e, testSchema); err != nil {
		t.Error(err)
	}
	wantCode(t, Check(e, Schema{}), errcode.ExprType)
	wantCode(t, Check(e, Schema{"D.exit_code": Type{}}), errcode.ExprType) // 无效 schema 类型
	wantCode(t, Check(e, Schema{"D.exit_code": ListOf(ListOf(TString))}), errcode.ExprType)
}

func TestCheckOK(t *testing.T) {
	for _, src := range []string{
		`D.exit_code == 0 && D.status == "ok"`,
		`D.status in ["ok", "done"]`,
		`D.status in params.names`,
		`D.exit_code in D.codes`,
		`D.ok`,
		`!D.ok || params.dry == true`,
		`params.env != "prod" || D.exit_code <= 120`,
		`D.exit_code in []`,
		`true`,
	} {
		if _, err := Compile(src, testSchema); err != nil {
			t.Errorf("Compile(%q): %v", src, err)
		}
	}
}

func TestEval(t *testing.T) {
	env := MapEnv{
		"D": map[string]any{
			"exit_code": 0, "status": "ok", "ok": true,
			"tags":  []any{"a", "b"},
			"codes": []any{json.Number("1"), int64(2), 3.0},
		},
		"exit_code": json.Number("3"),
		"rows":      uint8(100),
		"params":    map[string]any{"env": "stg", "dry": false, "names": []string{"x", "ok"}},
	}
	cases := []struct {
		src  string
		want bool
	}{
		{`D.exit_code == 0 && D.status == "ok"`, true},
		{`D.exit_code != 0 || D.status == "bad"`, false},
		{`exit_code > 2 && exit_code <= 3`, true},
		{`rows <= 120`, true},
		{`rows < 100`, false},
		{`D.status in params.names`, true},
		{`D.status in ["x"]`, false},
		{`3 in D.codes`, true},
		{`4 in D.codes`, false},
		{`!D.ok`, false},
		{`D.ok == true`, true},
		{`params.dry`, false},
		{`params.env == "stg"`, true},
	}
	for _, c := range cases {
		p, err := Compile(c.src, testSchema)
		if err != nil {
			t.Errorf("Compile(%q): %v", c.src, err)
			continue
		}
		got, err := p.Eval(env)
		if err != nil || got != c.want {
			t.Errorf("Eval(%q) = %v, %v; want %v", c.src, got, err, c.want)
		}
	}
}

type countEnv struct {
	m      MapEnv
	lookup []string
}

func (c *countEnv) Lookup(p string) (any, bool) {
	c.lookup = append(c.lookup, p)
	return c.m.Lookup(p)
}

func TestShortCircuit(t *testing.T) {
	// || 左侧为真：右侧缺失字段不报错，也不被读取
	p, err := Compile(`D.ok || D.status == "x"`, testSchema)
	if err != nil {
		t.Fatal(err)
	}
	env := &countEnv{m: MapEnv{"D": map[string]any{"ok": true}}}
	if got, err := p.Eval(env); err != nil || !got {
		t.Fatalf("got %v, %v", got, err)
	}
	if !reflect.DeepEqual(env.lookup, []string{"D.ok"}) {
		t.Errorf("lookups = %v", env.lookup)
	}
	// && 左侧为假
	p, _ = Compile(`D.ok && D.status == "x"`, testSchema)
	env = &countEnv{m: MapEnv{"D": map[string]any{"ok": false}}}
	if got, err := p.Eval(env); err != nil || got {
		t.Fatalf("got %v, %v", got, err)
	}
	if len(env.lookup) != 1 {
		t.Errorf("lookups = %v", env.lookup)
	}
	// 不短路时右侧缺失会报错
	env = &countEnv{m: MapEnv{"D": map[string]any{"ok": true}}}
	_, err = p.Eval(env)
	wantCode(t, err, errcode.ExprMissingField)
	// 类型错误同样被短路跳过
	p, _ = Compile(`D.ok || D.exit_code == 1`, testSchema)
	if got, err := p.Eval(MapEnv{"D": map[string]any{"ok": true, "exit_code": "oops"}}); err != nil || !got {
		t.Errorf("got %v, %v", got, err)
	}
}

func TestMissingFieldIsNotFalse(t *testing.T) {
	p, _ := Compile(`D.exit_code == 0`, testSchema)
	for _, env := range []Env{nil, MapEnv{}, MapEnv{"D": map[string]any{}}, MapEnv{"D": "not a map"}} {
		got, err := p.Eval(env)
		wantCode(t, err, errcode.ExprMissingField)
		if got {
			t.Error("must not be true on error")
		}
	}
	// 取反也不能把缺失变成 true
	p, _ = Compile(`!(D.exit_code == 0)`, testSchema)
	if got, err := p.Eval(MapEnv{}); err == nil || got {
		t.Errorf("got %v, %v", got, err)
	}
}

func TestRuntimeTypeMismatch(t *testing.T) {
	cases := []struct {
		src string
		env MapEnv
	}{
		{`D.exit_code == 0`, MapEnv{"D": map[string]any{"exit_code": "0"}}},
		{`D.exit_code == 0`, MapEnv{"D": map[string]any{"exit_code": nil}}},
		{`D.exit_code == 0`, MapEnv{"D": map[string]any{"exit_code": math.NaN()}}},
		{`D.exit_code == 0`, MapEnv{"D": map[string]any{"exit_code": math.Inf(1)}}},
		{`D.exit_code == 0`, MapEnv{"D": map[string]any{"exit_code": json.Number("abc")}}},
		{`D.exit_code == 0`, MapEnv{"D": map[string]any{"exit_code": true}}},
		{`D.status == "x"`, MapEnv{"D": map[string]any{"status": 1}}},
		{`D.ok`, MapEnv{"D": map[string]any{"ok": "true"}}},
		{`"a" in D.tags`, MapEnv{"D": map[string]any{"tags": "a"}}},
		{`"a" in D.tags`, MapEnv{"D": map[string]any{"tags": []any{"a", 1}}}},
	}
	for _, c := range cases {
		p, err := Compile(c.src, testSchema)
		if err != nil {
			t.Fatal(err)
		}
		_, err = p.Eval(c.env)
		wantCode(t, err, errcode.ExprType)
	}
}

func TestPaths(t *testing.T) {
	p, err := Compile(`D.status == "ok" && (D.exit_code == 0 || D.exit_code == 2) && params.env in ["a"] && D.status != ""`, testSchema)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"D.exit_code", "D.status", "params.env"}
	if got := p.Paths(); !reflect.DeepEqual(got, want) {
		t.Errorf("Paths = %v", got)
	}
	p.Paths()[0] = "tampered"
	if p.Paths()[0] != "D.exit_code" {
		t.Error("Paths exposes internal slice")
	}
	if p.Source() == "" {
		t.Error("Source")
	}
	e, _ := Parse(`b == a && a == 1`)
	if got := e.Paths(); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("Expr.Paths = %v", got)
	}
}

func TestCompileErrorsPropagate(t *testing.T) {
	_, err := Compile(`a ==`, testSchema)
	wantCode(t, err, errcode.ExprSyntax)
	_, err = Compile(strings.Repeat("(", 40), testSchema)
	wantCode(t, err, errcode.ExprLimit)
	if !errors.Is(err, errcode.New(errcode.ExprLimit, "")) {
		t.Error("errors.Is")
	}
}

func TestMapEnv(t *testing.T) {
	env := MapEnv{"a": map[string]any{"b": MapEnv{"c": 1}}, "x": 2}
	for path, want := range map[string]any{"x": 2, "a.b.c": 1} {
		if v, ok := env.Lookup(path); !ok || v != want {
			t.Errorf("Lookup(%q) = %v, %v", path, v, ok)
		}
	}
	for _, path := range []string{"y", "a.c", "x.y", "a.b.c.d"} {
		if _, ok := env.Lookup(path); ok {
			t.Errorf("Lookup(%q) should miss", path)
		}
	}
}

func TestTypeString(t *testing.T) {
	if ListOf(TString).String() != "list of string" || TBool.String() != "bool" || (Type{}).String() != "invalid" {
		t.Error("Type.String")
	}
	if ListOf(ListOf(TString)).valid() || ListOf(TString).valid() == false {
		t.Error("ListOf validity")
	}
}
