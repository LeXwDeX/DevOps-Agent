package pgstore

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/ports"
	schema "github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/core"
)

// AuditStore 在 audit 库上实现 ports.AuditSink。审计记录只追加：库里的触发器拒绝 UPDATE、DELETE 和 TRUNCATE。
type AuditStore struct {
	pool *pgxpool.Pool
}

var _ ports.AuditSink = (*AuditStore)(nil)

// NewAuditStore 返回审计存储。连接池由调用方创建和关闭。
func NewAuditStore(pool *pgxpool.Pool) *AuditStore { return &AuditStore{pool: pool} }

// Append 单条插入一条审计记录。
func (s *AuditStore) Append(ctx context.Context, rec ports.AuditRecord) error {
	raw, err := encodeDoc("audit record", auditToDoc(rec), auditFromDoc)
	if err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, sqlAuditInsert,
		ut(rec.At), rec.Actor, rec.Action, string(rec.CallID), int32(rec.Revision), rec.RunID, schema.AuditRecordV1, raw); err != nil {
		return mapErr(ctx, "append audit record", err)
	}
	return nil
}
