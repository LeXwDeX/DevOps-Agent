// Package migrations 用 embed 提供三个库的迁移文件（62 第 3.6 节）。
// 迁移文件是审核过的 SQL 文件，按文件名顺序执行；服务启动时不执行迁移。
// 本包只提供文件，执行由 cmd/migrate（受控执行）和 pgtest（测试）负责。
package migrations

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
)

//go:embed harness/*.sql config/*.sql audit/*.sql
var files embed.FS

// 库名。
const (
	Harness = "harness"
	Config  = "config"
	Audit   = "audit"
)

// File 是一个迁移文件。
type File struct {
	Name string // 文件名，例如 0001_init.sql
	SQL  string
}

// Databases 返回全部库名。
func Databases() []string { return []string{Harness, Config, Audit} }

// Files 返回指定库的迁移文件，按文件名排序。库名未知返回错误。
func Files(db string) ([]File, error) {
	switch db {
	case Harness, Config, Audit:
	default:
		return nil, fmt.Errorf("migrations: unknown database %q", db)
	}
	entries, err := fs.ReadDir(files, db)
	if err != nil {
		return nil, fmt.Errorf("migrations: read %s: %w", db, err)
	}
	out := make([]File, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := fs.ReadFile(files, db+"/"+e.Name())
		if err != nil {
			return nil, fmt.Errorf("migrations: read %s/%s: %w", db, e.Name(), err)
		}
		out = append(out, File{Name: e.Name(), SQL: string(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
