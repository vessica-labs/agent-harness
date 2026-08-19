#!/usr/bin/env python3
"""Preview or install Agent Harness bootstrap files into a Git repository."""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import tempfile
from pathlib import Path
from typing import Any


def emit(value: Any) -> None:
    print(json.dumps(value, indent=2, sort_keys=True))


def quoted(value: str) -> str:
    return json.dumps(value)


def render_config(args: argparse.Namespace) -> bytes:
    child = "null" if args.provider == "linear" else quoted(args.child_issue_type)
    automation = (
        "automation:\n"
        "  enabled: true\n"
        "  trigger:\n"
        "    provider: linear\n"
        "    type: label\n"
        f"    label: {quoted(args.trigger_label)}\n"
    ) if args.provider == "linear" else ""
    return (
        "version: 1\n"
        "tracker:\n"
        f"  provider: {args.provider}\n"
        f"  workspace: {quoted(args.workspace)}\n"
        f"  project: {quoted(args.project)}\n"
        f"  child_issue_type: {child}\n"
        "notion:\n"
        f"  parent_page_id: {quoted(args.notion_parent_page_id)}\n"
        "git:\n"
        f"  remote: {quoted(args.remote)}\n"
        f"  base_branch: {quoted(args.base_branch)}\n"
        + automation
    ).encode()


def atomic_write(path: Path, content: bytes) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, temporary = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    try:
        with os.fdopen(fd, "wb") as handle:
            handle.write(content)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def collect_files(plugin_root: Path, target: Path, config: bytes) -> list[tuple[Path, bytes]]:
    source = plugin_root / "assets" / "harness" / "base"
    if not source.is_dir():
        raise RuntimeError(f"packaged harness assets are missing: {source}")
    files: list[tuple[Path, bytes]] = []
    for path in sorted(source.rglob("*")):
        if path.is_file():
            relative = path.relative_to(source)
            # AGENTS.md is the repository entry point Codex discovers. Keep its
            # canonical template beside the other guidance assets, but install
            # it at the repository root.
            destination = Path("AGENTS.md") if relative == Path(".harness/AGENTS.md") else relative
            files.append((target / destination, path.read_bytes()))
    files.append((target / ".harness" / "config.yaml", config))
    files.append((target / ".harness" / "pipeline.yaml", (plugin_root / "pipelines" / "default.yaml").read_bytes()))
    return files


def plan_operations(files: list[tuple[Path, bytes]]) -> list[dict[str, Any]]:
    operations: list[dict[str, Any]] = []
    for path, content in files:
        if not path.exists():
            action = "create"
        elif path.read_bytes() == content:
            action = "unchanged"
        else:
            action = "conflict"
        operations.append({"path": str(path), "action": action, "bytes": len(content)})
    return operations


def run(command: list[str], cwd: Path) -> dict[str, Any]:
    process = subprocess.run(command, cwd=cwd, text=True, capture_output=True, check=False)
    return {
        "command": command,
        "ok": process.returncode == 0,
        "exit_code": process.returncode,
        "stdout": process.stdout.strip(),
        "stderr": process.stderr.strip(),
    }


def preflight(args: argparse.Namespace) -> int:
    target = Path(args.target).resolve()
    checks = {
        "git_repository": run(["git", "rev-parse", "--show-toplevel"], target),
        "git_remote": run(["git", "remote", "get-url", args.remote], target),
        "base_branch": run(["git", "rev-parse", "--verify", f"refs/remotes/{args.remote}/{args.base_branch}"], target),
        "github_auth": run(["gh", "auth", "status"], target),
    }
    ok = all(check["ok"] for check in checks.values())
    emit({"ok": ok, "checks": checks})
    return 0 if ok else 1


def bootstrap(args: argparse.Namespace) -> int:
    if args.provider == "jira" and not args.child_issue_type:
        raise RuntimeError("--child-issue-type is required for Jira")
    target = Path(args.target).resolve()
    plugin_root = Path(__file__).resolve().parent.parent
    files = collect_files(plugin_root, target, render_config(args))
    operations = plan_operations(files)
    conflicts = [operation for operation in operations if operation["action"] == "conflict"]
    if not args.apply:
        emit({"ok": True, "mode": "preview", "target": str(target), "operations": operations, "requires_force": bool(conflicts)})
        return 0
    if conflicts and not args.force:
        emit({"ok": False, "mode": "apply", "error": "existing files differ; preview and explicitly use --force to replace them", "conflicts": conflicts})
        return 1
    for path, content in files:
        if path.exists() and path.read_bytes() == content:
            continue
        if path.exists() and not args.force:
            continue
        atomic_write(path, content)
    emit({"ok": True, "mode": "apply", "target": str(target), "operations": operations})
    return 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)

    check = subparsers.add_parser("preflight")
    check.add_argument("--target", required=True)
    check.add_argument("--remote", default="origin")
    check.add_argument("--base-branch", default="main")

    install = subparsers.add_parser("bootstrap")
    install.add_argument("--target", required=True)
    install.add_argument("--provider", choices=("linear", "jira"), required=True)
    install.add_argument("--workspace", required=True)
    install.add_argument("--project", required=True)
    install.add_argument("--child-issue-type")
    install.add_argument("--notion-parent-page-id", required=True)
    install.add_argument("--remote", default="origin")
    install.add_argument("--base-branch", default="main")
    install.add_argument("--trigger-label", default="agent-harness")
    install.add_argument("--apply", action="store_true")
    install.add_argument("--force", action="store_true")
    return parser


def main() -> None:
    args = build_parser().parse_args()
    try:
        if args.command == "preflight":
            raise SystemExit(preflight(args))
        raise SystemExit(bootstrap(args))
    except (OSError, RuntimeError, subprocess.SubprocessError) as exc:
        emit({"ok": False, "error": str(exc)})
        raise SystemExit(1)


if __name__ == "__main__":
    main()
