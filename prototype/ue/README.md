# UE 原型

工作台前端的可点击原型。用于对齐页面结构和交互，不是设计来源，也不是前端代码。

- 在线入口（设计画布，可 Play 点击）：https://claude.ai/artifact/CzJbcKNj7ifaK2JgdFsDY3
- 本目录是画布文件的副本。`.dc.html` 依赖画布运行时（`support.js`），不能直接在浏览器打开。
- 页面数据都是示例数据。

## 页面

| 文件 | 页面 | 对应设计 |
|---|---|---|
| `Main.dc.html` | 运维大盘 | `docs/30-features/39-console-views.md` 第 0 节 |
| `Workbench.dc.html` | 项目对话 | `docs/30-features/31-project-workbench.md` 第 3.3 节 |
| `Review.dc.html` | 人工审核（▶ 执行 / ✕ 拒绝 / 点评） | `docs/20-execution/22-review-and-policy.md` 第 6 节 |
| `Topology.dc.html` | 逻辑拓扑地图 | `docs/30-features/39-console-views.md` 第 1 节 |
| `Resources.dc.html` | 当前资源 | `docs/30-features/39-console-views.md` 第 2 节 |
| `Sop.dc.html` | Harness 流程（SOP）、版本与回滚 | `docs/30-features/39-console-views.md` 第 3 节 |
| `canvas.json` | 画布索引：页面位置与顺序 | — |

## 规则

- 原型和设计文档冲突时，以 `docs/` 为准，再改原型。
- 改原型时，先改在线画布，再把文件同步到本目录。
