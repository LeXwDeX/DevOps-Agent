---
id: SCHEMA-CONST
title: Schema 驱动与集中常量
status: 已定
summary: 哪些东西用版本化 schema 定义；常量怎么分组集中；一项一个定义源；生成与校验；运行时记录所用版本；变更规则。
load_when: 定义类型、枚举、配置、接口、事件或工具 schema 时；新增状态字符串或错误码时；修改已有 schema 时
depends_on: [KERNEL, GLOSSARY, EXEC-MODEL, REVIEW-POLICY]
---

# Schema 驱动与集中常量

## 1. 为什么

K-22 要求结构、类型、枚举和契约用版本化 schema 定义，常量集中、一项一个定义源。目的：

- 不让字段名、状态字符串和默认值散落在代码各处，互相漂移。
- 让检验、审计和恢复能说清楚"当时用的是哪个版本的定义"。
- 让 Agent 和人都能从一个地方查到某个值的含义。

Go 代码实现状态转换和业务算法。适合声明的结构用 schema 描述；不适合 schema 的稳定值，也必须用集中定义的类型化常量。

## 2. 定义范围

| 范围 | 内容 | 固定规则 |
|---|---|---|
| 接口与消息 | SchemaID、版本、字段类型、必填项、枚举、单位、来源和兼容规则；HTTP/JSON 接口、任务队列、事件、工具输入输出、节点通道消息。 | 每个模块一份定义源，直接写成 Go 类型、常量和校验函数。不手工维护两套定义。 |
| 配置与能力 | 模块配置、ModelProfile、模型角色绑定、Hook 登记、策略、脚本参数、解析器能力和默认值。 | 管理员检验后登记固定版本（K-20）。缺项、非法值或安全相关的未知能力阻止启用。不自动接受厂商或 Agent 新增的字段。 |
| 状态与常量 | EventType、ModelRole、执行申请状态、策略模式、读写类别、HookPoint、错误码、保留命名空间等。 | 按职责域集中、类型化定义。外部名称保持稳定。常量不是可随意编辑的配置。 |
| 业务结构与流程 | 项目资料、事实类型、运维表格 schema、任务定义、命令模板、规程包、步骤参数和状态转换契约。 | 新结构或新语义以新版本审核（K-21）。迁移、恢复和旧任务兼容分别检验。可配置的步骤不能借此获得任意命令执行权。 |

## 3. 首批 schema

下表是起底开发首先要固定的 schema。语义以右列文档为准，schema 只把它变成机器可校验的定义。

| Schema | 语义来源 | 所属模块 |
|---|---|---|
| 执行申请 | [执行模型](../20-execution/21-execution-model.md) 第 3 节 | Harness |
| 执行预期 | [执行模型](../20-execution/21-execution-model.md) 第 4 节 | Harness |
| 指纹方法与指纹值 | [执行模型](../20-execution/21-execution-model.md) 第 6 节 | Harness、执行器 |
| 策略（分层、模式、`allow_auto`、按环境） | [审核与策略](../20-execution/22-review-and-policy.md) 第 4 节 | Harness、平台管理 |
| 预授权 | [审核与策略](../20-execution/22-review-and-policy.md) 第 7 节 | Harness |
| 工具 schema | [工具包装](../20-execution/26-tool-wrapping.md) 第 2 节 | 登记中心 |
| 命令模板 | [既定任务](../20-execution/23-harness-tasks.md) 第 3 节 | 登记中心 |
| 任务定义、规程包 | [既定任务](../20-execution/23-harness-tasks.md) 第 2、4 节 | 登记中心、Harness |
| K 引用与 mask 规则 | [密钥](../20-execution/25-secrets.md) | 凭证库、mask |
| ModelProfile、ModelRole | [SDK 概览](../40-sdk/41-sdk-overview.md) | SDK、平台管理 |
| Event、EventType | [事件与 Hook](../40-sdk/45-events-and-hooks.md) | SDK、平台事件 |
| Hook 登记、HookPoint | [事件与 Hook](../40-sdk/45-events-and-hooks.md) | SDK |
| provider schema | [云服务商](../30-features/35-cloud-providers.md) | 云适配器 |
| 事实类型 | [事实地图](../30-features/33-fact-map.md) | 事实 |
| 运维表格 schema | [运维表格](../30-features/34-ops-tables.md) | 运维表格 |
| 部署包、受控环境模板 | [部署底座](../10-architecture/12-deployment-substrate.md) 第 7、8 节 | 底座 |

## 4. 集中常量分组

常量按"SDK / 平台"分开，再按模块职责分组。下表列出分组和语义来源。"建议取值"是对外字符串的建议，P0 固定后不再随意改。

| 分组 | 语义来源 | 建议取值 | 代码位置 |
|---|---|---|---|
| EventType | [事件与 Hook](../40-sdk/45-events-and-hooks.md) 第 5 节（`goagent.*`）、第 6 节（`devops.*` 平台事件总表的唯一定义源） | SDK 用 `goagent.*`，平台用 `devops.*` | SDK：`sdk/agent` 的事件文件；平台：`platform/internal/core/events` |
| ModelRole | [SDK 概览](../40-sdk/41-sdk-overview.md) 第 8.2 节 | 见 41 | `sdk/agent` |
| HookPoint | [事件与 Hook](../40-sdk/45-events-and-hooks.md) 第 8 节 | 见 45 | `sdk/agent` |
| 执行申请状态 | [执行模型](../20-execution/21-execution-model.md) 第 8 节 | `draft` 草稿、`checking` 检验中、`awaiting_review` 待人工、`rejected` 已拒绝、`awaiting_revision` 待修订、`frozen` 已冻结、`target_changed` 目标已变化、`executing` 执行中、`succeeded` 成功、`mismatched` 与预期不符、`failed` 失败、`unknown` 结果未知、`stopped` 已停止 | 平台 contracts |
| ToolRequestStatus | [SDK 工具目录](../40-sdk/44-tool-catalog.md) 第 6 节 | 与执行申请状态一一对应 | `sdk/agent`（SDK 自持） |
| 既定任务运行状态 | [既定任务](../20-execution/23-harness-tasks.md) 第 2.6 节 | `pending` 待开始、`running` 运行中、`awaiting_review` 等待人工、`awaiting_input` 等待输入、`completed` 已完成、`stopped` 已停止、`cancelled` 已取消、`recovery_interrupted` 已停止（恢复中断）；每步状态沿用执行申请状态 | 平台 contracts |
| 停止原因 | [既定任务](../20-execution/23-harness-tasks.md) 第 2.6 节、[core 层详细设计](../60-design/61-core-layer.md) 第 7.4 节 | `mismatched` 与预期不符、`unknown` 结果未知、`failed` 失败、`rejected` 已拒绝、`target_changed` 目标已变化、`no_branch_matched` 没有分支匹配、`cancelled_by_human` 人工取消、`recovery_interrupted` 恢复中断 | 平台 contracts |
| 拒绝来源 | [执行模型](../20-execution/21-execution-model.md) 第 8 节 | `human`、`policy_forbid`、`dcg_forbid`、`inspection_failed` 检验不合格 | 平台 contracts |
| 申请来源 | [执行模型](../20-execution/21-execution-model.md) 第 3 节 | `task_step` 既定任务步骤、`registered_tool` 已登记工具或命令模板、`task_launch` Agent 发起既定任务、`ad_hoc` 独立命令、`pre_authorized` 预授权、`human_terminal` 人工终端 | 平台 contracts |
| 策略模式 | [审核与策略](../20-execution/22-review-and-policy.md) 第 2.3、4 节 | `auto` 自动、`manual` 人工、`forbid` 禁止；另有布尔项 `allow_auto` | 平台 contracts |
| 策略层级 | K-07 | `action` 具体功能、`zone` 权限区、`global` provider 或工具全局、`system` 系统兜底 | 平台 contracts |
| 权限区 | [审核与策略](../20-execution/22-review-and-policy.md) 第 4 节 | `read`、`write` | 平台 contracts |
| 读写类别 | [执行模型](../20-execution/21-execution-model.md) 第 4 节 | `read_only` 只读、`write` 写入、`delete` 删除、`permission_change` 权限变更、`start_stop` 启停、`billing` 计费、`unknown` 无法确定（转人工，K-08） | 平台 contracts |
| 可逆性 | [执行模型](../20-execution/21-execution-model.md) 第 4 节 | `reversible`、`irreversible`、`unknown` 未知（按不可逆处理） | 平台 contracts |
| 自审结论 | [审核与策略](../20-execution/22-review-and-policy.md) 第 2.1 节；SelfReview 结构见 [SDK 概览](../40-sdk/41-sdk-overview.md) 第 9 节 | `no_risk`、`risk`、`not_applicable` 不适用（只用于既定任务步骤）；缺失或"未完成"按 `risk` 处理 | SDK 与平台各持一份，见第 5 节 |
| dcg 结果 | [审核与策略](../20-execution/22-review-and-policy.md) 第 2.2 节 | `pass`、`hit`、`forbid`、`unavailable` 不可用，附规则 ID 和规则包版本 | 平台 contracts |
| 原生预演状态 | [core 层详细设计](../60-design/61-core-layer.md) 第 3.1 节 | `none` 未声明预演、`passed` 成功、`failed` 失败 | 平台 contracts |
| 指纹状态 | [core 层详细设计](../60-design/61-core-layer.md) 第 3.1 节 | `collected` 已采集、`unavailable` 采集不到 | 平台 contracts |
| 人工动作 | K-09 | `execute` ▶ 执行、`reject` ✕ 拒绝、`comment` 点评 | 平台 contracts |
| 会话模式 | [审核与策略](../20-execution/22-review-and-policy.md) 第 3 节 | `manual` 手工、`auto` 自动 | 平台 contracts |
| 包装形式 | [工具包装](../20-execution/26-tool-wrapping.md) 第 1 节 | `bash`、`python`、`tool_call` | 平台 contracts |
| 目标类型 | [工具包装](../20-execution/26-tool-wrapping.md) 第 2 节 | `host`、`container`、`database`、`cloud_resource`、`file` | 平台 contracts |
| 事实核验状态 | [事实地图](../30-features/33-fact-map.md) | 见 33 | 平台 contracts |
| 错误码 | 本文第 4.1 节 | `<模块>.<原因>`，例如 `harness.fingerprint_mismatch`、`executor.digest_mismatch`、`sql.parse_failed` | `platform/internal/schema/errcode` |
| 保留命名空间 | [术语表](../00-overview/02-glossary.md) | `goagent.`、`devops.` | SDK、平台各一处 |

表中"平台 contracts"指 `platform/internal/schema/consts`。

### 4.1 错误码

- 用稳定的字符串，不用会随排列变化的数字。
- 按模块加前缀，每个模块在自己的 contracts 包里集中定义。
- 每个错误码写明：含义、是否可重试、对应的申请状态。
- 对外接口返回错误码和 mask 后的说明，不返回内部堆栈。

### 4.2 常量的约束

- 常量不是配置。不能用配置关闭固定门禁，也不能改变枚举的含义。
- 外部输入里出现未知的枚举值：涉及门禁的一律拒绝或按最保守的值处理（例如未知读写类别按"无法确定"转人工，K-08），不能落到放行。
- 缺测、未知用专门的值表示，不用 0、空字符串或"否"代替（K-05、K-27）。

## 5. 一项一个定义源

| 规则 | 说明 |
|---|---|
| 语义和取值分工 | 文档定义含义（例如 21 第 8 节定义状态含义），代码的 contracts 包定义取值。两边列出的集合必须一致，用校验脚本比对。 |
| SDK 自持 | SDK 自己持有公共契约，不为复用常量而导入平台模块。平台可以映射 SDK 的常量，不能重新定义同名常量。 |
| 厂商字段不外泄 | 厂商专用字段留在对应的基础协议文件或派生适配器文件里，不进入通用业务类型（见 [LLM 协议](../40-sdk/42-llm-protocols.md) 第 3、5 节）。 |
| 不复制 | 同一个值不能在两个包里各写一份。需要时引用定义源。 |
| 跨 SDK 边界的同义枚举 | 唯一的例外。SDK 不能导入平台，Harness 也不依赖 SDK，所以像 `ToolRequestStatus` 与执行申请状态、SelfReview 结论这类枚举，SDK 和平台各持一份。语义只以一处文档为准（例如 21 第 8 节），两边的映射写在 Agent Worker 的桥接里，并用契约测试保证一一对应、没有遗漏。 |
| 方向 | Go 代码是定义源。需要给外部提供机器可读的 schema（例如给模型的工具定义要用 JSON Schema）时，从 Go 定义导出，不反过来由外部 schema 生成 Go。 |

## 6. 定义与校验

### 6.1 schema 层是什么

- schema 是一个代码层（`internal/schema/`），是常量体和配置定义的集合：枚举、常量、错误码、配置结构、带版本的对外契约结构，以及它们的校验函数。
- 直接用 Go 编写，不依赖代码生成。
- 需要对外提供 JSON Schema 等机器可读格式时，从 Go 定义导出。导出结果必须确定：同样的定义，输出逐字节相同。

### 6.2 CI 检查

| 检查 | 失败时 |
|---|---|
| 导出的外部 schema（如果有）与已提交文件比对 | 有差异就失败 |
| 文档枚举表与代码常量集合比对 | 不一致就失败 |
| schema 样例（正例和负例）校验 | 正例不通过或负例通过就失败 |
| 旧版本样例在新版本下的兼容规则 | 违反声明的兼容规则就失败 |

### 6.3 运行时校验

- 每个跨进程边界的输入（HTTP/JSON、任务队列、节点通道、事件），都按声明的 SchemaID 和版本校验。
- 涉及门禁的结构（执行申请、执行预期、策略、工具 schema）遇到未知字段直接拒绝。
- 模型协议里的未知可选字段只按 [LLM 协议](../40-sdk/42-llm-protocols.md) 第 5 节的规则保存在审核过的 sidecar 结构中，不获得执行含义。
- 校验不能代替授权、执行预期、SQL 分析和审核。

### 6.4 SchemaID 命名

建议格式：`<命名空间>.<模块>.<名称>/v<主版本>`，例如 `devops.execution.request/v1`、`goagent.tool.request/v1`。P0 固定。

## 7. 运行时记录所用版本

每次运行都固定并记录它用到的定义版本，便于审计和恢复时说明"当时按什么规则做的"。

| 记录 | 必须记录的版本 |
|---|---|
| 执行申请 | 执行申请 schema 版本；工具或命令模板的 ID、版本、内容摘要；策略版本和生效层级；dcg 规则包版本；指纹方法版本；Harness 和执行器的实现版本 |
| 既定任务运行 | 任务定义版本和摘要；规程包版本；参数 schema 版本 |
| 模型请求 | ModelProfile 版本、协议适配器与厂商派生适配器版本（见 [LLM 协议](../40-sdk/42-llm-protocols.md)） |
| 事件 | EventType 和 payload schema 版本（见 [事件与 Hook](../40-sdk/45-events-and-hooks.md)） |
| 审计记录 | 记录 schema 版本；写入组件和它的实现版本 |
| 事实 | 事实类型 schema 版本；采集器版本 |
| 运维表格快照 | 表格 schema 版本；维护脚本版本 |
| 部署 | 部署包版本、`config_revision`、各组件镜像 digest |

"实现版本"指二进制版本号加构建提交，以及镜像 digest。

## 8. 变更规则

- 已登记定义的内容、字段、范围或语义变化，建立新版本并重新审核（K-21）。在途任务继续用原版本。
- 变更要附：差异、兼容规则、正负样例、迁移和恢复证据。检验通过后发布。
- 数据库结构按"先扩展 → 迁移数据 → 最后收缩"演进。旧调用方和在途任务退出前，不删除旧契约（见 [测试与发布](55-testing-and-release.md)）。
- SDK 公共 schema 和字符串的破坏性变化遵守 SDK 主版本规则（见 [SDK 概览](../40-sdk/41-sdk-overview.md)）。
- Agent 可以起草新定义，不能自动登记、热改在途版本，也不能覆写安全规则（K-20）。
- 改枚举含义时，先改语义来源文档（规则先改 [内核规则](../00-overview/03-kernel-rules.md)），再改代码常量。
