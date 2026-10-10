// Package pgstore 是 core 存储端口的 PostgreSQL 实现（62 第 3 节）：
//   - HarnessStore：RequestStore、RevisionLog、EventSink，以及 outbox 的读取（PendingEvents、MarkDelivered）。
//   - TaskStore：TaskStore（定义在 config 库，运行在 harness 库）。
//   - AuditStore：AuditSink（audit 库，只追加）。
//
// 约定：
//   - 全部 SQL 是 queries.go 里的常量，不拼接 SQL（K-23）；测试对清单里的每条做 PREPARE。
//   - 主表关键字段建列，完整内容放 JSONB 文档（schema/core 的 *DocV1）；model 与文档的转换在 convert.go。
//   - 读取按严格模式解码：未知 schema_id、未知字段、非法枚举返回 storage.decode_failed。
//   - 状态和事件在同一事务里提交（READ COMMITTED）；CAS 由带版本条件的 UPDATE 保证。
//   - 连接和执行失败返回 storage.unavailable（保留原因）；调用方取消 context 时返回 context 错误。
package pgstore
