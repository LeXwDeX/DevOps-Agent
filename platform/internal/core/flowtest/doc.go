// Package flowtest 只有测试：把 coretest 的替身、检验流水线（core/pipeline）、
// 编排引擎（core/orchestrator）和恢复即停止（core/recovery）装配在一起，
// 按 61 第 7.3、7.4、8.1 节走完整条既定任务的运行：每一步的申请经检验、冻结、
// 执行前核验、执行、对比，再由引擎按分支推进；中途需要人工、目标变化或服务重启时，
// 运行按规则停下。本包没有运行时代码。
package flowtest
