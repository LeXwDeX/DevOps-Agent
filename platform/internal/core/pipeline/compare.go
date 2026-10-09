package pipeline

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/orchestrator/expr"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/consts"
)

// Compare 把执行结果与执行预期对比（61 第 5 节、第 3.3 节），返回终态和一句原因。纯函数。
//
//   - res.Err 非空 → failed，不看判据。
//   - 求值环境是 res.Output 加 exit_code；exit_code 永远取自 res.ExitCode（没有则不设该字段，
//     res.Output 里同名的值也要去掉，不让执行输出冒充退出码）。
//   - 每个判据：文字判据 → unknown（交给人工）；表达式按环境里实际值推断出 expr.Schema 后编译，
//     引用的字段不存在、语法或类型错误、求值出错 → unknown；求值为假 → mismatched。
//   - 预期变更里的结构化范围（ExpectedChange.Ranges）同样检查：字段缺失或不是数字 → unknown，越界 → mismatched。
//   - 汇总：任一为假 → mismatched；否则任一 unknown → unknown；否则 succeeded。
//     没有任何判据也不能算成功，记 unknown（K-05：没有证据不记成功）。
func Compare(r *request.ExecutionRequest, res request.ExecutionResult) (consts.RequestState, string) {
	if res.Err != "" {
		return consts.RequestFailed, "executor reported an error"
	}
	env := request.CloneMap(res.Output)
	if env == nil {
		env = map[string]any{}
	}
	delete(env, "exit_code")
	if res.ExitCode != nil {
		env["exit_code"] = *res.ExitCode
	}
	schema := inferSchema(env)
	menv := expr.MapEnv(env)

	var falses, unknowns []string
	for i, c := range r.Expectation.SuccessCriteria {
		if strings.TrimSpace(c.Expr) == "" {
			unknowns = append(unknowns, fmt.Sprintf("criterion #%d is text only, needs human check", i+1))
			continue
		}
		prog, err := expr.Compile(c.Expr, schema)
		if err != nil {
			unknowns = append(unknowns, fmt.Sprintf("criterion #%d %q cannot be evaluated: %s", i+1, c.Expr, reasonOf(err)))
			continue
		}
		ok, err := prog.Eval(menv)
		switch {
		case err != nil:
			unknowns = append(unknowns, fmt.Sprintf("criterion #%d %q cannot be evaluated: %s", i+1, c.Expr, reasonOf(err)))
		case !ok:
			falses = append(falses, fmt.Sprintf("criterion #%d %q is false", i+1, c.Expr))
		}
	}
	for _, rg := range r.Expectation.ExpectedChange.Ranges {
		v, ok := menv.Lookup(rg.Field)
		f, isNum := numberOf(v)
		switch {
		case !ok || !isNum:
			unknowns = append(unknowns, fmt.Sprintf("range field %q is missing or not a number", rg.Field))
		case (rg.Min != nil && f < *rg.Min) || (rg.Max != nil && f > *rg.Max):
			falses = append(falses, fmt.Sprintf("range field %q is out of the expected range", rg.Field))
		}
	}
	switch {
	case len(falses) > 0:
		return consts.RequestMismatched, strings.Join(append(falses, unknowns...), "; ")
	case len(unknowns) > 0:
		return consts.RequestUnknown, strings.Join(unknowns, "; ")
	case len(r.Expectation.SuccessCriteria) == 0 && len(r.Expectation.ExpectedChange.Ranges) == 0:
		return consts.RequestUnknown, "no success criteria to compare against"
	}
	return consts.RequestSucceeded, "result matches the expectation"
}

// inferSchema 按环境里的实际值推断表达式类型：字符串、数字、布尔、元素类型一致的非空列表。
// 嵌套 map 展开成点分路径。其他值（空列表、混合列表、nil 等）不进 schema，引用它们的判据得到 unknown。
func inferSchema(env map[string]any) expr.Schema {
	s := expr.Schema{}
	var walk func(prefix string, m map[string]any)
	walk = func(prefix string, m map[string]any) {
		for k, v := range m {
			path := prefix + k
			if sub, ok := v.(map[string]any); ok {
				walk(path+".", sub)
			} else if t, ok := typeOf(v); ok {
				s[path] = t
			}
		}
	}
	walk("", env)
	return s
}

func typeOf(v any) (expr.Type, bool) {
	if k, ok := scalarKind(v); ok {
		return expr.Type{Kind: k}, true
	}
	var items []any
	switch l := v.(type) {
	case []any:
		items = l
	case []string:
		for _, s := range l {
			items = append(items, s)
		}
	default:
		return expr.Type{}, false
	}
	if len(items) == 0 {
		return expr.Type{}, false
	}
	first, ok := scalarKind(items[0])
	if !ok {
		return expr.Type{}, false
	}
	for _, it := range items[1:] {
		if k, ok := scalarKind(it); !ok || k != first {
			return expr.Type{}, false
		}
	}
	return expr.ListOf(expr.Type{Kind: first}), true
}

func scalarKind(v any) (expr.Kind, bool) {
	switch v.(type) {
	case string:
		return expr.KindString, true
	case bool:
		return expr.KindBool, true
	}
	if _, ok := numberOf(v); ok {
		return expr.KindNumber, true
	}
	return expr.KindInvalid, false
}

// numberOf 把常见数字类型转成 float64；NaN 和无穷不算数字。
func numberOf(v any) (float64, bool) {
	var f float64
	switch n := v.(type) {
	case float64:
		f = n
	case float32:
		f = float64(n)
	case int:
		f = float64(n)
	case int8:
		f = float64(n)
	case int16:
		f = float64(n)
	case int32:
		f = float64(n)
	case int64:
		f = float64(n)
	case uint:
		f = float64(n)
	case uint8:
		f = float64(n)
	case uint16:
		f = float64(n)
	case uint32:
		f = float64(n)
	case uint64:
		f = float64(n)
	case json.Number:
		var err error
		if f, err = n.Float64(); err != nil {
			return 0, false
		}
	default:
		return 0, false
	}
	return f, !math.IsNaN(f) && !math.IsInf(f, 0)
}
