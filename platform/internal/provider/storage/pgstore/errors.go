package pgstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

// PostgreSQL 错误码和约束名（迁移文件里的命名）。
const (
	pgUniqueViolation = "23505"
	pgCheckViolation  = "23514"
	pgFKViolation     = "23503"

	conRequestsPkey      = "requests_pkey"
	conTaskRunsPkey      = "task_runs_pkey"
	conOutboxEventID     = "outbox_event_id_key"
	conOutboxStreamSeq   = "outbox_stream_sequence_key"
	conDefinitionsPkey   = "task_definitions_pkey"
	conActiveDefinitionK = "task_active_definition_fkey"
)

// pgError 取出错误链里的 PostgreSQL 错误。
func pgError(err error) *pgconn.PgError {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe
	}
	return nil
}

// isConstraint 判断 err 是否是指定错误码和约束名的违规。
func isConstraint(err error, code string, constraints ...string) bool {
	pe := pgError(err)
	if pe == nil || pe.Code != code {
		return false
	}
	for _, c := range constraints {
		if pe.ConstraintName == c {
			return true
		}
	}
	return false
}

// mapErr 把底层错误转成端口约定的错误：
//   - 已是带码错误，原样返回；
//   - 调用方取消或超时，返回 context 错误（保留 errors.Is）；
//   - 数据异常（类 22）和 CHECK 违规，说明调用方给了存不下的数据：core.invalid_argument；
//   - 其他一律 storage.unavailable，保留原因。
func mapErr(ctx context.Context, op string, err error) error {
	if err == nil {
		return nil
	}
	var coded *errcode.Error
	if errors.As(err, &coded) {
		return err
	}
	if cerr := ctx.Err(); cerr != nil {
		return fmt.Errorf("%s: %w", op, cerr)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", op, err)
	}
	if pe := pgError(err); pe != nil {
		if strings.HasPrefix(pe.Code, "22") || pe.Code == pgCheckViolation {
			return errcode.Wrap(errcode.InvalidArgument, op+": value cannot be stored", err)
		}
	}
	return errcode.Wrap(errcode.StorageUnavailable, op, err)
}

// withTx 在一个 READ COMMITTED 事务里执行 fn：fn 返回错误则回滚，否则提交。
// fn 返回的错误应已经是端口错误（用 mapErr 转换过）。
func withTx(ctx context.Context, pool *pgxpool.Pool, op string, fn func(tx pgx.Tx) error) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return mapErr(ctx, op, err)
	}
	rollback := func() {
		// 调用方的 context 可能已取消，回滚要另给一个有时限的 context。
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(rctx)
	}
	if err := fn(tx); err != nil {
		rollback()
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		rollback()
		return mapErr(ctx, op, err)
	}
	return nil
}

func decodeFailed(what string, cause error) error {
	return errcode.Wrap(errcode.StorageDecodeFailed, what, cause)
}
