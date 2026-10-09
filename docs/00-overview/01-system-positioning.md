---
id: POSITIONING
title: 系统定位
status: 已定
summary: DevOps-Agent 是什么、给谁用、做什么、不做什么、当前阶段和能力清单。
load_when: 必读
depends_on: [GLOSSARY, KERNEL]
---

# 系统定位

## 1. 一句话

用户在项目里和 Agent 对话，了解项目的实际情况，部署应用，安排定时检查，并按需求操作测试库和云资源。Agent 负责理解和计划，Harness 负责门禁和按计划执行，执行器负责真正操作目标。

## 2. 使用者

- 首版是内部系统，单人使用。
- 用户可信。审核的目的是避免不可挽回的误操作，不是防范攻击者。
- 平台管理模块（含 SSO、MFA）结构预留，后续支持多人（见 [平台管理](../30-features/38-platform-admin.md)）。

## 3. 核心思路

- **先检验，再执行**（K-01）。Agent 对自己要执行的内容必须有明确预期（K-03），执行前核对目标没有被改过（K-04）。
- **常规操作走既定任务。** 日常运维大多是登记好的脚本，按固定步骤和分支运行（见 [Harness 与既定任务](../20-execution/23-harness-tasks.md)）。
- **临时操作走审核。** Agent 临时组合的命令经过模型自审、dcg 兜底和策略判定，需要时由人点 ▶ 执行或 ✕ 拒绝（见 [审核与策略](../20-execution/22-review-and-policy.md)）。
- **密钥只传 K。** 模型和用户看不到 V（K-16）。
- **监控独立。** 告警不依赖 Agent 或模型（K-25）。

## 4. 能力清单

| 能力 | 说明 | 文档 |
|---|---|---|
| 项目工作台 | 按项目组织对话、资料、环境、任务和记录 | [31](../30-features/31-project-workbench.md) |
| 项目内问答 | 基于项目资料和已核验事实回答，标明来源与时间 | [31](../30-features/31-project-workbench.md)、[33](../30-features/33-fact-map.md) |
| 一键部署 | 按规程包部署应用，例如 OSS 制品到 systemd 服务 | [32](../30-features/32-project-deployment.md) |
| 定时检查 | 按时间表达式运行已登记的检查任务 | [23](../20-execution/23-harness-tasks.md)、[36](../30-features/36-monitoring.md) |
| 测试库操作 | 查询、增删改、建表改表删表，每条 SQL 走检验 | [24](../20-execution/24-sql.md) |
| 云资源操作 | Azure、阿里云、腾讯云，每个 API 单独配置策略 | [35](../30-features/35-cloud-providers.md) |
| 事实地图 | 资产、关系、配置和证据 | [33](../30-features/33-fact-map.md) |
| 运维表格 | 由已登记脚本维护的通用表格，按自然日更新 | [34](../30-features/34-ops-tables.md) |
| 监控旁路 | 独立采集、规则、告警和通知 | [36](../30-features/36-monitoring.md) |
| GoAgent SDK | 可被其他 Go 系统嵌入的 Agent SDK | [41](../40-sdk/41-sdk-overview.md) |

## 5. 不做什么

- 不自动注册工具、skill、Hook 或规程；不自动改运行配置；不做自主学习（K-20）。
- 不在系统内提供绕过检验的破窗通道（K-26）。
- 不在首版自动执行自由 Bash（K-10）。
- 不把 Hermes 作为部署组件、依赖、协议接入或源码兼容目标。Hermes 只是设计参考。
- 首版不做高可用、多人审批和细粒度角色。

## 6. 技术栈

- 平台与 Agent 模块使用 Go。Agent 能力封装为独立的 GoAgent SDK。
- 首版两个应用容器：core（核心控制服务、Agent Worker）和 runtime（Harness、调度、执行器、模型出口代理）。PostgreSQL 和监控旁路单独部署。
- 镜像只含程序；配置、密钥和数据全部来自外部挂载或环境变量表里的环境变量（K-31）。
- 详见 [组件与边界](../10-architecture/11-components-and-boundaries.md) 和 [Go 技术选型](../50-engineering/51-go-stack.md)。

## 7. 当前阶段

- 本文档库是唯一设计来源。文档内容是设计，不代表功能已经实现（K-30）。
- 代码按层一块一块实现，方法见 [阶段交付](../50-engineering/54-phases-and-acceptance.md) 第 1.1 节和 [起底开发指引](../50-engineering/56-bootstrap-guide.md)。
- 哪些能力已经可用，以代码、测试和发布清单为准（见 [测试与发布](../50-engineering/55-testing-and-release.md)）。不处理 Hermes 源码。
