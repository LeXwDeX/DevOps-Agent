package orchestrator

import (
	"encoding/json"
	"math"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/task"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

// checkParams 校验 params：不认识的名字和类型不符都返回 core.invalid_argument。
func checkParams(def *task.Definition, params map[string]any) error {
	specs := make(map[string]task.FieldSpec, len(def.Params))
	for _, p := range def.Params {
		specs[p.Name] = p
	}
	for name, v := range params {
		spec, ok := specs[name]
		if !ok {
			return errcode.Newf(errcode.InvalidArgument, "unknown param %q", name)
		}
		if !valueMatches(v, spec.Type) {
			return errcode.Newf(errcode.InvalidArgument, "param %q: value does not match type %s", name, typeName(spec.Type))
		}
	}
	return nil
}

// missingParams 按定义顺序列出缺少的必需参数。
func missingParams(def *task.Definition, params map[string]any) []string {
	var out []string
	for _, p := range def.Params {
		if !p.Required {
			continue
		}
		if _, ok := params[p.Name]; !ok {
			out = append(out, p.Name)
		}
	}
	return out
}

func typeName(t task.FieldType) string {
	if t.Kind == task.KindList {
		return "list of " + t.Elem
	}
	return t.Kind
}

func valueMatches(v any, t task.FieldType) bool {
	if t.Kind != task.KindList {
		return scalarMatches(v, t.Kind)
	}
	switch l := v.(type) {
	case []any:
		for _, e := range l {
			if !scalarMatches(e, t.Elem) {
				return false
			}
		}
		return true
	case []string:
		return t.Elem == task.KindString
	}
	return false
}

func scalarMatches(v any, kind string) bool {
	switch kind {
	case task.KindString:
		_, ok := v.(string)
		return ok
	case task.KindBool:
		_, ok := v.(bool)
		return ok
	case task.KindNumber:
		switch n := v.(type) {
		case float64:
			return !math.IsNaN(n) && !math.IsInf(n, 0)
		case float32:
			return !math.IsNaN(float64(n)) && !math.IsInf(float64(n), 0)
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			return true
		case json.Number:
			f, err := n.Float64()
			return err == nil && !math.IsInf(f, 0)
		}
	}
	return false
}
