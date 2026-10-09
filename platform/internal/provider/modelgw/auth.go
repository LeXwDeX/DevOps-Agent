package modelgw

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

// StaticTokenAuth 返回校验 "Authorization: Bearer <token>" 的函数，
// 常量时间比对。token 为空时永远不通过。
func StaticTokenAuth(token string) func(*http.Request) bool {
	if token == "" {
		return func(*http.Request) bool { return false }
	}
	want := sha256.Sum256([]byte(token))
	return func(r *http.Request) bool {
		h := r.Header.Get("Authorization")
		scheme, got, ok := strings.Cut(h, " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") || got == "" {
			return false
		}
		// 先取摘要再比对，长度也不泄露
		sum := sha256.Sum256([]byte(got))
		return subtle.ConstantTimeCompare(want[:], sum[:]) == 1
	}
}
