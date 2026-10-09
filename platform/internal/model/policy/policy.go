// Package policy 定义策略规则、策略集和解析的输入输出（22 第 4 节，61 第 6 节）。
// 解析算法在 core/policy，这里只放数据和结构校验。
package policy

import (
	"fmt"
	"strings"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/consts"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

// Rule 是一条策略规则。字段是否出现由层级决定：
//   - action 层：Subject + Action
//   - zone 层：Subject + Zone
//   - global 层：Subject
//   - system 层：不带键
type Rule struct {
	Layer       consts.PolicyLayer `json:"layer"`
	Subject     string             `json:"subject,omitempty"`     // provider 或工具键，system 层为空
	Action      string             `json:"action,omitempty"`      // 只用于 action 层
	Zone        consts.Zone        `json:"zone,omitempty"`        // 只用于 zone 层
	Environment string             `json:"environment,omitempty"` // 空 = 不分环境
	Mode        *consts.PolicyMode `json:"mode,omitempty"`        // nil = 不给出模式，只参与 allow_auto
	AllowAuto   *bool              `json:"allow_auto,omitempty"`
}

// Set 是已登记的全部策略规则及其版本。
type Set struct {
	Version string `json:"version"`
	Rules   []Rule `json:"rules"`
}

// Clone 深拷贝策略集（规则里的指针也复制）。
func (s Set) Clone() Set {
	if s.Rules == nil {
		return s
	}
	rules := make([]Rule, len(s.Rules))
	for i, r := range s.Rules {
		if r.Mode != nil {
			m := *r.Mode
			r.Mode = &m
		}
		if r.AllowAuto != nil {
			b := *r.AllowAuto
			r.AllowAuto = &b
		}
		rules[i] = r
	}
	s.Rules = rules
	return s
}

// ModePtr 返回 m 的指针，便于构造规则。
func ModePtr(m consts.PolicyMode) *consts.PolicyMode { return &m }

// BoolPtr 返回 b 的指针，便于构造规则。
func BoolPtr(b bool) *bool { return &b }

// Validate 检查规则的字段与层级匹配，枚举取值合法。
func (r Rule) Validate() error {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	if !r.Layer.Valid() {
		add("unknown layer %q", r.Layer)
	}
	if r.Zone != "" && !r.Zone.Valid() {
		add("unknown zone %q", r.Zone)
	}
	if r.Mode != nil && !r.Mode.Valid() {
		add("unknown mode %q", *r.Mode)
	}
	switch r.Layer {
	case consts.LayerAction:
		if r.Subject == "" {
			add("action layer needs subject")
		}
		if r.Action == "" {
			add("action layer needs action")
		}
		if r.Zone != "" {
			add("action layer must not set zone")
		}
	case consts.LayerZone:
		if r.Subject == "" {
			add("zone layer needs subject")
		}
		if r.Zone == "" {
			add("zone layer needs zone")
		}
		if r.Action != "" {
			add("zone layer must not set action")
		}
	case consts.LayerGlobal:
		if r.Subject == "" {
			add("global layer needs subject")
		}
		if r.Action != "" || r.Zone != "" {
			add("global layer must not set action or zone")
		}
	case consts.LayerSystem:
		if r.Subject != "" || r.Action != "" || r.Zone != "" {
			add("system layer must not set subject, action or zone")
		}
		if r.Mode == nil && r.AllowAuto == nil {
			add("system rule needs mode or allow_auto")
		}
	}
	if len(problems) > 0 {
		return errcode.New(errcode.InvalidArgument, "policy rule: "+strings.Join(problems, "; "))
	}
	return nil
}

// Validate 检查整个策略集。
func (s Set) Validate() error {
	for i, r := range s.Rules {
		if err := r.Validate(); err != nil {
			return errcode.Wrap(errcode.InvalidArgument, fmt.Sprintf("rule #%d", i), err)
		}
	}
	return nil
}

// Query 是解析输入：请求的 provider 或工具、功能、权限区、环境、会话模式和来源。
type Query struct {
	Subject     string
	Action      string
	Zone        consts.Zone
	Environment string
	SessionMode consts.SessionMode
	Source      consts.RequestSource
}

// Decision 是解析输出。
type Decision struct {
	Mode          consts.PolicyMode  `json:"mode"`
	Layer         consts.PolicyLayer `json:"layer"`      // 给出 Mode 的层
	AllowAuto     bool               `json:"allow_auto"` // 任一匹配规则 allow_auto:false 时为 false
	PolicyVersion string             `json:"policy_version"`
	Reasons       []string           `json:"reasons,omitempty"`
}

// Clone 深拷贝。
func (d Decision) Clone() Decision {
	d.Reasons = append([]string(nil), d.Reasons...)
	return d
}
