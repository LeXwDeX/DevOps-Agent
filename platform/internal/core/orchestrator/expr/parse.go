package expr

import (
	"sort"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

type node interface{ position() int }

type cmpOp uint8

const (
	opEq cmpOp = iota + 1
	opNe
	opLt
	opLe
	opGt
	opGe
	opIn
)

type (
	litNode struct {
		pos int
		typ Type
		s   string
		n   float64
		b   bool
	}
	pathNode struct {
		pos  int
		path string
	}
	notNode struct {
		pos int
		x   node
	}
	// logicNode 是拉平的 && 或 || 链。
	logicNode struct {
		pos int
		and bool
		xs  []node
	}
	cmpNode struct {
		pos  int
		op   cmpOp
		l, r node
	}
	listNode struct {
		pos   int
		elems []*litNode
	}
)

func (n *litNode) position() int   { return n.pos }
func (n *pathNode) position() int  { return n.pos }
func (n *notNode) position() int   { return n.pos }
func (n *logicNode) position() int { return n.pos }
func (n *cmpNode) position() int   { return n.pos }
func (n *listNode) position() int  { return n.pos }

// Expr 是解析后的表达式，尚未做类型检查。
type Expr struct {
	src  string
	root node
}

// Source 返回原始文本。
func (e *Expr) Source() string { return e.src }

// Paths 返回表达式引用的全部路径，已去重并排序。
func (e *Expr) Paths() []string {
	set := map[string]struct{}{}
	collectPaths(e.root, set)
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func collectPaths(n node, set map[string]struct{}) {
	switch x := n.(type) {
	case *pathNode:
		set[x.path] = struct{}{}
	case *notNode:
		collectPaths(x.x, set)
	case *logicNode:
		for _, c := range x.xs {
			collectPaths(c, set)
		}
	case *cmpNode:
		collectPaths(x.l, set)
		collectPaths(x.r, set)
	}
}

// Parse 解析表达式。语法错误为 core.expr_syntax（带偏移），超限为 core.expr_limit。
func Parse(src string) (*Expr, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	root, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if t := p.peek(); t.kind != tEOF {
		return nil, syntaxErr(t.pos, "unexpected token")
	}
	return &Expr{src: src, root: root}, nil
}

type parser struct {
	toks  []token
	i     int
	depth int
}

func (p *parser) peek() token { return p.toks[p.i] }

func (p *parser) next() token {
	t := p.toks[p.i]
	if t.kind != tEOF {
		p.i++
	}
	return t
}

func (p *parser) enter(pos int) error {
	p.depth++
	if p.depth > MaxDepth {
		return errcode.Newf(errcode.ExprLimit, "offset %d: nesting depth exceeds %d", pos, MaxDepth)
	}
	return nil
}

func (p *parser) parseOr() (node, error) { return p.parseLogic(false) }

func (p *parser) parseLogic(and bool) (node, error) {
	sub, sep := p.parseAnd, tOr
	if and {
		sub, sep = p.parseCmp, tAnd
	}
	first, err := sub()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != sep {
		return first, nil
	}
	n := &logicNode{pos: first.position(), and: and, xs: []node{first}}
	for p.peek().kind == sep {
		p.next()
		x, err := sub()
		if err != nil {
			return nil, err
		}
		n.xs = append(n.xs, x)
	}
	return n, nil
}

func (p *parser) parseAnd() (node, error) { return p.parseLogic(true) }

var cmpOps = map[tokKind]cmpOp{
	tEq: opEq, tNe: opNe, tLt: opLt, tLe: opLe, tGt: opGt, tGe: opGe, tIn: opIn,
}

func (p *parser) parseCmp() (node, error) {
	l, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	t := p.peek()
	op, ok := cmpOps[t.kind]
	if !ok {
		return l, nil
	}
	p.next()
	r, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	return &cmpNode{pos: t.pos, op: op, l: l, r: r}, nil
}

func (p *parser) parseUnary() (node, error) {
	t := p.peek()
	if t.kind != tNot {
		return p.parsePrimary()
	}
	if err := p.enter(t.pos); err != nil {
		return nil, err
	}
	p.next()
	x, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	p.depth--
	return &notNode{pos: t.pos, x: x}, nil
}

func literal(t token) *litNode {
	switch t.kind {
	case tString:
		return &litNode{pos: t.pos, typ: TString, s: t.text}
	case tNumber:
		return &litNode{pos: t.pos, typ: TNumber, n: t.num}
	case tTrue:
		return &litNode{pos: t.pos, typ: TBool, b: true}
	case tFalse:
		return &litNode{pos: t.pos, typ: TBool}
	}
	return nil
}

func (p *parser) parsePrimary() (node, error) {
	t := p.peek()
	if lit := literal(t); lit != nil {
		p.next()
		return lit, nil
	}
	switch t.kind {
	case tPath:
		p.next()
		return &pathNode{pos: t.pos, path: t.text}, nil
	case tLParen:
		if err := p.enter(t.pos); err != nil {
			return nil, err
		}
		p.next()
		x, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if c := p.next(); c.kind != tRParen {
			return nil, syntaxErr(c.pos, "expected ')'")
		}
		p.depth--
		return x, nil
	case tLBrack:
		return p.parseList()
	}
	return nil, syntaxErr(t.pos, "unexpected token, expected a value")
}

func (p *parser) parseList() (node, error) {
	open := p.next()
	n := &listNode{pos: open.pos}
	if p.peek().kind == tRBrack {
		p.next()
		return n, nil
	}
	for {
		t := p.next()
		lit := literal(t)
		if lit == nil {
			return nil, syntaxErr(t.pos, "list elements must be literals")
		}
		if len(n.elems) >= MaxListLen {
			return nil, errcode.Newf(errcode.ExprLimit, "offset %d: list length exceeds %d", t.pos, MaxListLen)
		}
		n.elems = append(n.elems, lit)
		switch c := p.next(); c.kind {
		case tComma:
		case tRBrack:
			return n, nil
		default:
			return nil, syntaxErr(c.pos, "expected ',' or ']'")
		}
	}
}
