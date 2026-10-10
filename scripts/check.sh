#!/usr/bin/env bash
# 本地快速检查。Agent、开发者和 CI 都运行这个脚本。
# 检查项来自 docs/50-engineering/55-testing-and-release.md 第 2 节"每次变更"。
#
# 用法：
#   scripts/check.sh          # gofmt、go vet、go test -race
#   scripts/check.sh --vuln   # 另外运行 govulncheck（需要能访问 vuln.go.dev）
#
# 每个 Go 模块单独检查，GOWORK=off（见 53-repo-layout.md 第 1 节）。
# 枚举 Git 管理和未忽略的新模块，不读取设计范围外的 hermes/。
# 仓库里还没有 go.mod 时，脚本说明这一点并以 0 退出。
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"

run_vuln=false
if [[ "${1:-}" == "--vuln" ]]; then
  run_vuln=true
fi

export GOWORK=off
export GOTOOLCHAIN="${GOTOOLCHAIN:-go1.27.2+auto}"

# macOS 的 /bin/bash 是 3.x，没有 mapfile。read -d '' 可安全处理带空格的路径。
git rev-parse --is-inside-work-tree >/dev/null
modules=()
while IFS= read -r -d '' go_mod; do
  if [[ "$go_mod" == "go.mod" ]]; then
    modules+=(".")
  else
    modules+=("${go_mod%/go.mod}")
  fi
done < <(git ls-files -z --cached --others --exclude-standard -- go.mod '*/go.mod' ':(exclude)hermes/**' ':(exclude)**/testdata/**')

if [[ ${#modules[@]} -eq 0 ]]; then
  echo "check: 没有找到 go.mod，跳过 Go 检查。"
  exit 0
fi

failed=0
for mod in "${modules[@]}"; do
  echo "== ${mod#./}"
  pushd "$mod" >/dev/null

  # 用模块所选工具链自带的 gofmt，不用系统 PATH 里的旧版本。
  if ! unformatted="$("$(go env GOROOT)/bin/gofmt" -l .)"; then
    failed=1
  fi
  if [[ -n "$unformatted" ]]; then
    echo "gofmt: 以下文件需要格式化："
    echo "$unformatted"
    failed=1
  fi

  if ! packages="$(go list ./...)"; then
    failed=1
    popd >/dev/null
    continue
  fi
  if [[ -z "$packages" ]]; then
    echo "check: 没有 Go 包，跳过 go vet、go test -race 和 govulncheck。"
    popd >/dev/null
    continue
  fi

  go vet ./... || failed=1
  go test -race -count=1 ./... || failed=1

  if $run_vuln; then
    go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./... || failed=1
  fi

  popd >/dev/null
done

exit "$failed"
