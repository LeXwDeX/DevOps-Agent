// Package core 定义对外契约的 SchemaID 常量。
// 对外契约结构在有边界的块里加入（61 第 2 节）。
package core

// SchemaID 是契约的稳定标识，带版本后缀。
const (
	ExecutionRequestV1 = "devops.execution.request/v1"
	TaskRunV1          = "devops.task.run/v1"
	EventV1            = "devops.event/v1"
)
