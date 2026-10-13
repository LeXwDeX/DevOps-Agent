---
id: DESIGN-M1
title: M1 平台骨架详细设计
status: 已定
summary: M1（本地 Docker 能对话）用到、而上游设计还没写清的部分。第一部分是部署与进程：M1 与 P1 的范围对应、镜像与 Compose 服务、入口进程、运行身份、网络与端口、配置文件、引导密钥文件、HTTP 资源限制、启动与停止、重启策略、数据库引导与受控迁移，以及 L05、L06、L13 的范围、测试和审核清单。核心控制服务、SDK v0、账号与页面由后续章节补充。
load_when: 开发或审核 cmd/migrate、cmd/model-proxy、入口进程、镜像、docker compose 部署之前；调整 M1 的进程、端口、挂载或重启策略时
depends_on: [KERNEL, COMPONENTS, SUBSTRATE, STATE-RECOVERY, GO-STACK, REPO-LAYOUT, PHASES, DESIGN-PROVIDER-A]
---

# M1 平台骨架详细设计

## 1. 范围

M1 的目标：两容器（core、runtime）加 PostgreSQL 用 `docker compose` 起来；用户经页面或 API 和 Agent 对话；模型请求经模型出口代理发往 `DEVOPS_LLM_BASE_URL`；工具执行全部关闭。

- M1 是 [54](../50-engineering/54-phases-and-acceptance.md) P1 的子集。哪些 P1 条目在 M1 验收、哪些后移，以 54 第 3 节"P1 的 M1 子集"表为准。M1 完成不等于 P1 完成。
- 本设计只补上游设计没有写清的部分。语义仍以上游文档为准：组件与权限边界见 [11](../10-architecture/11-components-and-boundaries.md)，重启与迁移见 [12](../10-architecture/12-deployment-substrate.md)，恢复见 [13](../10-architecture/13-state-and-recovery.md)。
- 本设计按章节分步写成。第 2 至 16 节（部署与进程，L04.1）已定。核心控制服务与任务队列（L04.2）、SDK v0 与平台依赖方式（L04.3）、账号、凭证库最小版与页面（L04.4）由后续章节补充；在它们写成之前，下游任务不得按自己的理解补齐。

| 章节 | 内容 | 供下游 |
|---|---|---|
| 第 2 至 4 节 | 部署总览、镜像、Compose 服务、进程、入口进程 | L06.2、L13.1、L13.2 |
| 第 5 至 8 节 | 运行身份、网络与端口、配置文件、引导密钥文件与挂载 | L05、L06、L13 |
| 第 9 至 11 节 | HTTP 资源限制、启动、停止与重启 | L06、L08.4、L13 |
| 第 12 节 | 数据库引导与受控迁移 | L05、L13.2、L14 |
| 第 13 至 16 节 | 新增错误码、验证防泄密、下游任务清单、审核清单 | L05、L06、L13、L14 |

## 2. 部署总览

```mermaid
flowchart LR
    U[浏览器 / API 客户端] -- "127.0.0.1:8080<br/>（宿主回环）" --> CS
    subgraph CORE[core 容器]
        CE[core 入口进程] --> CS[核心控制服务<br/>:8080 / 127.0.0.1:8090]
        CE --> W[Agent Worker]
        W -- "127.0.0.1:8090<br/>内部接口" --> CS
    end
    subgraph RT[runtime 容器]
        RE[runtime 入口进程] --> MP[模型出口代理 :8082]
    end
    W -- "http://runtime:8082<br/>客户端 token" --> MP
    MP -- "https" --> LLM[DEVOPS_LLM_BASE_URL]
    CS --> PG[(PostgreSQL :5432)]
    MG[migrate 一次性服务] -.-> PG
    DI[db-init 一次性服务] -.-> PG
```

- 宿主上只发布一个端口：core 的 `8080`，默认只绑定宿主回环地址 `127.0.0.1`。远程使用经 SSH 隧道。
- PostgreSQL、模型出口代理不发布到宿主。
- `migrate` 和 `db-init` 是一次性服务，只由管理员手动运行（第 12 节）。
- M1 没有底座、监控旁路、Harness、持久调度服务和执行器。

## 3. 镜像、Compose 服务与进程

### 3.1 镜像

| 镜像 | 内容（只含程序，K-31） | 基础镜像 |
|---|---|---|
| core | `/opt/devops-agent/bin/entry`、`core`、`agent-worker`；页面静态文件（L04.4 定） | 按 digest 固定，不用 `latest`。具体选型由 L13.1 定，要求：无包管理器、无需 shell |
| runtime | `/opt/devops-agent/bin/entry`、`model-proxy`、`migrate` | 同上 |

- `migrate` 在 runtime 镜像里，不代表它常驻运行。runtime 入口进程不启动它（第 4 节）。
- Harness、调度、执行器的二进制在 M2 之后加入 runtime 镜像。
- 镜像里没有配置文件、密钥、`.env`，也没有测试用程序。

### 3.2 Compose 服务

Compose 项目名固定为 `devops-agent`。

| 服务 | 镜像 | 启动方式 | 重启 | 发布端口 |
|---|---|---|---|---|
| `postgres` | PostgreSQL 16 官方镜像，按 digest 固定 | `up -d` | `"no"` | 无 |
| `core` | core | `up -d` | `"no"` | `${CORE_BIND:-127.0.0.1:8080}:8080` |
| `runtime` | runtime | `up -d` | `"no"` | 无 |
| `migrate` | runtime（覆盖 entrypoint） | `--profile migrate run --rm migrate` | `"no"` | 无 |
| `db-init` | PostgreSQL 16 官方镜像 | `--profile bootstrap run --rm db-init` | `"no"` | 无 |

- `profiles` 只决定默认是否启动，不是权限门禁。一次性服务的权限由它的挂载和环境决定（第 8 节）。
- `migrate` 服务独立定义，不用 `extends` 或 YAML 锚点继承 `runtime` 的环境和挂载。它没有 `DEVOPS_LLM_*`。

### 3.3 进程

| 容器 | 进程 | 二进制 | 运行身份（UID:GID） | M1 是否启动 |
|---|---|---|---|---|
| core | core 入口进程 | `entry core` | `0:0`，能力受限（第 5 节） | 是 |
| core | 核心控制服务 | `core` | `10001:10001` | 是 |
| core | Agent Worker | `agent-worker` | `10002:10002` | 是 |
| runtime | runtime 入口进程 | `entry runtime` | `0:0`，能力受限 | 是 |
| runtime | 模型出口代理 | `model-proxy` | `10011:10011` | 是 |
| runtime | Harness | `harness` | `10012:10012`（预留） | 否，M2 |
| runtime | 持久调度服务 | `scheduler` | `10013:10013`（预留） | 否，M2 之后 |
| runtime | 执行器 | `executor` | `10014:10014`（预留） | 否，M2 |
| migrate | 迁移程序 | `migrate` | `10020:10020` | 一次性 |

- M1 不为验证环境隔离而启动占位的 Harness、调度或执行器。
- 进程表编译进 `entry` 二进制，按参数 `core` 或 `runtime` 选择。没有配置文件或命令行能增加、替换子进程。

## 4. 入口进程

入口进程是一个只用标准库的 Go 二进制（`platform/cmd/entry`）。它只做启动与收尾，不是通用进程管理器。

### 4.1 启动

1. 核对自身环境：
   - `entry core`：调用 `modelgw.ForbidInCore`。环境里有任一模型接入变量就以非 0 退出，输出只含变量名（[62](62-provider-storage-modelgw.md) 第 5.3 节）。
   - `entry runtime`：不检查模型接入变量是否存在。缺失时代理照常运行并报告不可用（[11](../10-architecture/11-components-and-boundaries.md) 第 5.4 节）。
2. 按进程表顺序启动子进程。每个子进程：
   - 用固定的绝对路径启动，不经 shell，参数固定。
   - 用 `SysProcAttr.Credential` 切换到表里的 UID、GID，附加组为空。
   - 环境变量按白名单从空环境构造（第 4.3 节），不继承入口进程的环境。
   - 标准输出和标准错误接到入口进程的标准输出和标准错误。
3. 任一子进程启动失败：停止已启动的兄弟进程（第 4.2 节），以非 0 退出。

### 4.2 退出与信号

| 情况 | 入口进程的做法 |
|---|---|
| 任一子进程退出，包括退出码 0 | 视为故障。向其余子进程发 SIGTERM，等待最多 20 秒，仍未退出的发 SIGKILL；全部回收后以非 0 退出 |
| 收到 SIGTERM 或 SIGINT | 向全部子进程转发 SIGTERM，等待最多 20 秒，再 SIGKILL；全部回收后退出。全部子进程都在宽限期内退出时退出码为 0 |
| 多个子进程同时退出 | 按第一个观察到的退出处理，其余照常回收 |

- 入口进程只用每个子进程自己的 `cmd.Wait` 回收，不设通用的 `wait(-1)` 回收循环，避免争抢退出状态。M1 的子进程都不再启动子进程（[11](../10-architecture/11-components-and-boundaries.md) 第 5.4 节对代理的要求；核心控制服务和 Worker 同样不启动子进程）。
- 入口进程每个动作写一行结构化日志：子进程名、PID、退出码或信号。不写环境变量的值。
- Compose 的 `stop_grace_period` 设为 30 秒，大于入口进程的 20 秒宽限。
- 每个服务进程收到 SIGTERM 后调用 `http.Server.Shutdown`，宽限 15 秒。

### 4.3 子进程环境白名单

| 子进程 | 环境变量 |
|---|---|
| 核心控制服务 | 无 |
| Agent Worker | 无 |
| 模型出口代理 | 入口进程环境里存在的 `DEVOPS_LLM_BASE_URL`、`DEVOPS_LLM_API_KEY`，原样传递；不存在就不传 |

- 程序不读取表外的环境变量（K-31）。配置一律来自配置文件（第 7 节）。
- 入口进程的初始环境里仍有模型接入变量。隔离靠运行身份：其他子进程的 UID 不同，读不到入口进程和代理的 `/proc/<pid>/environ`（[11](../10-architecture/11-components-and-boundaries.md) 第 5.4 节）。

## 5. 运行身份与权限

### 5.1 容器安全选项

core、runtime 两个服务：

| 选项 | 值 | 理由 |
|---|---|---|
| `user` | `"0:0"` | 入口进程要切换到两个不同 UID 启动子进程，非 root 做不到 |
| `cap_drop` | `[ALL]` | — |
| `cap_add` | `[SETUID, SETGID, KILL]` | 只够切换身份和向其他 UID 的子进程发信号 |
| `security_opt` | `[no-new-privileges:true]` | 子进程不能重新获得权限 |
| `read_only` | `true` | 根文件系统只读；可写目录只有挂载的数据和临时目录 |
| Docker socket、宿主 PID 命名空间、`privileged` | 都不用 | K-18 |

- 子进程从 root 切换到非 root UID 时，能力集被清空。业务子进程没有任何能力。
- 入口进程没有 `CAP_DAC_OVERRIDE`。它读不到属于其他 UID、权限 0400 的密钥文件，也不需要读。

`migrate` 服务：`user: "10020:10020"`，`cap_drop: [ALL]`，`no-new-privileges`，`read_only: true`。

### 5.2 文件可读矩阵

| 文件（容器内路径） | 属主 | 权限 | 能读的进程 |
|---|---|---|---|
| `/run/secrets/devops-agent/core-service/db-password` | 10001 | 0400 | 核心控制服务 |
| `/run/secrets/devops-agent/agent-worker/model-proxy-token` | 10002 | 0400 | Agent Worker |
| `/run/secrets/devops-agent/model-proxy/client-token`（runtime） | 10011 | 0400 | 模型出口代理 |
| `/run/secrets/devops-agent/db-password`（migrate） | 10020 | 0400 | 迁移程序 |
| `/etc/devops-agent/*.json` | root | 0444 | 同容器全部进程 |

- 每个身份一个子目录，属主是该身份，权限 0500。父目录属主 root，权限 0755，只用于遍历。目录不能是 0400，否则无法进入。
- 核心控制服务读不到 Worker 的代理 token；Worker 读不到数据库口令。
- 宿主目录的属主和权限由宿主目录准备脚本设置并核对（第 15.3 节 L13.2）。Compose v5 对文件型 `secrets` 的 `uid`、`gid`、`mode` 不生效，所以用 bind mount，并核对实际属主和权限，不能只审 YAML。

## 6. 网络与端口

### 6.1 Compose 网络

| 网络 | 类型 | 成员 | 用途 |
|---|---|---|---|
| `app` | `internal: true` | postgres、core、runtime、migrate、db-init | 平台内部通信，没有外网出口 |
| `edge` | 普通 bridge | core | 只为发布 `8080` 端口 |
| `egress` | 普通 bridge | runtime | 模型出口代理访问 `DEVOPS_LLM_BASE_URL` |

- core 在 `edge` 上有外网出口。M1 不用网络阻断 core 的出口；core 不持有模型 key 和任何 V，访问模型厂商没有凭证（[12](../10-architecture/12-deployment-substrate.md) 第 8 节的网络边界由宿主防火墙或底座阶段落实）。这是 M1 的已知限制。
- runtime 不在 `edge` 上，不发布端口。
- 发布端口在仅 `internal` 网络的容器上不生效，所以 core 需要 `edge`。L13.2 要验证这一点。

### 6.2 监听地址

| 进程 | 监听地址 | 对谁开放 |
|---|---|---|
| 核心控制服务（公开接口） | `0.0.0.0:8080` | 宿主 `127.0.0.1:8080`（可由 `CORE_BIND` 改）；`app` 网络 |
| 核心控制服务（内部接口） | `127.0.0.1:8090` | 只有同容器的 Worker |
| 模型出口代理 | `0.0.0.0:8082` | `app` 网络（core 容器的 Worker） |
| Harness | `:8081` | 预留，M1 不监听 |
| PostgreSQL | `5432` | `app` 网络 |

- 内部接口（任务队列等）只在容器回环地址上，不经 `8080` 暴露。接口内容和鉴权由 L04.2 定。
- `CORE_BIND` 改为非回环地址时，由管理员自行负责访问控制；页面登录见 L04.4。

## 7. 配置文件

### 7.1 通用规则

- 格式：JSON，UTF-8。用标准库 `encoding/json`，开启 `DisallowUnknownFields`。不引入新的配置库（[51](../50-engineering/51-go-stack.md) 第 3 节）。
- 每个文件有 `schema` 字段，取值固定（见下表）。缺失、取值不符、未知字段、类型错误、超出范围，进程都以非 0 退出，错误码 `config.invalid`，信息里写字段路径，不写字段值。
- 文件里只有非敏感参数和密钥文件的路径。不写口令、token、模型端点。
- 时长写成 Go 时长字符串，例如 `"30s"`。大小写成字节数。
- 每个进程只读自己的文件。路径固定，不由环境变量或参数改。
- 配置变更要重启容器，按恢复处理（K-24）。M1 不支持热加载。

| 文件（容器内） | `schema` | 读取进程 |
|---|---|---|
| `/etc/devops-agent/core.json` | `devops.config.core/v1` | 核心控制服务 |
| `/etc/devops-agent/agent-worker.json` | `devops.config.agent-worker/v1` | Agent Worker |
| `/etc/devops-agent/model-proxy.json` | `devops.config.model-proxy/v1` | 模型出口代理 |
| `/etc/devops-agent/migrate.json` | `devops.config.migrate/v1` | 迁移程序 |

入口进程没有配置文件。

### 7.2 字段

下表是本部分定下的字段。L04.2 至 L04.4 可以给同一文件追加字段，追加时在本节补表；在那之前，下游实现不得自行加字段。

**`core.json`**

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `schema` | string | 必填 | `devops.config.core/v1` |
| `listen.public` | string | `0.0.0.0:8080` | 公开接口 |
| `listen.internal` | string | `127.0.0.1:8090` | 内部接口，必须是回环地址，否则 `config.invalid` |
| `database.host` | string | 必填 | 例如 `postgres` |
| `database.port` | int | `5432` | 1 至 65535 |
| `database.user` | string | 必填 | 例如 `devops_core` |
| `database.password_file` | string | 必填 | `/run/secrets/devops-agent/core-service/db-password` |
| `database.sslmode` | string | `disable` | `disable` 或 `require`。Compose 内部网络用 `disable` |
| `database.max_conns` | int | `10` | 1 至 100 |
| `http.public` | 对象 | 见第 9 节 | 公开接口的资源限制 |
| `http.internal` | 对象 | 见第 9 节 | 内部接口的资源限制 |

**`agent-worker.json`**

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `schema` | string | 必填 | `devops.config.agent-worker/v1` |
| `core_internal_url` | string | `http://127.0.0.1:8090` | 必须是回环地址的 `http` URL |
| `model_proxy.url` | string | `http://runtime:8082` | 代理地址 |
| `model_proxy.token_file` | string | 必填 | `/run/secrets/devops-agent/agent-worker/model-proxy-token` |
| `concurrency` | int | `2` | 0 至 16。`0` 表示进程存活但不领取任务（第 11.3 节） |

**`model-proxy.json`**

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `schema` | string | 必填 | `devops.config.model-proxy/v1` |
| `listen` | string | `0.0.0.0:8082` | — |
| `client_token_file` | string | 必填 | `/run/secrets/devops-agent/model-proxy/client-token` |
| `upstream.timeout` | duration | `120s` | 传给 `modelgw.Options.Timeout`（62 第 5.2 节第 7 条） |
| `upstream.max_request_bytes` | int | `4194304` | 传给 `Options.MaxRequestBytes` |
| `upstream.max_response_bytes` | int | `16777216` | 传给 `Options.MaxResponseBytes` |
| `http` | 对象 | 见第 9 节 | 资源限制 |

- 没有模型端点字段。模型端点只来自 `DEVOPS_LLM_BASE_URL`（[11](../10-architecture/11-components-and-boundaries.md) 第 5.4 节）。
- 客户端 token 文件缺失、不可读、为空或含空白字符：代理以非 0 退出，错误码 `config.secret_unreadable`，信息只写文件路径。

**`migrate.json`**

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `schema` | string | 必填 | `devops.config.migrate/v1` |
| `database.host`、`database.port`、`database.sslmode` | — | 同 `core.json` | — |
| `database.user` | string | 必填 | `devops_migrate` |
| `database.password_file` | string | 必填 | `/run/secrets/devops-agent/db-password` |
| `databases` | string 数组 | 必填 | M1 为 `["core", "harness", "config", "audit"]`；只能取内嵌迁移集里存在的库名 |

## 8. 引导密钥文件与挂载

### 8.1 引导密钥文件

| 宿主文件（相对 `${DATA_ROOT}/${INSTANCE_ID}/`） | 内容 | 生成 | 挂进 |
|---|---|---|---|
| `secrets/postgres/superuser-password` | PostgreSQL 超级用户口令 | 准备脚本生成 | postgres、db-init |
| `secrets/migrate/db-password` | `devops_migrate` 口令 | 准备脚本生成 | migrate、db-init |
| `secrets/core/core-service/db-password` | `devops_core` 口令 | 准备脚本生成 | core、db-init |
| `secrets/core/agent-worker/model-proxy-token` | Worker 访问代理的客户端 token | 准备脚本生成，与下一行同值 | core |
| `secrets/runtime/model-proxy/client-token` | 代理校验用的客户端 token | 准备脚本复制上一行 | runtime |

- 口令和 token 都是 32 字节随机数的 base64url 编码，不含换行。
- 准备脚本只在文件不存在时生成。已存在就不覆盖，也不输出值。
- 两个 token 文件必须同值。轮换时同时替换两份，再重启 core 和 runtime。
- 代理 token 不是资产凭证，也不是模型 key。它只证明请求来自本实例的 Worker（[53](../50-engineering/53-repo-layout.md) 第 6 节）。
- 只有模型出口代理（凭 token）能回答模型状态。核心控制服务不读代理 token；页面的模型状态由 Worker 查询代理后报告给核心，报告带时间，过期显示"未知"（K-05）。报告协议由 L04.2 定。

### 8.2 M1 挂载

宿主路径都在 `${DATA_ROOT}/${INSTANCE_ID}/` 下。`DATA_ROOT` 默认 `/srv/devops-agent`，`INSTANCE_ID` 默认 `local`。

| 服务 | 容器内路径 | 宿主路径 | 方式 |
|---|---|---|---|
| core | `/etc/devops-agent` | `config/core/` | 只读 |
| core | `/run/secrets/devops-agent` | `secrets/core/` | 只读 |
| core | `/var/lib/devops-agent` | `data/core/` | 读写 |
| core | `/var/tmp/devops-agent` | `tmp/core/` | 读写 |
| runtime | `/etc/devops-agent` | `config/runtime/` | 只读 |
| runtime | `/run/secrets/devops-agent` | `secrets/runtime/` | 只读 |
| runtime | `/var/tmp/devops-agent` | `tmp/runtime/` | 读写 |
| migrate | `/etc/devops-agent` | `config/migrate/` | 只读 |
| migrate | `/run/secrets/devops-agent` | `secrets/migrate/` | 只读 |
| postgres | `/var/lib/postgresql/data` | `pgdata/` | 读写 |
| postgres | `/run/secrets/postgres` | `secrets/postgres/` | 只读 |
| db-init | `/run/secrets/postgres`、`/run/secrets/migrate`、`/run/secrets/core-service` | 对应的 `secrets/` 子目录 | 只读 |
| db-init | `/opt/devops-agent/db-init` | 源码目录 `platform/deploy/db-init/` | 只读 |
| postgres | `/var/backups/devops-agent` | `backup/` | 读写 |

- 凭证库、制品、工作区、出厂资产包的挂载在 M1 不使用（[11](../10-architecture/11-components-and-boundaries.md) 第 5.3 节）。M1 凭证库最小版的位置由 L04.4 定。
- `db-init` 的脚本来自部署所用的源码检出目录，不打进镜像。
- 准备脚本按本表建目录、设属主和权限，再核对。缺失或不符就退出，不启动容器（12 第 3 节）。

### 8.3 环境变量

| 变量 | 用途 | 接收者 |
|---|---|---|
| `DEVOPS_LLM_BASE_URL`、`DEVOPS_LLM_API_KEY` | 模型接入变量 | 只有 `runtime` 服务 |
| `POSTGRES_PASSWORD_FILE=/run/secrets/postgres/superuser-password` | PostgreSQL 官方镜像初始化超级用户 | 只有 `postgres` 服务 |
| `DATA_ROOT`、`INSTANCE_ID`、`CORE_BIND`、镜像 digest | Compose 插值，只用于生成服务定义 | 不进入任何容器 |

- 本项目的程序只读模型接入变量（[11](../10-architecture/11-components-and-boundaries.md) 第 5.4 节）。`POSTGRES_PASSWORD_FILE` 是第三方镜像的变量，值是路径，不是口令。
- `.env` 只放在部署目录，权限 0600，不进仓库。仓库只提供 `.env.example`，只有变量名。

## 9. HTTP 资源限制

| 项 | core 公开接口 | core 内部接口 | 模型出口代理 |
|---|---|---|---|
| `MaxHeaderBytes` | 16 KiB | 16 KiB | 16 KiB |
| `ReadHeaderTimeout` | 5s | 5s | 5s |
| `ReadTimeout`（含请求体） | 15s | 15s | 30s |
| 请求体上限 | 1 MiB | 4 MiB | 4 MiB（`upstream.max_request_bytes`） |
| `WriteTimeout` | 30s | 30s | 不设（0），见下 |
| 单次写阻塞时限 | — | — | 30s |
| 单个响应总时限 | 30s | 30s | 非流式由上游超时约束；流式 10 分钟 |
| `IdleTimeout` | 60s | 60s | 60s |
| 并发上限（在途请求） | 128 | 32 | 16 |
| 超过并发上限 | 立即返回 503 和 `http.busy`，不排队 | 同左 | 同左 |
| 关闭宽限 | 15s | 15s | 15s |

- 每项都是对应配置对象的字段（`max_header_bytes`、`read_header_timeout`、`read_timeout`、`max_body_bytes`、`write_timeout`、`write_block_timeout`、`stream_max_duration`、`idle_timeout`、`max_in_flight`），默认值如表。
- 模型出口代理不设 `WriteTimeout`，否则合法的流式响应会被截断。装配层：
  - 每次写出和 flush 之前，用 `http.ResponseController.SetWriteDeadline` 设 30 秒。客户端不读、写阻塞超时就中断连接。
  - 用请求 context 的 10 分钟期限约束流式总时长。期限到达时 modelgw 按取消处理上游请求（62 第 5.2 节）。
  - 上游空闲超时仍由 `upstream.timeout` 控制。
- 内部接口如果用长轮询领取任务，单次等待必须小于 `write_timeout`（L04.2 定）。
- 并发上限用信号量实现，在读请求体之前占用。

## 10. 启动

### 10.1 顺序

```mermaid
flowchart LR
    A[准备宿主目录与密钥] --> B[up -d postgres]
    B --> C[db-init（首次）]
    C --> D[备份 + migrate plan/apply]
    D --> E[up -d runtime core]
```

- `core` 在 Compose 里 `depends_on: postgres`，条件 `service_healthy`。postgres 的健康检查用 `pg_isready`。
- `core` 不依赖 `runtime`。代理不在时 Worker 把模型报告为不可用，核心页面照常可用。
- `core` 不依赖 `migrate`。迁移由管理员显式运行，服务启动时不迁移（[12](../10-architecture/12-deployment-substrate.md) 第 6.3 节）。

### 10.2 启动时的检查

| 进程 | 检查 | 不通过 |
|---|---|---|
| core 入口进程 | 环境里没有模型接入变量 | 非 0 退出，`config.forbidden_env` |
| 核心控制服务 | 配置文件合法；密钥文件可读 | 非 0 退出，`config.invalid` / `config.secret_unreadable` |
| 核心控制服务 | 连接数据库，最多重试 30 秒 | 非 0 退出，`storage.unavailable` |
| 核心控制服务 | 所需库的迁移版本等于本程序编译时的期望版本 | 非 0 退出，`storage.schema_mismatch`，信息写库名、期望版本、实际版本 |
| 核心控制服务 | 恢复处理（`recovery.OnStartup`）完成之前，内部接口不交出任务 | — |
| Agent Worker | 配置文件合法；代理 token 文件可读；环境里没有模型接入变量 | 非 0 退出 |
| 模型出口代理 | 配置文件合法；客户端 token 文件可读 | 非 0 退出 |
| 模型出口代理 | 模型接入变量缺失或无效 | 不退出；拒绝模型请求，`/v1/status` 报告 `missing` 或 `invalid` |

- 期望版本是迁移集里每个库的最大版本号，编译时由内嵌迁移文件得出。库版本较旧（缺迁移）或较新（程序比库旧）都拒绝启动。
- 恢复处理和恢复报告的内容由 L04.2 定。

## 11. 停止、退出与重启

### 11.1 重启策略

M1 所有服务用 `restart: "no"`。Docker Engine 不自动重启任何容器。

- 12 第 4 节要求重启策略带次数上限、超过上限由监控旁路告警，并把已审核的重启策略视为预授权（K-11）。M1 没有监控旁路，也没有预授权的有效期核验。`unless-stopped` 没有次数上限，`on-failure:N` 有上限但无法满足其余要求。
- 所以 M1 关闭自动重启：故障后由管理员核对，再手动重启。这是 M1 的运行限制，宿主重启后也要手动 `up -d`。
- 已核实：Compose v2.40.3、v5.1.0 在非 Swarm 下只把 `deploy.restart_policy` 的 `condition`、`max_attempts` 映射为重启策略，不映射 `delay`、`window`。以后启用自动重启时，不能用这两个字段承诺重启间隔。

### 11.2 Worker 不单独重启

- Worker 是 core 容器的子进程。Worker 退出时，core 入口进程停止核心控制服务并退出，整个 core 容器停止（第 4.2 节）。
- 管理员重启 core 容器后进入恢复模式：未完成的对话任务标为"已停止（恢复中断）"，不自动续跑（K-24、Q5）。
- M1 不提供单独启停 Worker 的操作。12 第 6.2 节的"按组件分别操作"在 M1 指按容器操作。

### 11.3 停用 Worker 消费

把 `agent-worker.json` 的 `concurrency` 设为 `0`，重启 core 容器。

- Worker 进程存活，不领取任务。核心页面、项目、会话、历史消息照常可用；新消息显示"等待处理"。
- 这是"停止 Worker"的 M1 做法，供 L14 验收第 6 项使用。它和"Worker 进程崩溃"是两个不同的验收项。

### 11.4 人工恢复步骤

1. `docker compose -p devops-agent ps` 确认停止的服务。
2. `docker compose -p devops-agent logs --tail 200 <服务>` 查看原因。日志已经 mask。
3. 修正原因，`docker compose -p devops-agent up -d <服务>`。
4. 查看恢复报告（接口由 L04.2 定），人工决定是否重新发起任务。

## 12. 数据库引导与受控迁移

### 12.1 数据库与账号

| 账号 | 用途 | 持有者 |
|---|---|---|
| `postgres` | 超级用户。只用于 db-init 和备份 | postgres 服务、db-init |
| `devops_migrate` | 各库的属主，执行迁移 | migrate |
| `devops_core` | 核心控制服务的运行账号 | 核心控制服务 |

- M1 的库：`core`、`harness`、`config`、`audit`。
- `devops_core` 在各库的权限矩阵由 L04.2 定。审计库只能追加（[62](62-provider-storage-modelgw.md) 第 3.1 节）。
- runtime 在 M1 不持有任何数据库账号。

### 12.2 db-init（首次引导）

`db-init` 用 PostgreSQL 官方镜像，运行 `platform/deploy/db-init/db-init.sh`（L13.2）：

1. 用超级用户口令连接 postgres 服务。
2. 账号不存在就创建；存在就把口令设为口令文件里的值。
3. 库不存在就创建，属主 `devops_migrate`。
4. 收回 `PUBLIC` 在各库上的 `CONNECT` 和 `public` schema 的 `CREATE`。
5. 可重复运行，结果相同。不删除任何账号、库或数据。

### 12.3 迁移

迁移程序 `migrate`（L05）只做迁移，只持有迁移账号。它分两步：先生成计划，再按计划执行。

| 命令 | 行为 |
|---|---|
| `migrate plan` | 只读。逐库输出：库名、当前版本、待执行文件名及其 SHA-256、整体计划摘要。不创建任何对象，包括版本表 |
| `migrate apply --plan <摘要> --backup-ref <文本>` | 重新计算计划。摘要不同就拒绝，`migrate.plan_mismatch`。相同就逐库、逐文件执行 |

- 计划摘要是对"库名、当前版本、待执行文件名和 SHA-256"有序列表的 SHA-256。它绑定管理员看过的计划：预览后新增文件、文件内容变化、库版本变化都会让摘要不同。
- 每个库有版本表 `schema_migrations`：版本号、文件名、文件 SHA-256、执行时间、`backup_ref`。
- 每个文件在一个事务里执行，同一事务写版本表。文件不能自己提交事务。
- 已执行文件的 SHA-256 与内嵌文件不同：拒绝执行，`migrate.digest_mismatch`。
- 库版本比内嵌迁移集还新：拒绝执行，`migrate.unsupported_state`。
- 同一库同时只有一个迁移：执行前取该库的 `pg_try_advisory_lock`，取不到就拒绝，`migrate.locked`。
- 某个文件失败：停止，已提交的文件保留，不自动回滚，不自动重试。管理员处理后重新 `plan`。
- 跨库不保证原子性。
- 连接失败以非 0 退出，`storage.unavailable`，错误信息里没有口令。

### 12.4 管理员流程

1. 核对目标：`docker compose -p devops-agent ps`，确认是本项目的服务。
2. 停止应用写入：`docker compose -p devops-agent stop core runtime`。postgres 保持运行。
3. 备份：运行 L13.2 的备份脚本，记下它输出的备份标识。
4. 预览：`docker compose -p devops-agent --profile migrate run --rm migrate plan`。
5. 核对计划。
6. 执行：`docker compose -p devops-agent --profile migrate run --rm migrate apply --plan <摘要> --backup-ref <备份标识>`。
7. 启动：`docker compose -p devops-agent up -d runtime core`。core 启动时核对版本（第 10.2 节）。

- 这是 M1 的人工受控迁移：检验（plan）→ 人工核对 → 绑定计划执行 → 结果核对（core 启动检查）。底座阶段由底座在受控执行中调用同一程序（[12](../10-architecture/12-deployment-substrate.md) 第 6.3 节）。
- 迁移不经平台 API，Agent 不能发起迁移。

## 13. 新增错误码

下游任务把这些码加进 `platform/internal/schema/errcode`。

| 错误码 | 含义 | 加入者 |
|---|---|---|
| `config.invalid` | 配置文件缺失、格式错、未知字段、取值越界 | L05（首个使用者） |
| `config.secret_unreadable` | 密钥文件缺失、不可读、为空或格式错 | L05 |
| `http.busy` | 在途请求达到并发上限 | L06.1 |
| `storage.schema_mismatch` | 库的迁移版本与程序期望不同 | L08.4 |
| `migrate.plan_mismatch` | 执行时的计划摘要与给定摘要不同 | L05 |
| `migrate.digest_mismatch` | 已执行文件的 SHA-256 与内嵌文件不同 | L05 |
| `migrate.unsupported_state` | 库版本比内嵌迁移集新 | L05 |
| `migrate.locked` | 同一库有其他迁移在执行 | L05 |

入口进程的失败只用退出码和日志表达，不新增错误码。

## 14. 验证过程防泄密

- `docker compose config`、`docker inspect`、`docker compose exec ... env` 会输出展开后的变量值，可能含真实 key。
- L13 的检查只用假值（例如 `DEVOPS_LLM_API_KEY=test-not-a-key`）。
- L14 在测试机上不打印这些命令的完整输出。检查写成脚本，只输出结论，例如"core 环境里没有 `DEVOPS_LLM_` 开头的变量：是"。
- 日志检查用 `grep -c` 计数，不回显匹配行。

## 15. 下游任务

### 15.1 L05 `cmd/migrate`

| 项 | 内容 |
|---|---|
| 范围 | 第 7.2 节 `migrate.json`；第 12.3 节两条命令；版本表；第 13 节 `config.*`、`migrate.*` 错误码 |
| 测试 | 1. 空库 `plan` 列出全部文件，库里不出现任何新对象（执行前后比对 `pg_catalog`）。2. `apply` 后表全部存在，版本表记录完整。3. 再 `plan` 为空，再 `apply` 无变化，退出码 0。4. `plan` 后新增一个迁移文件，用旧摘要 `apply` 被拒绝。5. 改一个已执行文件，被拒绝。6. 库版本比迁移集新，被拒绝。7. 两个 `apply` 同时运行，一个被拒绝。8. 文件中途失败，之前的文件保留，失败文件没有版本记录。9. 连不上数据库，退出码非 0，输出里没有口令。10. 未知配置字段，`config.invalid` |
| 审核 | 只持有迁移账号；不被任何服务启动时调用；没有回滚；没有 `--force` 之类绕过摘要的选项 |

### 15.2 L06 `cmd/model-proxy` 与入口进程

| 项 | 内容 |
|---|---|
| L06.1 范围 | 第 7.2 节 `model-proxy.json`；第 9 节代理一列；`http.busy` |
| L06.1 测试 | 任务表 L06.1 的四项，另加：17 个并发请求时第 17 个立即得到 503 `http.busy`；流式响应超过 30 秒仍正常（不被 `WriteTimeout` 截断）；客户端停止读取时 30 秒内中断；流式超过 10 分钟被中断；未知配置字段拒绝启动 |
| L06.2 范围 | 第 4 节入口进程（`core`、`runtime` 两张进程表）；第 5.1 节对身份的要求 |
| L06.2 测试 | 1. 子进程启动失败时已启动的兄弟进程被停止，入口非 0 退出。2. 子进程以 0 或非 0 退出，入口都非 0 退出，其余子进程被回收。3. SIGTERM 转发、20 秒后 SIGKILL、无遗留进程。4. 子进程的 UID、GID、附加组为空、能力集为空（Linux；非 Linux 跳过并说明）。5. 用测试子进程验证非代理子进程的环境没有 `DEVOPS_LLM_`，代理子进程只有这两个变量。测试子进程只在测试里构造，不进入发布的进程表。6. `entry core` 遇到模型接入变量拒绝启动，输出只有变量名 |
| 审核 | 进程表编译固定；无 shell；只用标准库；不读环境里的值进日志 |

### 15.3 L13 镜像与 Compose

| 项 | 内容 |
|---|---|
| L13.1 范围 | 第 3.1 节两个镜像 |
| L13.1 测试 | 任务表 L13.1 的四项。第 4 项改为：业务子进程非 root；入口进程按第 5.1 节的能力集运行 |
| L13.2 范围 | 第 3.2 节五个服务；第 5.1 节安全选项；第 6 节网络；第 8 节挂载、密钥和环境变量；宿主目录准备脚本 `prepare-host.sh`；`db-init.sh`；备份脚本 `backup.sh`（`pg_dump` 每个库到 `backup/`，输出备份标识）；`.env.example` |
| L13.2 测试 | 任务表 L13.2 的四项，另加：1. `docker compose -p devops-agent config` 显示所有服务 `restart: "no"`；只有 core 发布端口且绑定 `127.0.0.1`；`migrate` 没有 `DEVOPS_LLM_*`。2. 准备脚本重复运行不改已有密钥；权限不符时退出。3. 文件可读矩阵（第 5.2 节）在运行的容器里逐项核对。4. `db-init` 重复运行结果相同。5. 发布端口在 `edge` 网络上可访问（第 6.1 节）。6. runtime 能访问外网，core 的 `app` 网络没有外网出口。检查全部用假 key（第 14 节） |
| L13.3 | 把上述检查写进 `verify.sh`，CI 运行 |
| 审核 | 镜像只含程序；没有 `latest`；`.env` 不进仓库；没有 Docker socket 挂载；一次性服务不继承 runtime 的环境 |

### 15.4 L14 的调整

- 第 3 项：除入口进程和代理外，runtime 里没有其他进程；入口进程的初始环境里有模型接入变量是已知例外（第 4.3 节）。
- 第 6 项：按第 11.3 节停用 Worker 消费。
- 部署步骤第 5 步：按第 12.4 节运行 `db-init` 和迁移。
- 所有检查按第 14 节只输出结论。

## 16. 审核清单（第一部分）

- [ ] 54 有"P1 的 M1 子集"表，后移项没有被写成已通过。
- [ ] 镜像、Compose 服务、进程三张表互相一致；`migrate` 不常驻。
- [ ] 每个进程的 UID、GID、能力、配置、密钥文件、环境变量都写明。
- [ ] 监听地址、宿主绑定、网络归属写明；8081 只预留。
- [ ] 每个配置文件的 `schema`、字段、默认值、非法值处理写明。
- [ ] 三个 HTTP 服务的资源限制都有具体值，流式响应单独处理。
- [ ] 启动依赖、启动检查、恢复完成前不交出任务写明。
- [ ] 退出联动、信号、宽限、重启策略、人工恢复步骤写明。
- [ ] Worker 并发为 0 与 Worker 崩溃分开写。
- [ ] 挂载表、密钥文件属主与权限、目录权限写明，并已同步到 11 第 5.3 节。
- [ ] 迁移计划绑定、备份、执行、失败处理写明，并已同步到 12 第 6.3 节、53 第 4.1 节。
- [ ] L05、L06、L13 的范围、测试、审核清单写明；任务表已同步。
- [ ] 没有修改 1.0 目标和内核规则。

## 17. 采用的决定

| 决定 | 理由 |
|---|---|
| M1 用 Compose；底座、SDK 的 ContextPatch、压缩、Hook 执行、子 Agent 后移；54 记录对应表 | M1 只需对话、工具执行为零。缩小范围不降低已开放能力的要求（K-29） |
| 入口进程用标准库 Go，进程表编译固定 | tini 不能多身份启动；supervisord、s6 增加依赖准入和第二个重启者的风险 |
| 入口进程以受限 root 运行（SETUID、SETGID、KILL） | 非 root 不能启动两个不同 UID 的子进程 |
| M1 runtime 只启动入口进程和代理；8081 预留 | 不为验证隔离而启动占位的生产进程 |
| 核心内部接口在 `127.0.0.1:8090` | 任务队列等内部接口不经宿主端口暴露 |
| 配置用 JSON 和标准库，拒绝未知字段 | 不引入新依赖；配置写错立即暴露 |
| 每个身份一个密钥子目录 | 目录要可进入；文件只给对应身份 |
| 代理 token 只给 Worker；模型状态由 Worker 报告 | 核心控制服务不持有访问模型通道的凭证 |
| 代理不设 `WriteTimeout`，改用单次写时限和流式总时限 | 固定 `WriteTimeout` 会截断合法的流式响应 |
| 迁移用 plan/apply 两步，apply 必须带计划摘要和备份标识 | 只检查已执行文件被改，不能证明执行的就是管理员核对过的计划 |

**改变已定设计的决定**（单独列出，原文档已同步修改）：

| 决定 | 原设计 | 修改位置 |
|---|---|---|
| M1 所有服务 `restart: "no"`，故障后人工重启 | 12 第 4 节：重启策略带次数上限，由监控旁路告警 | 12 第 4 节增加 M1 说明 |
| Worker 不单独重启；"停止 Worker"用并发 0 | 12 第 6.2 节："Worker 可以单独重启" | 12 第 6.2 节；11 第 3、5.1 节 |
| M1 迁移由管理员按 plan/apply 流程手动运行 | 12 第 6.3 节、51 第 3 节、53 第 4.1 节、56 第 3.2 节、62 第 1 节：由底座发起 | 这些位置改为按阶段区分 |
| `migrate` 打进 runtime 镜像 | 53 第 4.1 节：runtime 镜像没有 `migrate` | 53 第 4.1 节 |
| 密钥目录改为每身份子目录；增加代理 token 文件 | 11 第 5.3 节：`secrets/core/`、`secrets/runtime/` 只读 0400 | 11 第 5.3 节 |
| M1 只启动代理；入口进程初始环境例外写进验收 | 11 第 5.1 节四进程；54 P1"其他进程环境没有模型接入变量" | 11 第 5.1、5.4 节；54 P1 |
| M1 是 P1 的子集 | 54 P1 含底座和 SDK 高级能力；56 第 3.2 节先做底座 | 54 第 3 节；56 第 3.2 节 |

以上都不改变 1.0 目标，不修改内核规则。
