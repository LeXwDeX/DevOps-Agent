package expr

import (
	"encoding/json"
	"math"
	"strings"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

// Env 供求值时读取路径的值。第二个返回值为 false 表示路径不存在。
type Env interface {
	Lookup(path string) (any, bool)
}

// MapEnv 把嵌套的 map[string]any 当作 Env，路径按 "." 逐级下钻。
type MapEnv map[string]any

func (m MapEnv) Lookup(path string) (any, bool) {
	var cur any = map[string]any(m)
	for _, seg := range strings.Split(path, ".") {
		var next any
		var ok bool
		switch mm := cur.(type) {
		case map[string]any:
			next, ok = mm[seg]
		case MapEnv:
			next, ok = mm[seg]
		}
		if !ok {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

// Program 是登记时编译好的表达式。运行时只求值，不再解析新表达式。可并发使用。
type Program struct {
	src   string
	root  node
	paths []string
	types map[string]Type
}

// Compile = Parse + Check，登记时调用。
func Compile(src string, s Schema) (*Program, error) {
	e, err := Parse(src)
	if err != nil {
		return nil, err
	}
	used, err := check(e, s)
	if err != nil {
		return nil, err
	}
	return &Program{src: src, root: e.root, paths: e.Paths(), types: used}, nil
}

// Source 返回原始文本。
func (p *Program) Source() string { return p.src }

// Paths 返回引用的路径，已排序，返回副本。
func (p *Program) Paths() []string { return append([]string(nil), p.paths...) }

// Eval 求值。路径在运行时缺失返回 core.expr_missing_field（不当作 false）；
// 值的类型与 schema 不符返回 core.expr_type。&& 和 || 短路。
func (p *Program) Eval(env Env) (bool, error) {
	if env == nil {
		env = MapEnv(nil)
	}
	v, err := p.eval(p.root, env)
	if err != nil {
		return false, err
	}
	b, _ := v.(bool)
	return b, nil
}

// 运行时值只有 string、float64、bool、[]any 四种。
func (p *Program) eval(n node, env Env) (any, error) {
	switch x := n.(type) {
	case *litNode:
		return litValue(x), nil
	case *pathNode:
		raw, ok := env.Lookup(x.path)
		if !ok {
			return nil, errcode.Newf(errcode.ExprMissingField, "missing field %q", x.path)
		}
		return coerce(raw, p.types[x.path], x.path)
	case *notNode:
		v, err := p.eval(x.x, env)
		if err != nil {
			return nil, err
		}
		return !v.(bool), nil
	case *logicNode:
		for _, sub := range x.xs {
			v, err := p.eval(sub, env)
			if err != nil {
				return nil, err
			}
			if v.(bool) != x.and {
				return !x.and, nil // && 遇 false、|| 遇 true 即短路
			}
		}
		return x.and, nil
	case *listNode:
		out := make([]any, len(x.elems))
		for i, e := range x.elems {
			out[i] = litValue(e)
		}
		return out, nil
	case *cmpNode:
		l, err := p.eval(x.l, env)
		if err != nil {
			return nil, err
		}
		r, err := p.eval(x.r, env)
		if err != nil {
			return nil, err
		}
		return compare(x.op, l, r), nil
	}
	return nil, errcode.New(errcode.ExprType, "unsupported node")
}

func litValue(l *litNode) any {
	switch l.typ.Kind {
	case KindString:
		return l.s
	case KindNumber:
		return l.n
	}
	return l.b
}

// compare 的操作数类型已由 Check 和 coerce 保证一致。
func compare(op cmpOp, l, r any) bool {
	if op == opIn {
		for _, e := range r.([]any) {
			if e == l {
				return true
			}
		}
		return false
	}
	switch a := l.(type) {
	case float64:
		b := r.(float64)
		switch op {
		case opEq:
			return a == b
		case opNe:
			return a != b
		case opLt:
			return a < b
		case opLe:
			return a <= b
		case opGt:
			return a > b
		case opGe:
			return a >= b
		}
	case string:
		b := r.(string)
		switch op {
		case opEq:
			return a == b
		case opNe:
			return a != b
		case opLt:
			return a < b
		case opLe:
			return a <= b
		case opGt:
			return a > b
		case opGe:
			return a >= b
		}
	case bool:
		b := r.(bool)
		if op == opEq {
			return a == b
		}
		return a != b
	}
	return false
}

// coerce 把运行时值按 schema 类型归一化。
// 数字统一为 float64，大于 2^53 的整数会丢失精度。
func coerce(v any, t Type, path string) (any, error) {
	bad := func(why string) (any, error) {
		return nil, errcode.Newf(errcode.ExprType, "field %q: %s (want %s)", path, why, t)
	}
	switch t.Kind {
	case KindString:
		if s, ok := v.(string); ok {
			return s, nil
		}
		return bad("not a string")
	case KindBool:
		if b, ok := v.(bool); ok {
			return b, nil
		}
		return bad("not a bool")
	case KindNumber:
		f, ok := toNumber(v)
		if !ok {
			return bad("not a finite number")
		}
		return f, nil
	case KindList:
		var items []any
		switch l := v.(type) {
		case []any:
			items = l
		case []string:
			items = make([]any, len(l))
			for i, s := range l {
				items[i] = s
			}
		default:
			return bad("not a list")
		}
		elem := Type{Kind: t.Elem}
		out := make([]any, len(items))
		for i, it := range items {
			c, err := coerce(it, elem, path)
			if err != nil {
				return nil, err
			}
			out[i] = c
		}
		return out, nil
	}
	return bad("invalid schema type")
}

func toNumber(v any) (float64, bool) {
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
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}
