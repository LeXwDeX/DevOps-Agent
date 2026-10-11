// Package modelgw 是模型出口代理：读取模型接入变量，限制出口端点，
// 注入 API key，并按 key 的精确值 mask 所有返回内容。
// 设计见 docs/60-design/62-provider-storage-modelgw.md 第 5 节。只用标准库。
package modelgw

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

// 模型接入变量（环境变量表，K-31）。程序不读表外的变量。
const (
	EnvBaseURL = "DEVOPS_LLM_BASE_URL"
	EnvAPIKey  = "DEVOPS_LLM_API_KEY"
)

// MaskText 是 key 的替换文本（K-17）。
const MaskText = "[MASKED:" + EnvAPIKey + "]"

// 可用性状态。
const (
	StateAvailable = "available"
	StateMissing   = "missing"
	StateInvalid   = "invalid"
)

// Status 是模型可用性。Reason 只写变量名和规则，不写值。
type Status struct {
	State  string `json:"status"`
	Reason string `json:"reason"`
}

// varState 的零值是缺失。
type varState uint8

const (
	varMissing varState = iota
	varInvalid
	varOK
)

// Config 是校验后的模型接入配置。key 和地址都不对外暴露：
// 没有导出字段，格式化与 JSON 序列化一律遮盖。
type Config struct {
	urlState varState
	urlWhy   string // 固定规则文本，不含值
	target   string // base + /chat/completions，仅 OK 时有值

	keyState varState
	keyWhy   string
	key      string // 只要非空就保存，用于 mask，即使校验未通过
}

// LoadConfig 只读 DEVOPS_LLM_BASE_URL 和 DEVOPS_LLM_API_KEY。
func LoadConfig(lookup func(string) (string, bool)) Config {
	var c Config
	if lookup == nil {
		return c
	}
	if raw, ok := lookup(EnvBaseURL); ok && raw != "" {
		c.target, c.urlWhy = parseBaseURL(raw)
		if c.urlWhy == "" {
			c.urlState = varOK
		} else {
			c.urlState = varInvalid
			c.target = ""
		}
	}
	if raw, ok := lookup(EnvAPIKey); ok && raw != "" {
		c.key = raw
		if why := checkKey(raw); why != "" {
			c.keyState, c.keyWhy = varInvalid, why
		} else {
			c.keyState = varOK
		}
	}
	return c
}

// parseBaseURL 返回目标地址；失败时 why 是固定的规则文本。
func parseBaseURL(raw string) (target, why string) {
	for _, r := range raw {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return "", "must not contain whitespace or control characters"
		}
	}
	// 裸的 ? 和 # 也不允许，url.Parse 会把它们吞掉
	if strings.ContainsAny(raw, "?#") {
		return "", "must not contain a query or fragment"
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", "must be a valid URL"
	}
	if u.Scheme != "https" {
		return "", "scheme must be https"
	}
	if u.Opaque != "" || u.Hostname() == "" {
		return "", "host is required"
	}
	if u.User != nil {
		return "", "must not contain user info"
	}
	if port := u.Port(); port != "" || strings.HasSuffix(u.Host, ":") {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil || n == 0 {
			return "", "explicit port must be a decimal number from 1 to 65535"
		}
	}
	esc := strings.TrimRight(u.EscapedPath(), "/")
	dec := strings.TrimRight(u.Path, "/")
	if hasChatSuffix(esc) || hasChatSuffix(dec) {
		return "", "path must not end with /chat/completions"
	}
	return "https://" + u.Host + esc + "/chat/completions", ""
}

func hasChatSuffix(p string) bool {
	return strings.HasSuffix(strings.ToLower(p), "/chat/completions")
}

func checkKey(raw string) string {
	for _, r := range raw {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return "must not contain whitespace or control characters"
		}
	}
	return ""
}

// Available 表示两个变量都通过检查。
func (c Config) Available() bool {
	return c.urlState == varOK && c.keyState == varOK
}

// Status 返回可用性。原因里只有变量名和固定规则文本，不含值。
func (c Config) Status() Status {
	if c.Available() {
		return Status{State: StateAvailable}
	}
	var parts []string
	state := StateMissing
	add := func(name string, s varState, why string) {
		switch s {
		case varMissing:
			parts = append(parts, name+" is missing")
		case varInvalid:
			state = StateInvalid
			parts = append(parts, name+" is invalid: "+why)
		}
	}
	add(EnvBaseURL, c.urlState, c.urlWhy)
	add(EnvAPIKey, c.keyState, c.keyWhy)
	return Status{State: state, Reason: newMasker(c.key).str(strings.Join(parts, "; "))}
}

// 下面几个方法保证 key 不会经格式化或序列化泄露。

func (c Config) redacted() string {
	return newMasker(c.key).str("modelgw.Config{status:" + c.Status().State + ", secrets:redacted}")
}

// String 返回遮盖后的描述。
func (c Config) String() string { return c.redacted() }

// GoString 返回遮盖后的描述。
func (c Config) GoString() string { return c.redacted() }

// Format 对所有动词输出遮盖后的描述。
func (c Config) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(c.redacted())) }

// MarshalJSON 只输出状态。
func (c Config) MarshalJSON() ([]byte, error) { return json.Marshal(c.Status()) }

// MarshalText 只输出状态文本。
func (c Config) MarshalText() ([]byte, error) { return []byte(c.redacted()), nil }

// ForbidInCore 检查 core 容器的环境（"K=V" 列表）。
// 含任一模型接入变量就返回 config.forbidden_env，错误信息只写变量名。
func ForbidInCore(environ []string) error {
	found := map[string]bool{}
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		if name == EnvBaseURL || name == EnvAPIKey {
			found[name] = true
		}
	}
	if len(found) == 0 {
		return nil
	}
	names := make([]string, 0, len(found))
	for n := range found {
		names = append(names, n)
	}
	sort.Strings(names)
	return errcode.New(errcode.ConfigForbiddenEnv,
		"core container must not receive: "+strings.Join(names, ", "))
}
