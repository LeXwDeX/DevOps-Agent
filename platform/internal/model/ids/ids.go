// Package ids 定义不可变值对象：CallID、Revision、Digest、Version。
package ids

import (
	"regexp"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

// CallID 是一次调用的稳定编号；修订只加 Revision，不换 CallID（K-13）。
type CallID string

func (c CallID) Valid() bool { return c != "" }

// Revision 是修订号，从 1 开始。
type Revision int

// FirstRevision 是首个修订号。
const FirstRevision Revision = 1

func (r Revision) Valid() bool { return r >= FirstRevision }

// Next 返回下一个修订号。
func (r Revision) Next() Revision { return r + 1 }

// Version 是乐观锁版本号，每次状态转换加一，存储端口用 CAS 提交。
type Version int64

func (v Version) Next() Version { return v + 1 }

// Digest 是内容摘要：小写十六进制的 SHA-256。
type Digest string

var digestRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (d Digest) Valid() bool { return digestRe.MatchString(string(d)) }

func ParseDigest(s string) (Digest, error) {
	d := Digest(s)
	if !d.Valid() {
		return "", errcode.Newf(errcode.InvalidArgument, "invalid digest %q", s)
	}
	return d, nil
}
