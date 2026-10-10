package protocol

import "fmt"

// SDK 自持的错误码。SDK 不依赖平台 errcode 包。
const (
	CodeOutputTruncated  = "goagent.model.output_truncated"
	CodeBadRequest       = "goagent.model.bad_request"
	CodeBadResponse      = "goagent.model.bad_response"
	CodeTooLarge         = "goagent.model.too_large"
	CodeStreamIncomplete = "goagent.model.stream_incomplete"
)

// 哨兵错误。用 errors.Is(err, protocol.ErrBadResponse) 按错误码匹配。
var (
	ErrOutputTruncated  = &Error{Code: CodeOutputTruncated}
	ErrBadRequest       = &Error{Code: CodeBadRequest}
	ErrBadResponse      = &Error{Code: CodeBadResponse}
	ErrTooLarge         = &Error{Code: CodeTooLarge}
	ErrStreamIncomplete = &Error{Code: CodeStreamIncomplete}
)

// Error 是协议层返回的错误。Msg 只描述结构问题，不回显回复内容。
type Error struct {
	Code string
	Msg  string
}

// NewError 构造一个带错误码的 Error。派生适配器的钩子用它报告问题。
func NewError(code, format string, args ...any) *Error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, args...)}
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Msg == "" {
		return e.Code
	}
	return e.Code + ": " + e.Msg
}

// Is 按错误码比较，所以 errors.Is(err, ErrBadResponse) 对任何同码的 *Error 成立。
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && e != nil && t != nil && e.Code == t.Code
}

// asError 把任意错误统一成 *Error；已经是 *Error 的原样返回。
func asError(err error, code string) *Error {
	if err == nil {
		return nil
	}
	if e, ok := err.(*Error); ok {
		return e
	}
	return &Error{Code: code, Msg: "extension hook failed"}
}
