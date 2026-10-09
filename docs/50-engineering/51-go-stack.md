---
id: GO-STACK
title: Go 技术选型与组件准入
status: 已定（候选未经 PoC；Q11、Q15 待定）
summary: Go 工具链基线；按职责列出的依赖选型、使用边界和检验要求；第三方组件准入的统一标准（K-23）；P0 需要做的 PoC。
load_when: 搭建工程、引入或升级依赖、评估第三方组件（Temporal、Grafana、dcg 等）、选择解析器或云 SDK 时
depends_on: [KERNEL, COMPONENTS, SQL, SCHEMA-CONST, REPO-LAYOUT]
---

# Go 技术选型与组件准入

## 1. 原则

- 平台与 Agent 模块都用 Go。Agent 能力封装为独立的 GoAgent SDK（见 [SDK 概览](../40-sdk/41-sdk-overview.md)）。
- Go 负责固定规则、状态转换、接口契约、资源限制和受控执行。模型负责理解、问答、计划和自审。模型输出仍是待检验的输入（K-01）。
- 用 Go 不会让模型推理、远端网络或云 API 自动变得确定。确定性来自门禁、指纹和执行后对比。
- 下文区分**基线**和**候选**。候选都没有经过 PoC（小范围原型验证）。P0 记录证据和结论；没通过的能力保持关闭，不先接入再补门禁。
- 依赖只留在对应的适配层。厂商和框架的类型不进入业务规则层，也不进入 SDK 公共 API。

## 2. 工具链基线

工具链基线按 2026-10-05 的 Go 官方下载页核对。

| 项 | 基线 |
|---|---|
| 平台 Go 版本 | 固定 Go 1.27.1 |
| GoAgent SDK 最低版本 | Go 1.26；在 Go 1.26.8 与 Go 1.27.1 上复测 |
| 目标平台 | linux/amd64、linux/arm64 |
| CGO | 只在需要 SQL 解析的二进制中启用（pg_query_go，见第 3 节）。SDK 不引入 CGO 依赖。 |
| 构建输入 | 依赖版本、构建镜像和目标平台写入发布清单（见 [测试与发布](55-testing-and-release.md)）。不使用随时间变化的 `latest` 作为构建输入。 |
| 安全更新 | 经过复测后更新发布清单，再发布。 |

## 3. 选型表

| 职责 | 基线 / 候选 | 使用边界与检验 |
|---|---|---|
| 服务与生命周期 | 基线：标准库 `net/http`、`context`、`log/slog`。服务契约用版本化 HTTP/JSON。 | 每个请求有 deadline、取消、输入大小限制和有界并发。核心接口异步提交 Agent 任务，不等待模型调用完成。日志字段先 mask（K-17）。确有契约和性能需要时才加 gRPC，不同时维护两套等价接口。 |
| 业务状态 | 基线：PostgreSQL；驱动 `pgx/v5`；固定 SQL 用 `sqlc` 生成类型化接口。 | 驱动和生成代码只在存储适配层。平台自身的固定 SQL 在构建期分析和测试，随版本发布（K-23）。平台存储适配层不向 Agent 暴露任意 SQL 的能力。 |
| 任务队列（核心 ↔ Worker） | 本项目定义的版本化协议，持久保存（见 [组件与边界](../10-architecture/11-components-and-boundaries.md) 第 4 节）。 | 具体实现 P0 定。不为它引入额外中间件；确需引入时走组件准入（第 4 节）。 |
| 数据库迁移 | 基线：审核后的版本化 SQL 文件。候选：`goose`，通过兼容检验后才可用。 | 迁移文件在构建期分析。迁移的执行是一次受控执行，由底座发起（见 [部署底座](../10-architecture/12-deployment-substrate.md) 第 6.3 节）。服务启动时不自动迁移。不用 Go 回调迁移隐藏动态 SQL。运行账号与迁移账号分开。 |
| PostgreSQL 方言解析 | 候选：`pg_query_go`，使用 PostgreSQL 自身的解析器源码。 | 依赖 CGO。P0 验证目标数据库主版本、解析器版本、amd64/arm64 构建和镜像运行。语法树只说明结构，不能证明权限、影响范围或数据安全。解析失败或不支持的语法转人工（Q7）。 |
| MySQL 系方言解析 | 候选：TiDB parser（纯 Go）。 | MySQL 兼容不等于每个 MySQL、TiDB 或云派生产品完全等价。按产品、确切版本、SQL mode 和语句特性建立覆盖矩阵。不支持、多语句漏解析、动态 SQL 未展开或分析不完整，都按"读写类别无法确定"转人工（K-08、Q7）。每个派生数据库类型用独立的 Go 文件（见 [云服务商](../30-features/35-cloud-providers.md) 第 9、11 节）。 |
| 既定任务编排 | 基线：平台自带的限域状态机，通过平台的 `WorkflowPort` 隔离编排实现。 | 只运行已登记的任务定义：固定步骤、类型化分支和兜底分支（见 [既定任务](../20-execution/23-harness-tasks.md)）。不开放任意流程图编排。"开始执行"先落盘再执行；状态变更用 CAS；迟到的结果不覆盖较新的记录；结果未知先查询实际状态，不盲目重做。恢复即停止（K-24），不做租约续跑或断点自动恢复。 |
| 长流程编排器 | 候选：Temporal（Q15）。 | 按第 4 节组件准入、部署成本和恢复要求评估，首版不采用。评估要点见第 5 节。 |
| 时间表达式 | 基线：`robfig/cron/v3`，只用于表达式和时区解析。 | 计划和周期状态由持久调度服务保存，内存计时器不能作为唯一来源。每次触发绑定项目、任务版本和唯一周期键。明确时区、夏令时、错过时刻、重叠和补跑规则。恢复后计划暂停（Q4）。每次运行仍逐步经过门禁。 |
| Docker | 基线：官方 Moby Go client，模块路径 `github.com/moby/moby/client`。 | 两处使用，权限域分开：底座的节点代理管理平台自身容器；执行器调用目标主机的 Docker Engine 操作项目容器（见 [工具包装](../20-execution/26-tool-wrapping.md) 第 3 节）。两处都固定 Docker API 版本。 |
| 云厂商 | 基线：Azure、阿里云、腾讯云各自的官方 Go SDK。 | 只在云适配器中使用。每个产品适配器固定依赖和 API 版本。能力矩阵先声明原生预演（DryRun）、幂等、状态查询和失败恢复。厂商返回任务 ID 不等于执行成功（K-05）。厂商 SDK 不提供本系统的门禁。见 [云服务商](../30-features/35-cloud-providers.md)。 |
| Agent 框架 | 候选：Eino、Google ADK Go，作为模型与编排适配候选。SDK 公共契约由本项目自己定义。 | P0 检验工具回调、并行调用、子 Agent、流式和恢复路径。任何框架自带的执行器都不能直连 shell、SQL 或云资源，工具请求一律走统一申请端口（见 [SDK 工具目录](../40-sdk/44-tool-catalog.md)）。不满足就不采用。不同时引入两套执行框架。框架类型不暴露为 SDK 公共 API。 |
| 指标 | 基线：Prometheus Go client。 | 平台只暴露受控指标。敏感值不进入标签。 |
| 日志 | 基线：`log/slog`。 | 结构化字段先 mask，再输出。 |
| 调用链 | 候选：OpenTelemetry Go，按需采用。 | SDK 注入观测接口，不创建全局 exporter。敏感参数不进入 span 属性。首版不依赖还需另行检验的日志信号管线。 |
| 监控旁路 | 独立存储、计算和通知（K-25）。 | 采集组件和自研模块见 [监控](../30-features/36-monitoring.md)。第三方监控组件走组件准入。 |
| 外部命令守卫 | [destructive_command_guard（dcg）](https://github.com/Dicklesworthstone/destructive_command_guard)，Rust 独立二进制，打进 runtime 镜像。 | 不调用模型。二进制和规则包固定版本，由管理员登记（K-20），走组件准入。用法见 [审核与策略](../20-execution/22-review-and-policy.md) 第 2.2 节。P0 核实许可证、机器可读调用方式和 SQL 覆盖（Q11）。 |
| Schema 层 | 直接用 Go 编写常量、配置和契约结构，不依赖代码生成（见 [Schema 与常量](52-schema-and-constants.md) 第 6.1 节）。 | 一项一个定义源；文档枚举与代码常量在 CI 比对。 |
| 测试与依赖检查 | 基线：race detector、Go fuzzing、`govulncheck`、Testcontainers Go。 | race 只能发现已运行路径上的竞态。fuzz 检验解析和输入边界。真实服务检验 SQL 方言、事务和恢复。测试结果、依赖许可和维护状态写入发布记录。缺少关键测试时，不能宣称该能力可用。见 [测试与发布](55-testing-and-release.md)。 |

## 4. 组件准入（K-23）

第三方组件（例如 Temporal、Grafana、Prometheus、Alertmanager、数据库 exporter、现成的 dcg 实现）用同一套标准准入。准入是一次性的审核，替代对组件内部存储 SQL 的逐次检验。系统经执行路径对项目目标库发出的 SQL 不在此列，仍然每次检验（见 [SQL](../20-execution/24-sql.md) 第 1 节）。数据库 exporter 对目标库的固定只读查询按组件准入处理，另加只读专用账号和查询集审核等条件（见 [监控](../30-features/36-monitoring.md) 第 3.1 节）。

### 4.1 准入标准

| 方面 | 必须满足 |
|---|---|
| 版本 | 固定版本和镜像或二进制摘要。不用 `latest`。 |
| 来源与许可 | 官方来源；许可证与本项目兼容；维护状态可接受。 |
| 安全 | 漏洞扫描结果可接受；已知漏洞有处理结论。 |
| 存储 | 独立的库和账号，最小权限。组件存储里不能有 V，只能有 K 引用。 |
| 内部 SQL | 不逐条检验。准入时记录其初始化和迁移方式。升级迁移作为一次受控执行（先备份，执行预期写明版本变化）。 |
| 凭证 | 经 K→V 注入（K-16）。组件日志按 mask 规则处理（K-17）。 |
| 网络 | 入站和出站范围明确，按受控环境模板配置（见 [部署底座](../10-architecture/12-deployment-substrate.md) 第 8 节）。 |
| 不成为旁路 | 组件不能直接操作项目目标。任何对目标的操作仍经 Harness 和执行器（K-18）。 |
| 恢复 | 与恢复即停止一致（K-24）。组件自身的自动重试、重放或续跑不能驱动外部操作；平台恢复后，组件里的在途运行停止。 |
| 观测 | 健康、指标和日志可接入；组件故障不影响监控旁路（K-25）。 |
| 退出 | 可以停用和替换；数据可以导出。 |

### 4.2 准入流程

1. 提出准入申请：组件、版本、用途、替代方案。
2. 按 4.1 逐项给出证据；需要时做 PoC。
3. 管理员审核后登记，固定版本和摘要（K-20）。
4. 升级组件即新版本，重新准入（K-21）。升级本身是一次受控执行。

### 4.3 当前组件

| 组件 | 用途 | 状态 |
|---|---|---|
| PostgreSQL | 平台状态基线 | 基线。平台自身 SQL 构建期分析。 |
| Prometheus、Alertmanager、Grafana | 没有现成指标系统时的监控旁路默认组合 | 按 4.1 准入。见 [监控](../30-features/36-monitoring.md) 第 2 节。 |
| node_exporter、cAdvisor、blackbox_exporter、数据库 exporter | 监控采集 | 按 4.1 准入；数据库 exporter 另见 [监控](../30-features/36-monitoring.md) 第 3.1 节。 |
| Temporal | 长流程编排 | 候选（Q15），见第 5 节。 |
| dcg | 外部命令守卫 | 已选定 destructive_command_guard；P0 核实许可证与调用方式（Q11）。 |

## 5. Temporal 评估要点（Q15）

Temporal 提供持久工作流和独立的 Temporal Worker，适合长期等待和故障恢复。它的 Server 使用独立持久层。按第 4 节，它的内部 SQL 走组件准入，不再逐次检验。评估时还要看：

| 要点 | 说明 |
|---|---|
| 与恢复即停止的关系 | Temporal 的主要价值是自动续跑，而 K-24 规定恢复后在途任务停止。采用时必须配置成：平台恢复后终止或暂停在途运行，由人工决定是否重新执行。这会削弱它的收益。 |
| 外部副作用 | Workflow 重放和 Activity 重试不保证外部系统只发生一次副作用。仍由执行器查询实际结果、保证幂等。 |
| 密钥 | V 不能进入参数、Signal、结果、错误、心跳或历史，只保存 K 和 mask 后的引用。 |
| 状态来源 | 每个运行只绑定一个编排器。采用时 Temporal History 是该运行的唯一流程状态来源，核心只保存可重建、带新鲜度的查询投影。不能让两套存储同时推进同一流程。 |
| 版本 | 按 Worker Versioning 规则保留所有尚有在途运行的固定 Temporal Worker 版本，检验 Activity 与子流程的跨版本路由。 |
| 部署成本 | 额外的 Server、存储、升级和备份。 |
| SDK | GoAgent SDK 不依赖 Temporal。 |

当前默认：首版不采用，用平台自带的限域状态机。

## 6. P0 需要做的 PoC

| PoC | 要证明的事 |
|---|---|
| pg_query_go | 目标 PostgreSQL 主版本的覆盖；CGO 在 amd64/arm64 构建和镜像运行；多语句逐条覆盖。 |
| TiDB parser | 每个目标产品和版本的覆盖矩阵；SQL mode 差异；不支持时能明确判为"无法确定"。 |
| dcg 核实（Q11） | 规则覆盖（[审核与策略](../20-execution/22-review-and-policy.md) 第 2.2 节和 [SQL](../20-execution/24-sql.md) 第 5 节列出的类别）；能否追加项目规则；SQL 规则支持；固定版本与摘要；运行性能。 |
| Eino / Google ADK Go | 工具回调、并行调用、子 Agent、流式、取消和恢复路径都能接到统一申请端口；框架执行器无法绕过。 |
| Moby client | 节点代理的最小原型：按冻结申请创建、启动、停止容器，并采集指纹。 |
| 云 SDK | 每个首批产品的 DryRun 含义、幂等支持和状态查询方式（P4 只开放一个厂商）。 |
| Schema 导出 | 需要对外提供 JSON Schema 时，从 Go 定义导出，结果确定、可比对。 |

PoC 只在隔离环境运行，不接触真实项目资产。

## 7. 依赖管理

- `go.mod` 固定所有依赖版本，`go.sum` 入库。
- 发布构建不使用本地 `replace`，也不依赖 `go.work`（见 [仓库布局](53-repo-layout.md)）。
- SDK 的依赖尽量少，不依赖平台模块、云 SDK、数据库驱动或 Temporal。
- 引入新依赖时记录：用途、版本、许可、维护状态、漏洞扫描结果。
- 依赖升级按"变更即新版本"处理，并跑累积复测（K-29）。

## 8. 参考

- [Go 官方下载页](https://go.dev/dl/)
- [net/http](https://pkg.go.dev/net/http)、[context](https://pkg.go.dev/context)、[slog](https://pkg.go.dev/log/slog)
- [pgx](https://github.com/jackc/pgx)、[sqlc 语言支持](https://docs.sqlc.dev/en/latest/reference/language-support.html)、[goose](https://github.com/pressly/goose)
- [pg_query_go](https://github.com/pganalyze/pg_query_go)、[TiDB parser](https://github.com/pingcap/tidb/blob/master/pkg/parser/README.md)
- [Temporal](https://docs.temporal.io/temporal)、[Temporal 服务配置](https://docs.temporal.io/references/service-configuration)、[Workflow 定义](https://docs.temporal.io/workflow-definition)、[Worker Versioning](https://docs.temporal.io/worker-versioning)
- [robfig/cron/v3](https://pkg.go.dev/github.com/robfig/cron/v3)
- [Moby Go client](https://docs.docker.com/reference/api/engine/sdk/)
- [Azure Go SDK](https://learn.microsoft.com/en-us/azure/developer/go/overview)、[阿里云 SDK](https://github.com/aliyun/alibabacloud-sdk)、[腾讯云 Go SDK](https://github.com/tencentcloud/tencentcloud-sdk-go)
- [Eino](https://github.com/cloudwego/eino)、[Google ADK Go](https://github.com/google/adk-go)
- [Prometheus Go client](https://github.com/prometheus/client_golang)、[OpenTelemetry Go](https://opentelemetry.io/docs/languages/go/)
- [race detector](https://go.dev/doc/articles/race_detector)、[Go fuzzing](https://go.dev/doc/security/fuzz/)、[govulncheck](https://go.dev/doc/security/vuln/)、[Testcontainers Go](https://golang.testcontainers.org/modules/postgres/)
