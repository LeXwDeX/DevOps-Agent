// Package expr 实现分支条件和成功判据共用的小型类型化条件表达式
// （61 第 3.3、7.3 节）。纯逻辑，只依赖标准库，不支持函数调用、赋值、循环和 null。
//
// 语法：
//
//	or    := and ("||" and)*
//	and   := cmp ("&&" cmp)*
//	cmp   := unary [("==" | "!=" | "<" | "<=" | ">" | ">=" | "in") unary]
//	unary := "!" unary | primary
//	primary := path | string | number | "true" | "false" | list | "(" or ")"
//	list  := "[" [literal ("," literal)*] "]"
//
// 比较不连写（a == b == c 是语法错误，要写括号）。"!" 比比较优先级高，
// 所以 !x in [..] 等于 (!x) in [..]，要取反请写 !(x in [..])。
// 数字字面量可带负号（-1），没有减法。列表字面量语法上可出现在任何位置，
// 类型检查只允许它出现在 in 的右侧。
package expr

import (
	"strconv"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

// 上限（超出返回 core.expr_limit）。
const (
	MaxSourceBytes = 4096
	MaxDepth       = 32
	MaxListLen     = 256
)

type tokKind uint8

const (
	tEOF tokKind = iota
	tPath
	tString
	tNumber
	tTrue
	tFalse
	tIn
	tLParen
	tRParen
	tLBrack
	tRBrack
	tComma
	tNot
	tAnd
	tOr
	tEq
	tNe
	tLt
	tLe
	tGt
	tGe
)

type token struct {
	kind tokKind
	pos  int
	text string  // 路径或字符串内容
	num  float64 // 数字字面量
}

func syntaxErr(pos int, format string, args ...any) error {
	return errcode.Newf(errcode.ExprSyntax, "offset "+strconv.Itoa(pos)+": "+format, args...)
}

func isIdentStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isIdentChar(c byte) bool { return isIdentStart(c) || isDigit(c) }

func lex(src string) ([]token, error) {
	if len(src) > MaxSourceBytes {
		return nil, errcode.Newf(errcode.ExprLimit, "source length %d exceeds %d bytes", len(src), MaxSourceBytes)
	}
	if !utf8.ValidString(src) {
		return nil, syntaxErr(0, "invalid UTF-8")
	}
	var toks []token
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case isIdentStart(c):
			start := i
			for {
				for i < len(src) && isIdentChar(src[i]) {
					i++
				}
				if i < len(src) && src[i] == '.' {
					if i+1 >= len(src) || !isIdentStart(src[i+1]) {
						return nil, syntaxErr(i, "expected identifier after '.'")
					}
					i++
					continue
				}
				break
			}
			text := src[start:i]
			kind := tPath
			switch text {
			case "true":
				kind = tTrue
			case "false":
				kind = tFalse
			case "in":
				kind = tIn
			case "null":
				return nil, syntaxErr(start, "null is not supported")
			}
			toks = append(toks, token{kind: kind, pos: start, text: text})
		case isDigit(c) || c == '-' && i+1 < len(src) && isDigit(src[i+1]):
			start := i
			if c == '-' {
				i++
			}
			for i < len(src) && isDigit(src[i]) {
				i++
			}
			if i < len(src) && src[i] == '.' {
				i++
				if i >= len(src) || !isDigit(src[i]) {
					return nil, syntaxErr(i, "expected digit after '.'")
				}
				for i < len(src) && isDigit(src[i]) {
					i++
				}
			}
			if i < len(src) && (isIdentChar(src[i]) || src[i] == '.') {
				return nil, syntaxErr(i, "unexpected %q after number", src[i])
			}
			n, err := strconv.ParseFloat(src[start:i], 64)
			if err != nil {
				return nil, syntaxErr(start, "number %q out of range", src[start:i])
			}
			toks = append(toks, token{kind: tNumber, pos: start, num: n})
		case c == '"':
			s, next, err := lexString(src, i)
			if err != nil {
				return nil, err
			}
			toks = append(toks, token{kind: tString, pos: i, text: s})
			i = next
		default:
			kind, n := lexOp(src, i)
			if n == 0 {
				return nil, syntaxErr(i, "unexpected character %q", c)
			}
			toks = append(toks, token{kind: kind, pos: i})
			i += n
		}
	}
	toks = append(toks, token{kind: tEOF, pos: len(src)})
	return toks, nil
}

func lexOp(src string, i int) (tokKind, int) {
	two := ""
	if i+1 < len(src) {
		two = src[i : i+2]
	}
	switch two {
	case "==":
		return tEq, 2
	case "!=":
		return tNe, 2
	case "<=":
		return tLe, 2
	case ">=":
		return tGe, 2
	case "&&":
		return tAnd, 2
	case "||":
		return tOr, 2
	}
	switch src[i] {
	case '<':
		return tLt, 1
	case '>':
		return tGt, 1
	case '!':
		return tNot, 1
	case '(':
		return tLParen, 1
	case ')':
		return tRParen, 1
	case '[':
		return tLBrack, 1
	case ']':
		return tRBrack, 1
	case ',':
		return tComma, 1
	}
	return 0, 0
}

// lexString 解析从 start 处的 '"' 开始的字符串字面量。
// 转义同 JSON：\" \\ \/ \b \f \n \r \t \uXXXX。不允许裸控制字符。
func lexString(src string, start int) (string, int, error) {
	var out []byte
	i := start + 1
	for i < len(src) {
		c := src[i]
		switch {
		case c == '"':
			return string(out), i + 1, nil
		case c < 0x20:
			return "", 0, syntaxErr(i, "control character in string")
		case c != '\\':
			out = append(out, c)
			i++
		default:
			if i+1 >= len(src) {
				return "", 0, syntaxErr(i, "unterminated string")
			}
			e := src[i+1]
			i += 2
			switch e {
			case '"', '\\', '/':
				out = append(out, e)
			case 'b':
				out = append(out, '\b')
			case 'f':
				out = append(out, '\f')
			case 'n':
				out = append(out, '\n')
			case 'r':
				out = append(out, '\r')
			case 't':
				out = append(out, '\t')
			case 'u':
				r, next, err := lexUnicode(src, i, start)
				if err != nil {
					return "", 0, err
				}
				out = utf8.AppendRune(out, r)
				i = next
			default:
				return "", 0, syntaxErr(i-2, "invalid escape \\%c", e)
			}
		}
	}
	return "", 0, syntaxErr(start, "unterminated string")
}

func hex4(src string, i int) (rune, bool) {
	if i+4 > len(src) {
		return 0, false
	}
	n, err := strconv.ParseUint(src[i:i+4], 16, 32)
	if err != nil || src[i] == '+' || src[i] == '-' {
		return 0, false
	}
	return rune(n), true
}

func lexUnicode(src string, i, strStart int) (rune, int, error) {
	r, ok := hex4(src, i)
	if !ok {
		return 0, 0, syntaxErr(i-2, "invalid \\u escape")
	}
	i += 4
	if !utf16.IsSurrogate(r) {
		return r, i, nil
	}
	if r < 0xDC00 && i+6 <= len(src) && src[i] == '\\' && src[i+1] == 'u' {
		if r2, ok := hex4(src, i+2); ok && r2 >= 0xDC00 && r2 < 0xE000 {
			return utf16.DecodeRune(r, r2), i + 6, nil
		}
	}
	return 0, 0, syntaxErr(i-6, "invalid surrogate in \\u escape")
}
