---
id: INDEX
title: DevOps-Agent 设计文档库
status: 持续维护
summary: 文档库入口。说明怎么用、目录、按任务加载哪些文档。
load_when: 每次开始工作时先读本页
depends_on: []
---

# DevOps-Agent 设计文档库

本文档库是项目的唯一设计来源，供 Agent 做项目起底开发时按需加载。文档内容是设计，不代表功能已经实现（K-30）。

## 怎么用

1. **先读必读集**：[系统定位](00-overview/01-system-positioning.md)、[术语表](00-overview/02-glossary.md)、[内核规则](00-overview/03-kernel-rules.md)。
2. **按任务加载**：查下方"任务路由"，只加载列出的文档。每篇文档开头的 `summary` 和 `load_when` 可以帮你判断是否需要读全文。
3. **规则冲突时**：以 [内核规则](00-overview/03-kernel-rules.md) 为准。其他文档用 `K-xx` 引用规则，不重复改写。
4. **遇到待定项**：文档里的 `Qn` 指 [待定事项](00-overview/04-open-questions.md)。按"当前默认"做。设计没写清楚的地方，Agent 自己会审并采用推荐做法，把决定写进对应文档和审核报告；改变当前默认或已定设计时同样处理，并在审核报告里单独列出（见 [起底开发指引](50-engineering/56-bootstrap-guide.md) 第 6 节）。
5. **旧称**：拆分前的旧稿已删除。"所有命令必须真实无副作用 DRY-RUN""强制独立 JEV 交叉审核""密钥剪贴板"等旧说法已被本库取代，旧称对照见 [术语表](00-overview/02-glossary.md)。

## 必读集

| 文档 | 内容 |
|---|---|
| [01 系统定位](00-overview/01-system-positioning.md) | 做什么、不做什么、当前阶段、能力清单 |
| [02 术语表](00-overview/02-glossary.md) | 统一用词与旧称对照 |
| [03 内核规则](00-overview/03-kernel-rules.md) | K-01 至 K-31，规则唯一定义源 |

## 目录

| 路径 | 内容 | 何时加载 | 状态 |
|---|---|---|---|
| **00 总览** | | | |
| [01-system-positioning](00-overview/01-system-positioning.md) | 系统定位、范围、非目标、能力清单 | 必读 | 已定 |
| [02-glossary](00-overview/02-glossary.md) | 术语表 | 必读 | 已定 |
| [03-kernel-rules](00-overview/03-kernel-rules.md) | 内核规则 K-xx | 必读 | 已定 |
| [04-open-questions](00-overview/04-open-questions.md) | 待定事项及当前默认 | 遇到 Qn 时 | 持续维护 |
| **10 架构** | | | |
| [11-components-and-boundaries](10-architecture/11-components-and-boundaries.md) | 组件、职责、权限边界、通信；core + runtime 容器划分、挂载表与环境变量表（模型接入变量） | 开发任何模块前 | 已定 / 部署形态草案 |
| [12-deployment-substrate](10-architecture/12-deployment-substrate.md) | Docker 底座、Supervisor、跨 VM 分发、运行环境模板、工具隔离 | 部署平台自身、容器与沙盒相关开发 | 已定 |
| [13-state-and-recovery](10-architecture/13-state-and-recovery.md) | 状态位置、恢复即停止、迁移 | 存储、状态机、备份恢复 | 已定 |
| **20 执行** | | | |
| [21-execution-model](20-execution/21-execution-model.md) | 执行路径、执行申请、执行预期、原生预演、指纹、状态 | 任何产生实际操作的功能 | 已定 |
| [22-review-and-policy](20-execution/22-review-and-policy.md) | 自审、dcg、会话模式、策略分层、人工审核、预授权 | 审核界面、策略、门禁 | 已定 |
| [23-harness-tasks](20-execution/23-harness-tasks.md) | Harness 职责、既定任务、分支、命令模板、规程包 | 编排、规程包、定时任务 | 已定 |
| [24-sql](20-execution/24-sql.md) | SQL 解析分类、预期、指纹、组件 SQL 边界 | 任何发出 SQL 的功能 | 已定 |
| [25-secrets](20-execution/25-secrets.md) | 密钥 K→V、注入、mask 规则；首版模型 API key 来源 | 凭证、执行器、日志、模型输入 | 已定 / 存储待设计 |
| [26-tool-wrapping](20-execution/26-tool-wrapping.md) | 工具包装形式、工具 schema、登记、MCP 定位 | 新增或登记工具 | 已定 |
| **30 功能** | | | |
| [31-project-workbench](30-features/31-project-workbench.md) | 项目、用户流程、项目 Agent 生成 | 工作台与项目功能 | 已定 |
| [32-project-deployment](30-features/32-project-deployment.md) | 部署规程包、OSS 到系统服务的流程 | 项目部署 | 已定 |
| [33-fact-map](30-features/33-fact-map.md) | 事实模型、证据、采集 | 事实地图、目标选择 | 已定 |
| [34-ops-tables](30-features/34-ops-tables.md) | 运维表格、维护脚本、自然日周期 | 运维表格 | 雏形 |
| [35-cloud-providers](30-features/35-cloud-providers.md) | 云 API 三层、provider schema 与策略、数据库派生 | 云资源与数据库适配 | 已定 |
| [36-monitoring](30-features/36-monitoring.md) | 监控旁路、采集组件、自研模块与派生解析器 | 监控与告警 | 已定 |
| [37-ops-entrypoints](30-features/37-ops-entrypoints.md) | 运维入口、发起方式、人类终端、无破窗通道 | 入口与终端 | 已定 |
| [38-platform-admin](30-features/38-platform-admin.md) | 平台管理模块、SSO/MFA 预留、审计页面 | 管理功能 | 结构预留 |
| [39-console-views](30-features/39-console-views.md) | 侧边栏四个入口：运维大盘、逻辑拓扑地图、当前资源、Harness 流程（SOP）列表与版本回滚 | 前端页面、运维大盘、TopologyView、SOP 版本 | 已定 |
| **40 SDK** | | | |
| [41-sdk-overview](40-sdk/41-sdk-overview.md) | GoAgent SDK 边界、宿主端口、模型角色 | 任何 SDK 开发 | 已定 |
| [42-llm-protocols](40-sdk/42-llm-protocols.md) | 四种基础协议、厂商派生适配器、请求与流式管理 | 模型访问 | 已定 |
| [43-context-management](40-sdk/43-context-management.md) | 上下文投影、ContextPatch、压缩、同一调用多修订 | 上下文与会话 | 已定 |
| [44-tool-catalog](40-sdk/44-tool-catalog.md) | SDK 工具目录与统一申请端口 | SDK 工具层 | 已定 |
| [45-events-and-hooks](40-sdk/45-events-and-hooks.md) | 公开事件枚举、Hook 契约 | 事件与扩展 | 已定 |
| **50 工程** | | | |
| [51-go-stack](50-engineering/51-go-stack.md) | Go 版本、依赖选型与准入 | 引入依赖、搭建工程 | 已定 |
| [52-schema-and-constants](50-engineering/52-schema-and-constants.md) | Schema 驱动与集中常量 | 定义类型、枚举、配置 | 已定 |
| [53-repo-layout](50-engineering/53-repo-layout.md) | 仓库与模块布局、依赖方向 | 创建目录和模块 | 已定 |
| [54-phases-and-acceptance](50-engineering/54-phases-and-acceptance.md) | P0–P6 阶段与验收条件 | 规划和验收 | 已定 |
| [55-testing-and-release](50-engineering/55-testing-and-release.md) | 复测与发布质量门槛 | 测试与发布 | 已定 |
| [56-bootstrap-guide](50-engineering/56-bootstrap-guide.md) | 起底开发指引：第一步做什么 | 开始写代码前 | 已定 |
| **60 详细设计** | | | |
| [61-core-layer](60-design/61-core-layer.md) | core 层详细设计：状态机、检验流水线、策略解析、修订冻结、编排引擎、端口与测试 | 开发或审核 core 层 | 已定 |
| [62-provider-storage-modelgw](60-design/62-provider-storage-modelgw.md) | provider 第一块：PostgreSQL 存储、outbox、审计、系统时钟与 ID、模型出口代理 | 开发或审核存储、模型出口代理 | 已定 |

## 任务路由

除必读集外，按任务加载：

| 任务 | 加载 |
|---|---|
| 开始项目起底开发 | 56、53、54、11、51 |
| 开发 core 层 | 61、21、22、23、53、52 |
| 开发 provider 存储或模型出口代理 | 62、61、11、13、25、51 |
| 开发 Harness 门禁 | 21、22、23、13、11 |
| 开发执行器 | 21、25、24、26、11 |
| 开发审核界面与策略配置 | 22、21、38 |
| 新增命令模板或脚本 | 23、26、21、22 |
| 开发测试库 SQL 操作 | 24、21、35 |
| 开发项目部署流程 | 32、23、21、25、13 |
| 开发运维表格 | 34、23、24、13 |
| 开发事实地图 | 33、35、36 |
| 开发工作台前端（运维大盘、拓扑地图、资源、SOP 列表） | 39、33、23、31 |
| 接入一个云厂商 | 35、26、22、21 |
| 开发监控旁路或自研监控模块 | 36、33、12 |
| 开发 GoAgent SDK | 41、42、43、44、45、52 |
| 新增模型厂商适配器 | 42、41、55 |
| 开发 Hook 或事件 | 45、43、41 |
| 部署平台自身 | 12、11、13 |
| 开发模型出口代理或模型接入 | 11、25、42、41 |
| 开发平台管理模块 | 38、22、25 |
| 定义 schema、类型或常量 | 52、53 |

## 文档约定

- 每篇文档以 YAML front matter 开头：`id`、`title`、`status`、`summary`、`load_when`、`depends_on`。
- `status` 取值：已定 / 雏形 / 草案 / 结构预留 / 持续维护。
- 规则用 `K-xx` 引用；待定项用 `Qn` 引用。
- 图用 Mermaid 写在 Markdown 里。
- 只记录当前设计，不记录更新日志。
- 用短句和常用词。
- 改规则先改 [内核规则](00-overview/03-kernel-rules.md)，再改引用处；新增术语先加进 [术语表](00-overview/02-glossary.md)。
