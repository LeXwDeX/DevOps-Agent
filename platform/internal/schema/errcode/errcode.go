// Package errcode 集中定义错误码和带码错误。
// 格式 <层或模块>.<原因>，见 docs/50-engineering/52-schema-and-constants.md 第 4.1 节。
package errcode

import (
	"errors"
	"fmt"
	"sort"
)

// Code 是稳定的错误码字符串。
type Code string

const (
	InvalidTransition    Code = "core.invalid_transition"
	TerminalState        Code = "core.terminal_state"
	ScopeViolation       Code = "core.scope_violation"
	UnitMismatch         Code = "core.unit_mismatch"
	FrozenImmutable      Code = "core.frozen_immutable"
	InvalidRevisionSrc   Code = "core.invalid_revision_source"
	CASConflict          Code = "core.cas_conflict"
	NotFound             Code = "core.not_found"
	InvalidArgument      Code = "core.invalid_argument"
	DigestMismatch       Code = "core.digest_mismatch"
	FingerprintChanged   Code = "core.fingerprint_changed"
	CanonicalUnsupported Code = "core.canonical_unsupported"
	ExprSyntax           Code = "core.expr_syntax"
	ExprType             Code = "core.expr_type"
	ExprMissingField     Code = "core.expr_missing_field"
	ExprLimit            Code = "core.expr_limit"
)

// Info 描述一个错误码：含义、是否可重试、对应的申请状态（可为空）。
type Info struct {
	Code         Code
	Meaning      string
	Retryable    bool
	RequestState string
}

var catalog = map[Code]Info{
	InvalidTransition:    {InvalidTransition, "状态转换不在状态机表内", false, ""},
	TerminalState:        {TerminalState, "对象已在终态，不再转换", false, ""},
	ScopeViolation:       {ScopeViolation, "目标超出会话的项目或环境范围", false, "rejected"},
	UnitMismatch:         {UnitMismatch, "已登记单元的版本或摘要与登记不一致", false, "rejected"},
	FrozenImmutable:      {FrozenImmutable, "申请已冻结，内容不可再改", false, "frozen"},
	InvalidRevisionSrc:   {InvalidRevisionSrc, "修订来源只能是自审或人工点评", false, ""},
	CASConflict:          {CASConflict, "乐观锁版本冲突，需重新读取后重试", true, ""},
	NotFound:             {NotFound, "对象不存在", false, ""},
	InvalidArgument:      {InvalidArgument, "参数缺失、非法或枚举值未知", false, ""},
	DigestMismatch:       {DigestMismatch, "内容摘要与预期不一致", false, ""},
	FingerprintChanged:   {FingerprintChanged, "执行前指纹与检验时不一致，目标已变化", false, "target_changed"},
	CanonicalUnsupported: {CanonicalUnsupported, "内容无法规范化（不支持的类型或数值）", false, ""},
	ExprSyntax:           {ExprSyntax, "条件表达式语法错误", false, ""},
	ExprType:             {ExprType, "条件表达式类型错误：未知路径、类型不符或结果非布尔", false, ""},
	ExprMissingField:     {ExprMissingField, "条件表达式运行时缺少所需字段", false, ""},
	ExprLimit:            {ExprLimit, "条件表达式超出长度、嵌套或列表上限", false, ""},
}

// Lookup 查询错误码的登记信息。
func Lookup(c Code) (Info, bool) {
	i, ok := catalog[c]
	return i, ok
}

// All 返回全部登记信息，按错误码排序。
func All() []Info {
	out := make([]Info, 0, len(catalog))
	for _, i := range catalog {
		out = append(out, i)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Code < out[b].Code })
	return out
}

// Error 是带错误码的错误。
type Error struct {
	Code  Code
	Msg   string
	Cause error
}

func New(code Code, msg string) *Error { return &Error{Code: code, Msg: msg} }

func Wrap(code Code, msg string, cause error) *Error {
	return &Error{Code: code, Msg: msg, Cause: cause}
}

// Newf 是带格式化的 New。
func Newf(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, args...)}
}

func (e *Error) Error() string {
	s := string(e.Code)
	if e.Msg != "" {
		s += ": " + e.Msg
	}
	if e.Cause != nil {
		s += ": " + e.Cause.Error()
	}
	return s
}

func (e *Error) Unwrap() error { return e.Cause }

// Is 按错误码匹配，忽略 Msg 和 Cause。
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t != nil && t.Code == e.Code
}

// CodeOf 取出错误链中第一个带码错误的错误码。
func CodeOf(err error) (Code, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e.Code, true
	}
	return "", false
}
