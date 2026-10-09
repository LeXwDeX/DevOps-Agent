package request

import (
	"reflect"
	"slices"
)

// CloneMap 深拷贝类 JSON 的 map（Params、Output）。nil 返回 nil。
func CloneMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = CloneValue(v)
	}
	return out
}

// CloneValue 深拷贝类 JSON 的值：map、slice、指针、接口递归拷贝，标量原样返回。
// 结构体按值拷贝，其内部的引用不再深拷贝；类 JSON 数据里不应出现结构体。
func CloneValue(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case map[string]any:
		return CloneMap(x)
	case []any:
		if x == nil {
			return x
		}
		out := make([]any, len(x))
		for i := range x {
			out[i] = CloneValue(x[i])
		}
		return out
	case string, bool, int, int64, float64, float32, int32, uint, uint64, uint32:
		return v
	}
	return deepCopy(reflect.ValueOf(v)).Interface()
}

func deepCopy(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Map:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		iter := v.MapRange()
		for iter.Next() {
			out.SetMapIndex(iter.Key(), deepCopy(iter.Value()))
		}
		return out
	case reflect.Slice:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(deepCopy(v.Index(i)))
		}
		return out
	case reflect.Pointer:
		if v.IsNil() {
			return v
		}
		out := reflect.New(v.Type().Elem())
		out.Elem().Set(deepCopy(v.Elem()))
		return out
	case reflect.Interface:
		if v.IsNil() {
			return v
		}
		out := reflect.New(v.Type()).Elem()
		out.Set(deepCopy(v.Elem()))
		return out
	}
	return v
}

func cloneStrings(in []string) []string {
	if in == nil {
		return nil
	}
	return slices.Clone(in)
}

func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	c := *p
	return &c
}

// Clone 深拷贝 Scope。
func (s Scope) Clone() Scope {
	s.Targets = cloneStrings(s.Targets)
	return s
}

// Clone 深拷贝 Content。
func (c Content) Clone() Content {
	c.Unit = clonePtr(c.Unit)
	c.Params = CloneMap(c.Params)
	c.SecretKeys = cloneStrings(c.SecretKeys)
	return c
}

// Clone 深拷贝 Expectation。
func (e Expectation) Clone() Expectation {
	e.Targets = cloneStrings(e.Targets)
	e.SuccessCriteria = slices.Clone(e.SuccessCriteria)
	if e.ExpectedChange.Ranges != nil {
		rs := make([]ValueRange, len(e.ExpectedChange.Ranges))
		for i, r := range e.ExpectedChange.Ranges {
			r.Min, r.Max = clonePtr(r.Min), clonePtr(r.Max)
			rs[i] = r
		}
		e.ExpectedChange.Ranges = rs
	}
	return e
}

// Clone 深拷贝 Submission。
func (s Submission) Clone() Submission {
	s.SelfReview = clonePtr(s.SelfReview)
	s.HookReasons = cloneStrings(s.HookReasons)
	return s
}

// Clone 深拷贝 Checks。
func (c Checks) Clone() Checks {
	c.Preview = clonePtr(c.Preview)
	c.SelfReview = clonePtr(c.SelfReview)
	if c.DCG != nil {
		d := *c.DCG
		d.RuleIDs = cloneStrings(d.RuleIDs)
		c.DCG = &d
	}
	if c.Policy != nil {
		d := c.Policy.Clone()
		c.Policy = &d
	}
	c.Escalations = cloneStrings(c.Escalations)
	return c
}

// Clone 深拷贝 ExecutionResult。
func (r *ExecutionResult) Clone() *ExecutionResult {
	if r == nil {
		return nil
	}
	c := *r
	c.ExitCode = clonePtr(r.ExitCode)
	c.Output = CloneMap(r.Output)
	return &c
}

// Clone 深拷贝整个执行申请：map、切片和指针都不与原值共享。
func (r *ExecutionRequest) Clone() *ExecutionRequest {
	if r == nil {
		return nil
	}
	c := *r
	c.Task = clonePtr(r.Task)
	c.Scope = r.Scope.Clone()
	c.Content = r.Content.Clone()
	c.Expectation = r.Expectation.Clone()
	c.Submission = r.Submission.Clone()
	c.Fingerprint = clonePtr(r.Fingerprint)
	c.Checks = r.Checks.Clone()
	c.Reviews = slices.Clone(r.Reviews)
	c.Rejection = clonePtr(r.Rejection)
	c.Stop = clonePtr(r.Stop)
	c.Frozen = clonePtr(r.Frozen)
	if r.Execution != nil {
		e := *r.Execution
		e.Result = r.Execution.Result.Clone()
		c.Execution = &e
	}
	return &c
}
