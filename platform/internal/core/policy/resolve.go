// Package policy 实现策略解析 Resolve 和转人工判断 Escalate（61 第 6 节）。
// 两者都是纯函数：同样的输入永远得到同样的输出，不做 I/O。
package policy

import (
	"fmt"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/policy"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/consts"
)

// ZoneOf 把读写类别映射到权限区：read_only → read，其余（含 unknown）→ write。
func ZoneOf(c consts.Category) consts.Zone {
	if c == consts.CategoryReadOnly {
		return consts.ZoneRead
	}
	return consts.ZoneWrite
}

// matches 判断规则的匹配键和环境是否与查询相符。
func matches(r policy.Rule, q policy.Query) bool {
	if r.Environment != "" && r.Environment != q.Environment {
		return false
	}
	switch r.Layer {
	case consts.LayerAction:
		return r.Subject == q.Subject && r.Action == q.Action
	case consts.LayerZone:
		return r.Subject == q.Subject && r.Zone == q.Zone
	case consts.LayerGlobal:
		return r.Subject == q.Subject
	case consts.LayerSystem:
		return true
	}
	return false
}

// ordered 按优先级返回匹配的规则：从细到粗的层，层内当前环境的规则在不分环境的规则之前，
// 同级保持登记顺序。
func ordered(set policy.Set, q policy.Query) []policy.Rule {
	var out []policy.Rule
	for _, layer := range consts.PolicyLayers() {
		for _, envSpecific := range []bool{true, false} {
			for _, r := range set.Rules {
				if r.Layer == layer && (r.Environment != "") == envSpecific && matches(r, q) {
					out = append(out, r)
				}
			}
		}
	}
	return out
}

func describe(r policy.Rule) string {
	s := string(r.Layer) + " 层"
	switch r.Layer {
	case consts.LayerAction:
		s += fmt.Sprintf("（%s / %s）", r.Subject, r.Action)
	case consts.LayerZone:
		s += fmt.Sprintf("（%s / %s 区）", r.Subject, r.Zone)
	case consts.LayerGlobal:
		s += fmt.Sprintf("（%s）", r.Subject)
	}
	if r.Environment != "" {
		s += "，环境 " + r.Environment
	}
	return s
}

// Resolve 按 61 第 6.2 节解析策略：
//
//  1. 从细到粗依次查找（具体功能 → 权限区 → 全局 → 系统兜底），层内先当前环境、后不分环境。
//     第一个带 Mode 的规则给出 Mode 和命中层。
//  2. 所有匹配规则中任何一条 forbid，结果为 forbid（禁止优先于一切），命中层记为该规则所在层。
//  3. 所有匹配规则中任何一条 allow_auto:false，AllowAuto 为 false，auto 改为 manual。
//  4. 会话不是自动模式时，auto 改为 manual；来源为 pre_authorized 除外。
//     会话模式为空或未知值时按手工处理，不落到放行值。
//  5. 没有任何规则给出 Mode：系统兜底 manual，命中层记为 system。
//
// Reasons 是给人看的中文说明。
func Resolve(set policy.Set, q policy.Query) policy.Decision {
	d := policy.Decision{AllowAuto: true, PolicyVersion: set.Version}
	rules := ordered(set, q)

	var forbid *policy.Rule
	found := false
	for i, r := range rules {
		if r.Mode != nil && !found {
			found = true
			d.Mode, d.Layer = *r.Mode, r.Layer
			d.Reasons = append(d.Reasons, fmt.Sprintf("策略命中 %s：%s", describe(r), *r.Mode))
		}
		if r.Mode != nil && *r.Mode == consts.PolicyForbid && forbid == nil {
			forbid = &rules[i]
		}
		if r.AllowAuto != nil && !*r.AllowAuto {
			if d.AllowAuto {
				d.Reasons = append(d.Reasons, fmt.Sprintf("%s 设置了不允许自动", describe(r)))
			}
			d.AllowAuto = false
		}
	}

	if !found {
		d.Mode, d.Layer = consts.PolicyManual, consts.LayerSystem
		d.Reasons = append(d.Reasons, "没有规则给出模式，系统兜底：manual")
	}
	if forbid != nil {
		if d.Mode != consts.PolicyForbid {
			d.Reasons = append(d.Reasons, fmt.Sprintf("%s 禁止，禁止优先", describe(*forbid)))
		}
		d.Mode, d.Layer = consts.PolicyForbid, forbid.Layer
		return d
	}
	if d.Mode == consts.PolicyAuto && !d.AllowAuto {
		d.Mode = consts.PolicyManual
		d.Reasons = append(d.Reasons, "allow_auto 为 false，自动改为人工")
	}
	if d.Mode == consts.PolicyAuto && q.SessionMode != consts.SessionAuto && q.Source != consts.SourcePreAuthorized {
		d.Mode = consts.PolicyManual
		d.Reasons = append(d.Reasons, "会话为手工模式，自动改为人工")
	}
	return d
}
