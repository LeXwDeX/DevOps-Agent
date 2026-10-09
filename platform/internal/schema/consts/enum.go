// Package consts 集中定义状态、策略模式等类型化枚举。
// 取值与 docs/50-engineering/52-schema-and-constants.md 第 4 节一致，由文档比对测试守住。
// 未知值一律报错，不落到任何放行值（52 第 4.2 节）。
package consts

import (
	"slices"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

func contains[T ~string](all []T, v T) bool { return slices.Contains(all, v) }

func parse[T ~string](kind string, all []T, s string) (T, error) {
	v := T(s)
	if contains(all, v) {
		return v, nil
	}
	var zero T
	return zero, errcode.Newf(errcode.InvalidArgument, "unknown %s %q", kind, s)
}
