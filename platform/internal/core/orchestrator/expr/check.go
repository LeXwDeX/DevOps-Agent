package expr

import (
	"strconv"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

// Kind 是值的基本类别。
type Kind uint8

const (
	KindInvalid Kind = iota
	KindString
	KindNumber
	KindBool
	KindList
)

func (k Kind) String() string {
	switch k {
	case KindString:
		return "string"
	case KindNumber:
		return "number"
	case KindBool:
		return "bool"
	case KindList:
		return "list"
	}
	return "invalid"
}

// Type 是表达式的静态类型。列表的元素只能是 String、Number、Bool。
// 空列表字面量的 Elem 为 KindInvalid，可与任何元素类型比较。
type Type struct {
	Kind Kind
	Elem Kind
}

var (
	TString = Type{Kind: KindString}
	TNumber = Type{Kind: KindNumber}
	TBool   = Type{Kind: KindBool}
)

// ListOf 返回元素类型为 elem 的列表类型；elem 不是标量时返回无效类型。
func ListOf(elem Type) Type {
	if !elem.scalar() {
		return Type{}
	}
	return Type{Kind: KindList, Elem: elem.Kind}
}

func (t Type) scalar() bool {
	return t.Kind == KindString || t.Kind == KindNumber || t.Kind == KindBool
}

func (t Type) valid() bool {
	switch t.Kind {
	case KindString, KindNumber, KindBool:
		return t.Elem == KindInvalid
	case KindList:
		return Type{Kind: t.Elem}.scalar()
	}
	return false
}

func (t Type) String() string {
	if t.Kind == KindList {
		return "list of " + t.Elem.String()
	}
	return t.Kind.String()
}

// Schema 给出每个可引用路径的类型，键是完整的点分路径，例如 "D.exit_code"。
type Schema map[string]Type

func typeErr(n node, format string, args ...any) error {
	return errcode.Newf(errcode.ExprType, "offset "+strconv.Itoa(n.position())+": "+format, args...)
}

// Check 检验整个表达式的结果是 Bool，且所有路径和运算的类型都成立。
func Check(e *Expr, s Schema) error {
	_, err := check(e, s)
	return err
}

// check 返回通过检验后实际引用到的路径类型。
func check(e *Expr, s Schema) (map[string]Type, error) {
	c := &checker{schema: s, used: map[string]Type{}}
	t, err := c.typeOf(e.root)
	if err != nil {
		return nil, err
	}
	if t != TBool {
		return nil, typeErr(e.root, "expression must be bool, got %s", t)
	}
	return c.used, nil
}

type checker struct {
	schema Schema
	used   map[string]Type
}

func (c *checker) typeOf(n node) (Type, error) {
	switch x := n.(type) {
	case *litNode:
		return x.typ, nil
	case *pathNode:
		t, ok := c.schema[x.path]
		if !ok {
			return Type{}, typeErr(n, "unknown path %q", x.path)
		}
		if !t.valid() {
			return Type{}, typeErr(n, "path %q has invalid schema type", x.path)
		}
		c.used[x.path] = t
		return t, nil
	case *notNode:
		return c.wantBool(x.x, "operand of '!'")
	case *logicNode:
		for _, sub := range x.xs {
			if _, err := c.wantBool(sub, "operand of '&&' / '||'"); err != nil {
				return Type{}, err
			}
		}
		return TBool, nil
	case *listNode:
		return Type{}, typeErr(n, "list literal is only allowed on the right side of 'in'")
	case *cmpNode:
		return c.cmp(x)
	}
	return Type{}, typeErr(n, "unsupported node")
}

func (c *checker) wantBool(n node, what string) (Type, error) {
	t, err := c.typeOf(n)
	if err != nil {
		return Type{}, err
	}
	if t != TBool {
		return Type{}, typeErr(n, "%s must be bool, got %s", what, t)
	}
	return TBool, nil
}

func (c *checker) cmp(x *cmpNode) (Type, error) {
	lt, err := c.typeOf(x.l)
	if err != nil {
		return Type{}, err
	}
	if x.op == opIn {
		return c.in(x, lt)
	}
	rt, err := c.typeOf(x.r)
	if err != nil {
		return Type{}, err
	}
	if !lt.scalar() || lt != rt {
		return Type{}, typeErr(x, "cannot compare %s with %s", lt, rt)
	}
	switch x.op {
	case opLt, opLe, opGt, opGe:
		if lt.Kind == KindBool {
			return Type{}, typeErr(x, "ordering is not defined for bool")
		}
	}
	return TBool, nil
}

func (c *checker) in(x *cmpNode, lt Type) (Type, error) {
	var rt Type
	if l, ok := x.r.(*listNode); ok {
		var err error
		if rt, err = listType(l); err != nil {
			return Type{}, err
		}
	} else {
		var err error
		if rt, err = c.typeOf(x.r); err != nil {
			return Type{}, err
		}
	}
	if rt.Kind != KindList {
		return Type{}, typeErr(x.r, "right side of 'in' must be a list, got %s", rt)
	}
	if !lt.scalar() {
		return Type{}, typeErr(x.l, "left side of 'in' must be a scalar, got %s", lt)
	}
	if rt.Elem != KindInvalid && rt.Elem != lt.Kind {
		return Type{}, typeErr(x, "cannot test %s in %s", lt, rt)
	}
	return TBool, nil
}

func listType(l *listNode) (Type, error) {
	t := Type{Kind: KindList}
	for _, e := range l.elems {
		if t.Elem == KindInvalid {
			t.Elem = e.typ.Kind
		} else if t.Elem != e.typ.Kind {
			return Type{}, typeErr(e, "list mixes %s and %s", t.Elem, e.typ.Kind)
		}
	}
	return t, nil
}
