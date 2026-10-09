# DevOps-Agent

服务端运维 Agent 设计项目。平台与 Agent 模块规划使用 Go。Hermes 仅作设计参考，不属于部署组件、协议接入或源码兼容目标。

用户在项目里和 Agent 对话，了解项目的实际情况，部署应用，安排定时检查，并按需求操作测试库和云资源。

## 设计文档

设计文档库在 [docs/](docs/README.md)，是唯一设计来源。按目录和任务路由按需加载。

- 必读：[系统定位](docs/00-overview/01-system-positioning.md)、[术语表](docs/00-overview/02-glossary.md)、[内核规则](docs/00-overview/03-kernel-rules.md)
- 待定事项：[待定事项](docs/00-overview/04-open-questions.md)

## 设计要点

- 先检验，再执行。Agent 执行前写出明确的执行预期，执行前用状态指纹确认目标没有被改过。
- 常规运维走已登记的 Harness 既定任务；临时命令经模型自审、dcg 兜底和策略判定，需要时人工点 ▶ 执行或 ✕ 拒绝。
- 密钥只传 K，执行器以环境变量注入 V，模型和用户都读不到 V。
- 监控旁路独立运行，不依赖 Agent 或模型。
- Agent 能力封装为可嵌入的 GoAgent SDK。

## 当前阶段

项目在开发阶段，按层逐块开发，当前开发 core 层。功能尚未可用。不处理 Hermes 源码。

## License

MIT。LICENSE 文件后续直接使用 GitHub 提供的 MIT 模板添加。
