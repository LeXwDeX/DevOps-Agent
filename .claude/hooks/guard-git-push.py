#!/usr/bin/env python3
"""PreToolUse 钩子：在 Bash 命令运行前检查 git push。

拒绝：推送 main、强推、删除远端分支、--mirror、--all。
这只防误操作，不是安全边界（见 AGENTS.md 第 7 节）。
退出码 2 表示拒绝，原因写到 stderr。
"""
import json
import shlex
import subprocess
import sys

PROTECTED = {"main", "refs/heads/main"}
BAD_FLAGS = {"-f", "--force", "--mirror", "--all", "--delete", "-d", "--prune"}
BAD_FLAG_PREFIXES = ("--force-with-lease", "--force-if-includes")
SEPARATORS = {"&&", "||", ";", "|", "&"}
# 带参数的 git 全局选项，参数要跳过。
GIT_OPTS_WITH_ARG = {"-C", "-c", "--git-dir", "--work-tree", "--namespace"}
PUSH_OPTS_WITH_ARG = {"--repo", "--receive-pack", "--exec", "-o", "--push-option", "--recurse-submodules"}


def segments(tokens):
    seg = []
    for t in tokens:
        if t in SEPARATORS:
            if seg:
                yield seg
            seg = []
        else:
            seg.append(t)
    if seg:
        yield seg


def current_branch(cwd):
    try:
        out = subprocess.run(["git", "rev-parse", "--abbrev-ref", "HEAD"], cwd=cwd,
                             capture_output=True, text=True, timeout=5)
        return out.stdout.strip()
    except Exception:
        return ""


def check_push(args, cwd):
    """args 是 push 之后的参数。返回拒绝原因，或空字符串。"""
    positional = []
    i = 0
    while i < len(args):
        a = args[i]
        if a in BAD_FLAGS or a.startswith(BAD_FLAG_PREFIXES):
            return f"不允许 git push {a}"
        if a.startswith("-") and not a.startswith("--") and len(a) > 2 and ("f" in a[1:] or "d" in a[1:]):
            return f"不允许 git push {a}（含 -f 或 -d）"
        if a in PUSH_OPTS_WITH_ARG:
            i += 2
            continue
        if not a.startswith("-"):
            positional.append(a)
        i += 1
    refspecs = positional[1:]
    if not refspecs:
        if current_branch(cwd) in ("main", "HEAD", ""):
            return "当前分支是 main（或无法确定），不允许不带 refspec 的 git push"
        return ""
    for r in refspecs:
        if r.startswith("+"):
            return f"不允许强推 refspec {r}"
        if r.startswith(":"):
            return f"不允许删除远端分支 {r}"
        dst = r.split(":", 1)[1] if ":" in r else r
        if dst in PROTECTED or (dst == "HEAD" and current_branch(cwd) == "main"):
            return f"不允许推送到 main（{r}）"
    return ""


def main():
    try:
        data = json.load(sys.stdin)
    except Exception:
        return 0
    if data.get("tool_name") != "Bash":
        return 0
    cmd = (data.get("tool_input") or {}).get("command", "")
    if "push" not in cmd:
        return 0
    cwd = data.get("cwd") or None
    try:
        tokens = shlex.split(cmd, posix=True)
    except ValueError:
        if "git" in cmd and "push" in cmd:
            print("无法解析含 git push 的命令，已拒绝。请拆成简单命令。", file=sys.stderr)
            return 2
        return 0
    for seg in segments(tokens):
        if not seg or seg[0] != "git":
            continue
        j = 1
        while j < len(seg) and seg[j].startswith("-"):
            j += 2 if seg[j] in GIT_OPTS_WITH_ARG else 1
        if j < len(seg) and seg[j] == "push":
            reason = check_push(seg[j + 1:], cwd)
            if reason:
                print(f"guard-git-push: {reason}。规则见 AGENTS.md 第 6 节。", file=sys.stderr)
                return 2
    return 0


if __name__ == "__main__":
    sys.exit(main())
