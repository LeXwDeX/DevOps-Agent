package pipeline_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/pipeline"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/consts"
)

func expReq(crit ...request.Criterion) *request.ExecutionRequest {
	return &request.ExecutionRequest{Expectation: request.Expectation{SuccessCriteria: crit}}
}

func ex(s string) request.Criterion  { return request.Criterion{Expr: s} }
func txt(s string) request.Criterion { return request.Criterion{Text: s} }
func f64(v float64) *float64         { return &v }

func TestCompare(t *testing.T) {
	rows := []struct {
		name string
		req  *request.ExecutionRequest
		res  request.ExecutionResult
		want consts.RequestState
		why  string
	}{
		{"exit code zero", expReq(ex("exit_code == 0")), request.ExecutionResult{ExitCode: intp(0)}, consts.RequestSucceeded, ""},
		{"exit code nonzero", expReq(ex("exit_code == 0")), request.ExecutionResult{ExitCode: intp(1)}, consts.RequestMismatched, "exit_code == 0"},
		{"err wins over passing criteria", expReq(ex("exit_code == 0")), request.ExecutionResult{ExitCode: intp(0), Err: "boom"}, consts.RequestFailed, "error"},
		{"err wins over nothing", expReq(), request.ExecutionResult{Err: "x"}, consts.RequestFailed, ""},
		{"text only", expReq(txt("check the dashboard")), request.ExecutionResult{ExitCode: intp(0)}, consts.RequestUnknown, "text only"},
		{"text plus passing expr stays unknown", expReq(ex("exit_code == 0"), txt("eyeball it")), request.ExecutionResult{ExitCode: intp(0)}, consts.RequestUnknown, "text only"},
		{"false beats text", expReq(txt("eyeball it"), ex("exit_code == 0")), request.ExecutionResult{ExitCode: intp(3)}, consts.RequestMismatched, "text only"},
		{"false beats missing field", expReq(ex("rows <= 1"), ex("exit_code == 0")), request.ExecutionResult{ExitCode: intp(3)}, consts.RequestMismatched, ""},
		{"missing field is unknown not false", expReq(ex("rows_affected <= 120")), request.ExecutionResult{ExitCode: intp(0), Output: map[string]any{"other": 1}}, consts.RequestUnknown, "rows_affected"},
		{"present field passes", expReq(ex("rows_affected <= 120")), request.ExecutionResult{Output: map[string]any{"rows_affected": 100}}, consts.RequestSucceeded, ""},
		{"present field fails", expReq(ex("rows_affected <= 120")), request.ExecutionResult{Output: map[string]any{"rows_affected": 130}}, consts.RequestMismatched, ""},
		{"int64 and float64 numbers", expReq(ex("a == 2 && b == 2")), request.ExecutionResult{Output: map[string]any{"a": int64(2), "b": 2.0}}, consts.RequestSucceeded, ""},
		{"json number", expReq(ex("a == 2")), request.ExecutionResult{Output: map[string]any{"a": json.Number("2")}}, consts.RequestSucceeded, ""},
		{"string and bool", expReq(ex(`status == "active" && !degraded`)), request.ExecutionResult{Output: map[string]any{"status": "active", "degraded": false}}, consts.RequestSucceeded, ""},
		{"in list literal", expReq(ex(`status in ["active", "running"]`)), request.ExecutionResult{Output: map[string]any{"status": "running"}}, consts.RequestSucceeded, ""},
		{"in list field", expReq(ex(`"web" in roles`)), request.ExecutionResult{Output: map[string]any{"roles": []any{"db", "web"}}}, consts.RequestSucceeded, ""},
		{"in string slice field", expReq(ex(`"web" in roles`)), request.ExecutionResult{Output: map[string]any{"roles": []string{"db"}}}, consts.RequestMismatched, ""},
		{"nested path", expReq(ex("db.rows > 1")), request.ExecutionResult{Output: map[string]any{"db": map[string]any{"rows": 5}}}, consts.RequestSucceeded, ""},
		{"nested path missing", expReq(ex("db.rows > 1")), request.ExecutionResult{Output: map[string]any{"db": map[string]any{"cols": 5}}}, consts.RequestUnknown, ""},
		{"output cannot spoof exit code", expReq(ex("exit_code == 0")), request.ExecutionResult{Output: map[string]any{"exit_code": 0}}, consts.RequestUnknown, "exit_code"},
		{"exit code field overrides output", expReq(ex("exit_code == 0")), request.ExecutionResult{ExitCode: intp(5), Output: map[string]any{"exit_code": 0}}, consts.RequestMismatched, ""},
		{"type mismatch is unknown", expReq(ex(`exit_code == "0"`)), request.ExecutionResult{ExitCode: intp(0)}, consts.RequestUnknown, "cannot be evaluated"},
		{"syntax error is unknown", expReq(ex("exit_code ==")), request.ExecutionResult{ExitCode: intp(0)}, consts.RequestUnknown, "cannot be evaluated"},
		{"empty list field cannot be typed", expReq(ex(`"a" in xs`)), request.ExecutionResult{Output: map[string]any{"xs": []any{}}}, consts.RequestUnknown, ""},
		{"mixed list cannot be typed", expReq(ex(`"a" in xs`)), request.ExecutionResult{Output: map[string]any{"xs": []any{"a", 1}}}, consts.RequestUnknown, ""},
		{"no criteria is never success", expReq(), request.ExecutionResult{ExitCode: intp(0)}, consts.RequestUnknown, "no success criteria"},
		{"nil output", expReq(ex("exit_code == 0")), request.ExecutionResult{ExitCode: intp(0), Output: nil}, consts.RequestSucceeded, ""},
	}
	for _, rw := range rows {
		t.Run(rw.name, func(t *testing.T) {
			got, why := pipeline.Compare(rw.req, rw.res)
			if got != rw.want {
				t.Fatalf("state = %s (%s), want %s", got, why, rw.want)
			}
			if !strings.Contains(why, rw.why) {
				t.Errorf("reason %q lacks %q", why, rw.why)
			}
			if why == "" {
				t.Error("empty reason")
			}
		})
	}
}

func TestCompareRanges(t *testing.T) {
	req := func(min, max *float64) *request.ExecutionRequest {
		r := expReq(ex("exit_code == 0"))
		r.Expectation.ExpectedChange.Ranges = []request.ValueRange{{Field: "rows", Min: min, Max: max}}
		return r
	}
	res := func(rows any) request.ExecutionResult {
		out := map[string]any{}
		if rows != nil {
			out["rows"] = rows
		}
		return request.ExecutionResult{ExitCode: intp(0), Output: out}
	}
	cases := []struct {
		name string
		r    *request.ExecutionRequest
		res  request.ExecutionResult
		want consts.RequestState
	}{
		{"inside", req(f64(1), f64(10)), res(5), consts.RequestSucceeded},
		{"on the bounds", req(f64(1), f64(10)), res(10), consts.RequestSucceeded},
		{"above max", req(f64(1), f64(10)), res(11), consts.RequestMismatched},
		{"below min", req(f64(1), nil), res(0), consts.RequestMismatched},
		{"only max", req(nil, f64(10)), res(-100), consts.RequestSucceeded},
		{"missing field", req(f64(1), f64(10)), res(nil), consts.RequestUnknown},
		{"not a number", req(f64(1), f64(10)), res("many"), consts.RequestUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got, why := pipeline.Compare(c.r, c.res); got != c.want {
				t.Errorf("state = %s (%s), want %s", got, why, c.want)
			}
		})
	}
}

// Compare 是纯函数：不改入参。
func TestCompareDoesNotMutateInputs(t *testing.T) {
	r := expReq(ex("exit_code == 0 && a == 1"))
	res := request.ExecutionResult{ExitCode: intp(0), Output: map[string]any{"a": 1, "nested": map[string]any{"k": "v"}}}
	rBefore, resBefore := r.Clone(), res.Clone()
	pipeline.Compare(r, res)
	if !reflect.DeepEqual(r, rBefore) || !reflect.DeepEqual(res.Output, resBefore.Output) {
		t.Error("Compare mutated its inputs")
	}
	if _, ok := res.Output["exit_code"]; ok {
		t.Error("exit_code leaked into the executor output")
	}
}
