#!/usr/bin/env bash
# 云端会话启动钩子：准备 Go 工具链和依赖，让 scripts/check.sh 能直接运行。
# 只在云端会话运行。本地开发者自己管理环境。
set -euo pipefail

if [[ "${CLAUDE_CODE_REMOTE:-}" != "true" ]]; then
  exit 0
fi

cd "${CLAUDE_PROJECT_DIR:-$(pwd)}"

# 平台 Go 版本基线见 docs/50-engineering/51-go-stack.md 第 2 节。
export GOTOOLCHAIN="go1.27.2+auto"
export GOWORK=off
if [[ -n "${CLAUDE_ENV_FILE:-}" ]]; then
  {
    echo 'export GOTOOLCHAIN="go1.27.2+auto"'
    echo 'export GOWORK=off'
  } >> "$CLAUDE_ENV_FILE"
fi

# 下载并缓存工具链（经 proxy.golang.org）。
go version

# 用 Git 枚举模块，排除不在设计范围内的 hermes/ 和测试语料目录。
git rev-parse --is-inside-work-tree >/dev/null
while IFS= read -r -d '' go_mod; do
  if [[ "$go_mod" == "go.mod" ]]; then
    mod="."
  else
    mod="${go_mod%/go.mod}"
  fi
  echo "go mod download: ${mod#./}"
  (cd "$mod" && go mod download)
done < <(git ls-files -z --cached --others --exclude-standard -- go.mod '*/go.mod' ':(exclude)hermes/**' ':(exclude)**/testdata/**')
