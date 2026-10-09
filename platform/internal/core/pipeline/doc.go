// Package pipeline 是检验流水线（61 第 5 节）：提交、检验、修订、人工审核、取消、
// 执行前核验、执行和执行后对比。
//
// 约定：
//   - 状态转换只经 core/request、core/review 的纯函数，策略只经 core/policy，不重复实现。
//   - 每次状态保存都把该转换的事件在同一次 RequestStore.Create/Save 里交给存储端口（61 第 8.2 节），
//     保存时 r.Version 已设为 expected.Next()（CAS）。
//   - 事件 Data 只放摘要、错误码、引用和原因，不放 Rendered、Params 的值或执行输出。
//   - 返回的错误都带 errcode 错误码。端口返回的、没有专门映射的错误原样向上返回，状态保持不变，不猜。
package pipeline
