#!/usr/bin/env python3
"""Deterministic local primitives for the Agent Harness Codex plugin."""

from __future__ import annotations

import argparse
import copy
import datetime as dt
import json
import os
import re
import secrets
import subprocess
import sys
import tempfile
from pathlib import Path
from typing import Any


STAGE_ALIASES = {
    "architect": "arch",
    "architecture": "arch",
    "coding": "coder",
    "code": "coder",
    "documentation": "docs",
    "pull-request": "pr",
    "pull_request": "pr",
}
TERMINAL_RUN_STATUSES = {"completed"}
STAGE_STATUSES = {"pending", "running", "ready", "blocked", "completed", "skipped"}
PRD_HEADINGS = [
    "# PRD:",
    "## Summary",
    "## Problem",
    "## Goals",
    "## Non-Goals",
    "## Scope",
    "## Requirements",
    "## Product and UI/UX Direction",
    "## Acceptance Criteria",
    "## Constraints and Dependencies",
    "## Risks and Assumptions",
]
ADR_HEADINGS = [
    "# ADR:",
    "## Context",
    "## Decision Drivers",
    "## Decision",
    "## Consequences",
    "## Alternatives Considered",
    "## Ticket Constraints",
]
GENERIC_AGENT_CONTRACTS = {
    "coder": ({"completed", "blocked"}, {"ticket_key", "commit", "files_changed", "tdd", "checks", "worktree_clean", "blocker", "residual_risks"}),
    "lint": ({"passed", "blocked"}, {"commits", "gates", "worktree_clean", "blocker", "residual_risks"}),
    "qa": ({"passed", "requeue", "blocked"}, {"acceptance_results", "commits", "new_tickets", "worktree_clean", "blocker", "residual_risks"}),
    "docs": ({"completed", "blocked"}, {"commits", "documents", "external_documents", "checks", "worktree_clean", "blocker", "residual_risks"}),
    "pr": ({"created", "blocked"}, {"base", "head", "rebase", "checks", "push", "pull_request", "worktree_clean", "blocker", "residual_risks"}),
}


class HarnessError(Exception):
    pass


def utc_now() -> str:
    return dt.datetime.now(dt.timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def emit(value: Any) -> None:
    print(json.dumps(value, indent=2, sort_keys=True))


def fail(message: str, *, details: Any = None) -> None:
    payload: dict[str, Any] = {"ok": False, "error": message}
    if details is not None:
        payload["details"] = details
    emit(payload)
    raise SystemExit(1)


def read_json(path: Path) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise HarnessError(f"file not found: {path}") from exc
    except json.JSONDecodeError as exc:
        raise HarnessError(f"invalid JSON in {path}: {exc}") from exc


def atomic_write_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, temporary = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            json.dump(value, handle, indent=2, sort_keys=True)
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def atomic_write_text(path: Path, value: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, temporary = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            handle.write(value)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def _strip_yaml_comment(value: str) -> str:
    quote: str | None = None
    escaped = False
    for index, char in enumerate(value):
        if escaped:
            escaped = False
            continue
        if char == "\\" and quote == '"':
            escaped = True
            continue
        if char in {'"', "'"}:
            if quote == char:
                quote = None
            elif quote is None:
                quote = char
        elif char == "#" and quote is None and (index == 0 or value[index - 1].isspace()):
            return value[:index].rstrip()
    return value.rstrip()


def _yaml_scalar(value: str) -> Any:
    value = value.strip()
    if value == "":
        return None
    if value in {"null", "Null", "NULL", "~"}:
        return None
    if value.lower() in {"true", "false"}:
        return value.lower() == "true"
    if re.fullmatch(r"-?[0-9]+", value):
        return int(value)
    if value.startswith(("[", "{", '"')):
        try:
            return json.loads(value)
        except json.JSONDecodeError as exc:
            if value.startswith("[") and value.endswith("]"):
                inner = value[1:-1].strip()
                if not inner:
                    return []
                return [_yaml_scalar(item.strip()) for item in inner.split(",")]
            raise HarnessError(f"unsupported inline YAML value {value!r}: {exc}") from exc
    if value.startswith("'") and value.endswith("'"):
        return value[1:-1].replace("''", "'")
    return value


def load_simple_yaml(path: Path) -> Any:
    """Load the strict YAML subset used by harness config and pipeline files."""
    tokens: list[tuple[int, str, int]] = []
    for number, raw in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        if "\t" in raw[: len(raw) - len(raw.lstrip())]:
            raise HarnessError(f"tabs are not allowed for YAML indentation ({path}:{number})")
        cleaned = _strip_yaml_comment(raw)
        if not cleaned.strip() or cleaned.lstrip().startswith("---"):
            continue
        indent = len(cleaned) - len(cleaned.lstrip(" "))
        tokens.append((indent, cleaned.strip(), number))
    if not tokens:
        return {}

    def parse_block(position: int, indent: int) -> tuple[Any, int]:
        if position >= len(tokens):
            return {}, position
        if tokens[position][0] != indent:
            raise HarnessError(f"unexpected indentation at {path}:{tokens[position][2]}")
        if tokens[position][1].startswith("- ") or tokens[position][1] == "-":
            result: list[Any] = []
            while position < len(tokens) and tokens[position][0] == indent:
                content, line = tokens[position][1], tokens[position][2]
                if not (content.startswith("- ") or content == "-"):
                    break
                rest = content[1:].strip()
                position += 1
                if not rest:
                    if position >= len(tokens) or tokens[position][0] <= indent:
                        result.append(None)
                    else:
                        item, position = parse_block(position, tokens[position][0])
                        result.append(item)
                    continue
                if ":" not in rest:
                    result.append(_yaml_scalar(rest))
                    continue
                key, raw_value = rest.split(":", 1)
                item: dict[str, Any] = {}
                raw_value = raw_value.strip()
                if raw_value:
                    item[key.strip()] = _yaml_scalar(raw_value)
                elif position < len(tokens) and tokens[position][0] > indent + 1:
                    item[key.strip()], position = parse_block(position, tokens[position][0])
                else:
                    item[key.strip()] = {}
                if position < len(tokens) and tokens[position][0] == indent + 2 and not tokens[position][1].startswith("-"):
                    extra, position = parse_map(position, indent + 2)
                    item.update(extra)
                result.append(item)
            return result, position
        return parse_map(position, indent)

    def parse_map(position: int, indent: int) -> tuple[dict[str, Any], int]:
        result: dict[str, Any] = {}
        while position < len(tokens) and tokens[position][0] == indent:
            content, line = tokens[position][1], tokens[position][2]
            if content.startswith("-"):
                break
            if ":" not in content:
                raise HarnessError(f"expected key: value at {path}:{line}")
            key, raw_value = content.split(":", 1)
            key = key.strip()
            if not key or key in result:
                raise HarnessError(f"invalid or duplicate key at {path}:{line}")
            position += 1
            raw_value = raw_value.strip()
            if raw_value:
                result[key] = _yaml_scalar(raw_value)
            elif position < len(tokens) and tokens[position][0] > indent:
                result[key], position = parse_block(position, tokens[position][0])
            else:
                result[key] = {}
        return result, position

    parsed, final = parse_block(0, tokens[0][0])
    if final != len(tokens):
        raise HarnessError(f"could not parse YAML near {path}:{tokens[final][2]}")
    return parsed


def require_mapping(value: Any, name: str) -> dict[str, Any]:
    if not isinstance(value, dict):
        raise HarnessError(f"{name} must be a mapping")
    return value


def validate_config(config: Any) -> list[str]:
    errors: list[str] = []
    if not isinstance(config, dict):
        return ["config must be a mapping"]
    if config.get("version") != 1:
        errors.append("version must be 1")
    tracker = config.get("tracker")
    if not isinstance(tracker, dict):
        errors.append("tracker must be a mapping")
    else:
        if tracker.get("provider") not in {"linear", "jira"}:
            errors.append("tracker.provider must be linear or jira")
        for key in ("workspace", "project"):
            if not isinstance(tracker.get(key), str) or not tracker[key].strip():
                errors.append(f"tracker.{key} must be a non-empty string")
        if tracker.get("provider") == "jira" and not tracker.get("child_issue_type"):
            errors.append("tracker.child_issue_type is required for jira")
    notion = config.get("notion")
    if not isinstance(notion, dict) or not notion.get("parent_page_id"):
        errors.append("notion.parent_page_id is required")
    git = config.get("git")
    if not isinstance(git, dict):
        errors.append("git must be a mapping")
    else:
        for key in ("remote", "base_branch"):
            if not isinstance(git.get(key), str) or not git[key].strip():
                errors.append(f"git.{key} must be a non-empty string")
    return errors


def _safe_relative(path: str) -> bool:
    candidate = Path(path)
    return not candidate.is_absolute() and ".." not in candidate.parts


def _validate_file_entries(entries: Any, kind: str, prefix: str) -> list[str]:
    errors: list[str] = []
    if not isinstance(entries, list):
        return [f"{prefix}.{kind} must be a list"]
    seen: set[str] = set()
    for index, entry in enumerate(entries):
        entry_prefix = f"{prefix}.{kind}[{index}]"
        if not isinstance(entry, dict):
            errors.append(f"{entry_prefix} must be a mapping")
            continue
        entry_id = entry.get("id")
        if not isinstance(entry_id, str) or not re.fullmatch(r"[a-z][a-z0-9_]*", entry_id):
            errors.append(f"{entry_prefix}.id is invalid")
        elif entry_id in seen:
            errors.append(f"duplicate {kind} id in {prefix}: {entry_id}")
        else:
            seen.add(entry_id)
        path = entry.get("file")
        if not isinstance(path, str) or not path or not _safe_relative(path):
            errors.append(f"{entry_prefix}.file must be a safe run-relative path")
        if entry.get("format") not in {"json", "markdown", "text"}:
            errors.append(f"{entry_prefix}.format must be json, markdown, or text")
        if kind == "inputs":
            if not isinstance(entry.get("required"), bool):
                errors.append(f"{entry_prefix}.required must be boolean")
            sources = entry.get("sources")
            if sources is not None and (not isinstance(sources, list) or not sources or any(not isinstance(source, str) or not source for source in sources)):
                errors.append(f"{entry_prefix}.sources must be a non-empty string list")
            generated = entry.get("generated_from")
            if generated is not None:
                if not isinstance(generated, dict):
                    errors.append(f"{entry_prefix}.generated_from must be a mapping")
                else:
                    generated_path = generated.get("file")
                    if not isinstance(generated_path, str) or not generated_path or not _safe_relative(generated_path):
                        errors.append(f"{entry_prefix}.generated_from.file must be a safe run-relative path")
                    for key in ("collection", "key"):
                        if not isinstance(generated.get(key), str) or not generated[key]:
                            errors.append(f"{entry_prefix}.generated_from.{key} is required")
        elif not isinstance(entry.get("from_result"), str) or not entry["from_result"]:
            errors.append(f"{entry_prefix}.from_result is required")
    return errors


def validate_pipeline(pipeline: Any, repo: Path | None = None) -> list[str]:
    errors: list[str] = []
    if not isinstance(pipeline, dict):
        return ["pipeline must be a mapping"]
    if pipeline.get("version") != 1:
        errors.append("version must be 1")
    if not isinstance(pipeline.get("name"), str) or not pipeline["name"].strip():
        errors.append("name must be a non-empty string")
    run_root = pipeline.get("run_root")
    if not isinstance(run_root, str) or not run_root or not _safe_relative(run_root) or Path(run_root).name != "{run_id}":
        errors.append("run_root must be a safe repo-relative path ending in {run_id}")
    defaults = pipeline.get("defaults")
    if not isinstance(defaults, dict):
        errors.append("defaults must be a mapping")
    else:
        if defaults.get("repository_access") is not True:
            errors.append("defaults.repository_access must be true")
        if defaults.get("result_format") != "json":
            errors.append("defaults.result_format must be json")
    stages = pipeline.get("stages")
    if not isinstance(stages, list) or not stages:
        return errors + ["stages must be a non-empty list"]
    seen: set[str] = set()
    for index, stage in enumerate(stages):
        prefix = f"stages[{index}]"
        if not isinstance(stage, dict):
            errors.append(f"{prefix} must be a mapping")
            continue
        stage_id = stage.get("id")
        if not isinstance(stage_id, str) or not re.fullmatch(r"[a-z][a-z0-9-]*", stage_id):
            errors.append(f"{prefix}.id is invalid")
            continue
        if stage_id in seen:
            errors.append(f"duplicate stage id: {stage_id}")
        needs = stage.get("needs")
        if not isinstance(needs, list) or any(not isinstance(item, str) for item in needs):
            errors.append(f"{prefix}.needs must be a list of stage ids")
            needs = []
        for needed in needs:
            if needed not in seen:
                errors.append(f"{stage_id} depends on missing or later stage {needed}")
        agent = stage.get("agent")
        if not isinstance(agent, str) or not agent.startswith(".agents/") or not agent.endswith(".md") or not _safe_relative(agent):
            errors.append(f"{prefix}.agent must be a safe .agents/*.md path")
        elif repo is not None and not (repo / agent).is_file():
            errors.append(f"agent file does not exist: {agent}")
        mode = stage.get("mode")
        if mode not in {"single", "ticket_parallel"}:
            errors.append(f"{prefix}.mode must be single or ticket_parallel")
        parallelism = stage.get("parallelism")
        if not isinstance(parallelism, int) or isinstance(parallelism, bool) or not 1 <= parallelism <= 16:
            errors.append(f"{prefix}.parallelism must be an integer from 1 to 16")
        elif mode == "single" and parallelism != 1:
            errors.append(f"{prefix}.parallelism must be 1 for single mode")
        errors.extend(_validate_file_entries(stage.get("inputs"), "inputs", prefix))
        errors.extend(_validate_file_entries(stage.get("outputs"), "outputs", prefix))
        result = stage.get("result")
        if not isinstance(result, dict):
            errors.append(f"{prefix}.result must be a mapping")
        else:
            result_path = result.get("file")
            if not isinstance(result_path, str) or not result_path or not _safe_relative(result_path):
                errors.append(f"{prefix}.result.file must be a safe run-relative path")
            if result.get("format") != "json":
                errors.append(f"{prefix}.result.format must be json")
            if result.get("agent") not in {"product", "architect", "coder", "lint", "qa", "docs", "pr"}:
                errors.append(f"{prefix}.result.agent is invalid")
            if mode == "ticket_parallel" and isinstance(result_path, str) and "{ticket_key}" not in result_path:
                errors.append(f"{prefix}.result.file must contain {{ticket_key}} for ticket_parallel mode")
        if mode == "ticket_parallel":
            generated_inputs = [entry for entry in stage.get("inputs", []) if isinstance(entry, dict) and "generated_from" in entry]
            if len(generated_inputs) != 1:
                errors.append(f"{prefix} ticket_parallel mode requires exactly one generated input")
        hooks = stage.get("hooks")
        if not isinstance(hooks, dict):
            errors.append(f"{prefix}.hooks must be a mapping")
        else:
            for timing in ("before", "after", "on_failure"):
                entries = hooks.get(timing)
                if not isinstance(entries, list):
                    errors.append(f"{prefix}.hooks.{timing} must be a list")
                    continue
                for hook_index, hook in enumerate(entries):
                    hook_prefix = f"{prefix}.hooks.{timing}[{hook_index}]"
                    if not isinstance(hook, dict):
                        errors.append(f"{hook_prefix} must be a mapping")
                        continue
                    if not isinstance(hook.get("id"), str) or not hook["id"]:
                        errors.append(f"{hook_prefix}.id is required")
                    argv = hook.get("argv")
                    if not isinstance(argv, list) or not argv or any(not isinstance(arg, str) for arg in argv):
                        errors.append(f"{hook_prefix}.argv must be a non-empty string list")
                    cwd = hook.get("cwd", ".")
                    if not isinstance(cwd, str) or not _safe_relative(cwd):
                        errors.append(f"{hook_prefix}.cwd must be repo-relative")
                    timeout = hook.get("timeout_seconds", 300)
                    if not isinstance(timeout, int) or isinstance(timeout, bool) or not 1 <= timeout <= 3600:
                        errors.append(f"{hook_prefix}.timeout_seconds must be 1..3600")
        seen.add(stage_id)
    loops = pipeline.get("repair_loops", [])
    if not isinstance(loops, list):
        errors.append("repair_loops must be a list")
    else:
        stage_ids = [stage.get("id") for stage in stages if isinstance(stage, dict)]
        for index, loop in enumerate(loops):
            if not isinstance(loop, dict):
                errors.append(f"repair_loops[{index}] must be a mapping")
                continue
            for key in ("from", "to", "through"):
                if loop.get(key) not in stage_ids:
                    errors.append(f"repair_loops[{index}].{key} must reference a stage")
            max_reentries = loop.get("max_reentries")
            if not isinstance(max_reentries, int) or isinstance(max_reentries, bool) or not 1 <= max_reentries <= 10:
                errors.append(f"repair_loops[{index}].max_reentries must be an integer from 1 to 10")
    return errors


def stage_order(pipeline: dict[str, Any]) -> list[str]:
    return [stage["id"] for stage in pipeline["stages"]]


def pipeline_stage(pipeline: dict[str, Any], stage_id: str) -> dict[str, Any]:
    for stage in pipeline["stages"]:
        if stage["id"] == stage_id:
            return stage
    raise HarnessError(f"stage not found in pipeline: {stage_id}")


def run_file(run_dir: Path, relative: str, variables: dict[str, str] | None = None) -> Path:
    rendered = relative
    for key, value in (variables or {}).items():
        if not re.fullmatch(r"[A-Za-z0-9._-]+", value):
            raise HarnessError(f"unsafe file placeholder value for {key}: {value}")
        rendered = rendered.replace("{" + key + "}", value)
    if "{" in rendered or "}" in rendered or "*" in rendered or "?" in rendered:
        raise HarnessError(f"file path contains an unresolved placeholder or glob: {relative}")
    if not _safe_relative(rendered):
        raise HarnessError(f"unsafe run-relative file path: {relative}")
    root = run_dir.resolve()
    resolved = (root / rendered).resolve()
    if resolved != root and root not in resolved.parents:
        raise HarnessError(f"file path escapes run directory: {relative}")
    return resolved


def extract_result(data: Any, selector: str) -> Any:
    if selector == "$":
        return data
    current = data
    for part in selector.split("."):
        if not isinstance(current, dict) or part not in current:
            raise HarnessError(f"result selector does not exist: {selector}")
        current = current[part]
    return current


def write_contract_file(path: Path, format_name: str, value: Any) -> None:
    if format_name == "json":
        atomic_write_json(path, value)
        return
    if not isinstance(value, str):
        raise HarnessError(f"{format_name} output must resolve to a string: {path}")
    atomic_write_text(path, value if value.endswith("\n") else value + "\n")


def resolve_stages(pipeline: dict[str, Any], requested: str, completed: set[str]) -> dict[str, Any]:
    order = stage_order(pipeline)
    raw = [item.strip().lower() for item in requested.split(",") if item.strip()]
    if not raw:
        raise HarnessError("at least one stage is required")
    if len(raw) == 1 and raw[0] in {"all", "full", "pipeline"}:
        selected = order
    else:
        normalized = [STAGE_ALIASES.get(item, item) for item in raw]
        unknown = sorted(set(normalized) - set(order))
        if unknown:
            raise HarnessError(f"unknown stages: {', '.join(unknown)}")
        selected = [stage for stage in order if stage in set(normalized)]
    by_id = {stage["id"]: stage for stage in pipeline["stages"]}
    missing: dict[str, list[str]] = {}
    for stage_id in selected:
        unmet = [need for need in by_id[stage_id].get("needs", []) if need not in selected and need not in completed]
        if unmet:
            missing[stage_id] = unmet
    return {"selected": selected, "missing_prerequisites": missing}


def dependency_waves(tickets: list[dict[str, Any]]) -> list[list[str]]:
    keys = [ticket.get("key") for ticket in tickets]
    if any(not isinstance(key, str) or not key for key in keys):
        raise HarnessError("every ticket requires a non-empty key")
    if len(keys) != len(set(keys)):
        raise HarnessError("ticket keys must be unique")
    remaining = {ticket["key"]: set(ticket.get("depends_on", [])) for ticket in tickets}
    unknown = sorted({dep for deps in remaining.values() for dep in deps if dep not in remaining})
    if unknown:
        raise HarnessError(f"unknown ticket dependencies: {', '.join(unknown)}")
    waves: list[list[str]] = []
    completed: set[str] = set()
    while remaining:
        ready = sorted(key for key, deps in remaining.items() if deps <= completed)
        if not ready:
            raise HarnessError(f"ticket dependency cycle: {', '.join(sorted(remaining))}")
        waves.append(ready)
        completed.update(ready)
        for key in ready:
            remaining.pop(key)
    return waves


def _paths_overlap(left: str, right: str) -> bool:
    left_path = left.rstrip("/")
    right_path = right.rstrip("/")
    return left_path == right_path or left_path.startswith(right_path + "/") or right_path.startswith(left_path + "/")


def _missing_or_out_of_order_headings(markdown: str, headings: list[str]) -> list[str]:
    missing: list[str] = []
    position = -1
    for heading in headings:
        found = markdown.find(heading, position + 1)
        if found < 0:
            missing.append(heading)
        else:
            position = found
    return missing


def validate_product_output(data: Any) -> list[str]:
    errors: list[str] = []
    if not isinstance(data, dict):
        return ["product output must be an object"]
    required = {"agent", "status", "source_issue", "prd_markdown", "tickets", "coverage", "blockers"}
    missing = sorted(required - set(data))
    if missing:
        errors.append(f"missing fields: {', '.join(missing)}")
    if data.get("agent") != "product":
        errors.append("agent must be product")
    if data.get("status") not in {"ready", "blocked"}:
        errors.append("status must be ready or blocked")
    if not isinstance(data.get("prd_markdown"), str):
        errors.append("prd_markdown must be a string")
    else:
        invalid_headings = _missing_or_out_of_order_headings(data["prd_markdown"], PRD_HEADINGS)
        if invalid_headings:
            errors.append(f"prd_markdown is missing or reorders required headings: {', '.join(invalid_headings)}")
    tickets = data.get("tickets")
    if not isinstance(tickets, list):
        return errors + ["tickets must be a list"]
    keys: set[str] = set()
    for index, ticket in enumerate(tickets):
        prefix = f"tickets[{index}]"
        if not isinstance(ticket, dict):
            errors.append(f"{prefix} must be an object")
            continue
        for key in ("key", "type", "title", "objective", "acceptance_criteria", "owned_paths", "depends_on", "focused_checks", "commit_message", "complexity"):
            if key not in ticket:
                errors.append(f"{prefix}.{key} is required")
        ticket_key = ticket.get("key")
        if not isinstance(ticket_key, str) or not ticket_key:
            errors.append(f"{prefix}.key must be a non-empty string")
        elif ticket_key in keys:
            errors.append(f"duplicate ticket key: {ticket_key}")
        else:
            keys.add(ticket_key)
        if ticket.get("type") not in {"feature", "bug", "refactor", "test", "infrastructure"}:
            errors.append(f"{prefix}.type is invalid")
        if ticket.get("complexity") not in {"xs", "s", "m", "l"}:
            errors.append(f"{prefix}.complexity is invalid")
        for string_key in ("title", "objective", "commit_message"):
            if not isinstance(ticket.get(string_key), str) or not ticket[string_key].strip():
                errors.append(f"{prefix}.{string_key} must be a non-empty string")
        paths = ticket.get("owned_paths")
        if not isinstance(paths, list) or not paths or any(not isinstance(path, str) or not _safe_relative(path) for path in paths):
            errors.append(f"{prefix}.owned_paths must contain safe relative paths")
        for list_key in ("acceptance_criteria", "depends_on", "focused_checks"):
            if not isinstance(ticket.get(list_key), list) or any(not isinstance(item, str) for item in ticket.get(list_key, [])):
                errors.append(f"{prefix}.{list_key} must be a string list")
    try:
        waves = dependency_waves([ticket for ticket in tickets if isinstance(ticket, dict)])
        by_key = {ticket["key"]: ticket for ticket in tickets if isinstance(ticket, dict) and isinstance(ticket.get("key"), str)}
        for wave in waves:
            for left_index, left_key in enumerate(wave):
                for right_key in wave[left_index + 1 :]:
                    for left_path in by_key[left_key].get("owned_paths", []):
                        for right_path in by_key[right_key].get("owned_paths", []):
                            if _paths_overlap(left_path, right_path):
                                errors.append(f"parallel tickets {left_key} and {right_key} overlap at {left_path} / {right_path}")
    except HarnessError as exc:
        errors.append(str(exc))
    coverage = data.get("coverage")
    if not isinstance(coverage, list):
        errors.append("coverage must be a list")
    else:
        for index, item in enumerate(coverage):
            if not isinstance(item, dict) or not isinstance(item.get("requirement"), str) or not isinstance(item.get("tickets"), list):
                errors.append(f"coverage[{index}] is invalid")
                continue
            unknown = set(item["tickets"]) - keys
            if unknown:
                errors.append(f"coverage[{index}] references unknown tickets: {', '.join(sorted(unknown))}")
    if data.get("status") == "blocked" and not data.get("blockers"):
        errors.append("blocked output must include blockers")
    return errors


def validate_architect_output(data: Any, ticket_keys: set[str] | None = None) -> list[str]:
    errors: list[str] = []
    if not isinstance(data, dict):
        return ["architect output must be an object"]
    required = {"agent", "status", "adr_filename", "adr_markdown", "ticket_constraints", "ticket_graph_valid", "blockers"}
    missing = sorted(required - set(data))
    if missing:
        errors.append(f"missing fields: {', '.join(missing)}")
    if data.get("agent") != "architect":
        errors.append("agent must be architect")
    if data.get("status") not in {"ready", "blocked"}:
        errors.append("status must be ready or blocked")
    filename = data.get("adr_filename")
    if not isinstance(filename, str) or not re.fullmatch(r"ADR-[A-Za-z0-9._-]+\.md", filename):
        errors.append("adr_filename must be a safe ADR-*.md filename")
    if not isinstance(data.get("adr_markdown"), str):
        errors.append("adr_markdown must be a string")
    else:
        invalid_headings = _missing_or_out_of_order_headings(data["adr_markdown"], ADR_HEADINGS)
        if invalid_headings:
            errors.append(f"adr_markdown is missing or reorders required headings: {', '.join(invalid_headings)}")
    constraints = data.get("ticket_constraints")
    if not isinstance(constraints, list):
        errors.append("ticket_constraints must be a list")
    elif ticket_keys is not None:
        for index, constraint in enumerate(constraints):
            if not isinstance(constraint, dict) or constraint.get("ticket_key") not in ticket_keys:
                errors.append(f"ticket_constraints[{index}] references an unknown ticket")
    if not isinstance(data.get("ticket_graph_valid"), bool):
        errors.append("ticket_graph_valid must be boolean")
    if data.get("status") == "ready" and data.get("ticket_graph_valid") is not True:
        errors.append("ready output requires ticket_graph_valid=true")
    if data.get("status") == "blocked" and not data.get("blockers"):
        errors.append("blocked output must include blockers")
    return errors


def validate_generic_agent_output(agent: str, data: Any) -> list[str]:
    if agent not in GENERIC_AGENT_CONTRACTS:
        return [f"unsupported agent validator: {agent}"]
    if not isinstance(data, dict):
        return [f"{agent} output must be an object"]
    statuses, required = GENERIC_AGENT_CONTRACTS[agent]
    errors: list[str] = []
    missing = sorted(({"agent", "status"} | required) - set(data))
    if missing:
        errors.append(f"missing fields: {', '.join(missing)}")
    if data.get("agent") != agent:
        errors.append(f"agent must be {agent}")
    if data.get("status") not in statuses:
        errors.append(f"status must be one of: {', '.join(sorted(statuses))}")
    if data.get("status") == "blocked" and not data.get("blocker"):
        errors.append("blocked output must include blocker")
    if data.get("status") != "blocked" and data.get("worktree_clean") is not True:
        errors.append("successful or requeue output requires worktree_clean=true")
    if agent == "coder" and data.get("status") == "completed" and not isinstance(data.get("commit"), str):
        errors.append("completed coder output requires a commit")
    if agent == "lint":
        gates = data.get("gates")
        if not isinstance(gates, dict):
            errors.append("gates must be an object")
        else:
            for gate_name in ("lint", "lint_arch", "build"):
                gate = gates.get(gate_name)
                if not isinstance(gate, dict) or gate.get("status") not in {"PASS", "FAIL"}:
                    errors.append(f"gates.{gate_name}.status must be PASS or FAIL")
            if data.get("status") == "passed" and any(
                not isinstance(gates.get(gate_name), dict) or gates[gate_name].get("status") != "PASS"
                for gate_name in ("lint", "lint_arch", "build")
            ):
                errors.append("passed lint output requires every gate to pass")
    if agent == "qa":
        results = data.get("acceptance_results")
        if not isinstance(results, list):
            errors.append("acceptance_results must be a list")
        elif data.get("status") == "passed" and any(result.get("status") != "PASS" for result in results if isinstance(result, dict)):
            errors.append("passed QA output requires every acceptance result to pass")
        new_tickets = data.get("new_tickets")
        if not isinstance(new_tickets, list):
            errors.append("new_tickets must be a list")
        elif data.get("status") == "requeue" and not new_tickets:
            errors.append("requeue QA output requires new_tickets")
    if agent == "docs":
        external = data.get("external_documents")
        if not isinstance(external, list):
            errors.append("external_documents must be a list")
        else:
            for index, document in enumerate(external):
                if not isinstance(document, dict) or any(not isinstance(document.get(key), str) or not document[key].strip() for key in ("title", "markdown", "purpose")):
                    errors.append(f"external_documents[{index}] requires title, markdown, and purpose")
    if agent == "pr" and data.get("status") == "created":
        pull_request = data.get("pull_request")
        if not isinstance(pull_request, dict) or not pull_request.get("url"):
            errors.append("created PR output requires pull_request.url")
    return errors


def deep_merge(target: dict[str, Any], patch: dict[str, Any]) -> dict[str, Any]:
    result = copy.deepcopy(target)
    for key, value in patch.items():
        if isinstance(value, dict) and isinstance(result.get(key), dict):
            result[key] = deep_merge(result[key], value)
        else:
            result[key] = copy.deepcopy(value)
    return result


def find_active_run(runs_root: Path, provider: str, issue_key: str) -> Path | None:
    if not runs_root.exists():
        return None
    candidates: list[tuple[str, Path]] = []
    for state_path in runs_root.glob("run_*/state.json"):
        try:
            state = read_json(state_path)
        except HarnessError:
            continue
        issue = state.get("issue", {})
        if issue.get("provider") == provider and str(issue.get("key", "")).upper() == issue_key.upper() and state.get("status") not in TERMINAL_RUN_STATUSES:
            candidates.append((state.get("updated_at", ""), state_path.parent))
    return max(candidates, default=("", None))[1]


def acquire_lease(repo: Path, issue_key: str, run_id: str, lease_seconds: int, reclaim: bool) -> str:
    lock_dir = repo / ".harness" / ".locks"
    lock_dir.mkdir(parents=True, exist_ok=True)
    lock_name = re.sub(r"[^A-Za-z0-9._-]+", "-", issue_key.upper()) + ".json"
    lock_path = lock_dir / lock_name
    now = dt.datetime.now(dt.timezone.utc)
    if lock_path.exists():
        existing = read_json(lock_path)
        try:
            updated = dt.datetime.fromisoformat(existing["updated_at"].replace("Z", "+00:00"))
        except (KeyError, TypeError, ValueError):
            updated = now
        fresh = (now - updated).total_seconds() < int(existing.get("lease_seconds", lease_seconds))
        if fresh and not reclaim:
            raise HarnessError(f"issue is leased by {existing.get('run_id', 'another run')}; inspect it or explicitly reclaim the lease")
    token = secrets.token_urlsafe(24)
    atomic_write_json(lock_path, {
        "issue_key": issue_key,
        "run_id": run_id,
        "session_token": token,
        "updated_at": utc_now(),
        "lease_seconds": lease_seconds,
    })
    return token


def release_lease(repo: Path, issue_key: str, session_token: str) -> None:
    lock_name = re.sub(r"[^A-Za-z0-9._-]+", "-", issue_key.upper()) + ".json"
    lock_path = repo / ".harness" / ".locks" / lock_name
    if not lock_path.exists():
        return
    existing = read_json(lock_path)
    if not secrets.compare_digest(str(existing.get("session_token", "")), session_token):
        raise HarnessError("session token does not own the issue lease")
    lock_path.unlink()


def load_state(run_dir: Path) -> dict[str, Any]:
    value = read_json(run_dir / "state.json")
    if not isinstance(value, dict):
        raise HarnessError("state.json must contain an object")
    return value


def parent_comment(state: dict[str, Any]) -> str:
    marker = f"<!-- agent-harness:run:{state['run_id']} -->"
    lines = [
        marker,
        f"## Agent Harness — `{state['run_id']}`",
        "",
        f"**Issue:** {state['issue'].get('key')}  ",
        f"**Status:** {state.get('status')}  ",
        f"**Updated:** {state.get('updated_at')}",
        "",
        "| Stage | Status | Evidence |",
        "|---|---|---|",
    ]
    for stage in state.get("selected_stages", []):
        details = state.get("stages", {}).get(stage, {})
        evidence = str(details.get("summary") or details.get("error") or "—").replace("\n", " ")
        lines.append(f"| {stage} | {details.get('status', 'pending')} | {evidence} |")
    tickets = state.get("tickets", [])
    if tickets:
        lines.extend(["", "### Tickets", "", "| Logical | Tracker | Depends on | Status | Commit |", "|---|---|---|---|---|"])
        for ticket in tickets:
            lines.append(
                f"| {ticket.get('key', '—')} | {ticket.get('provider_key', 'pending')} | "
                f"{', '.join(ticket.get('depends_on', [])) or '—'} | {ticket.get('status', 'pending')} | {ticket.get('commit', '—') or '—'} |"
            )
    artifacts = state.get("artifacts", {})
    if artifacts:
        lines.extend(["", "### Artifacts"])
        for name, artifact in sorted(artifacts.items()):
            if isinstance(artifact, dict):
                reference = artifact.get("notion_url") or artifact.get("path") or "pending"
            else:
                reference = artifact
            lines.append(f"- **{name}:** {reference}")
    git = state.get("git", {})
    if git.get("branch") or git.get("pr_url"):
        lines.extend(["", "### Delivery", f"- Branch: `{git.get('branch', 'pending')}`", f"- Pull request: {git.get('pr_url', 'pending')}"])
    lines.extend(["", "_This comment is the external projection of the local run journal._"])
    return "\n".join(lines) + "\n"


def ticket_comment(run_id: str, ticket: dict[str, Any]) -> str:
    marker = f"<!-- agent-harness:ticket:{run_id}:{ticket['key']} -->"
    lines = [
        marker,
        f"## Agent Harness ticket `{ticket['key']}`",
        "",
        f"- Run: `{run_id}`",
        f"- Status: {ticket.get('status', 'pending')}",
        f"- Depends on: {', '.join(ticket.get('depends_on', [])) or 'None'}",
        f"- Owner: {ticket.get('owner', 'Unclaimed')}",
        f"- Commit: {ticket.get('commit') or 'Pending'}",
    ]
    if ticket.get("summary"):
        lines.extend(["", str(ticket["summary"])])
    return "\n".join(lines) + "\n"


def terminal_summary(state: dict[str, Any]) -> str:
    marker = f"<!-- agent-harness:summary:{state['run_id']} -->"
    completed = [name for name in state.get("selected_stages", []) if state.get("stages", {}).get(name, {}).get("status") == "completed"]
    blocked = [name for name in state.get("selected_stages", []) if state.get("stages", {}).get(name, {}).get("status") == "blocked"]
    ticket_counts: dict[str, int] = {}
    for ticket in state.get("tickets", []):
        status = str(ticket.get("status", "pending"))
        ticket_counts[status] = ticket_counts.get(status, 0) + 1
    lines = [
        marker,
        f"## Agent Harness {state.get('status')} — `{state['run_id']}`",
        "",
        f"- Completed stages: {', '.join(completed) or 'None'}",
        f"- Blocked stages: {', '.join(blocked) or 'None'}",
        f"- Tickets: {', '.join(f'{status}={count}' for status, count in sorted(ticket_counts.items())) or 'None'}",
    ]
    artifacts = state.get("artifacts", {})
    if artifacts:
        lines.append(f"- Artifacts: {', '.join(sorted(artifacts))}")
    git = state.get("git", {})
    if git.get("pr_url"):
        lines.append(f"- Pull request: {git['pr_url']}")
    elif git.get("branch"):
        lines.append(f"- Branch: `{git['branch']}`")
    last_event = state.get("events", [])[-1] if state.get("events") else None
    if last_event:
        lines.append(f"- Last checkpoint: {last_event.get('type')} at {last_event.get('at')}")
    return "\n".join(lines) + "\n"


def stable_slug(value: str) -> str:
    slug = re.sub(r"[^a-z0-9]+", "-", value.lower()).strip("-")
    if not slug:
        raise HarnessError("marker value must contain a letter or number")
    return slug


def fixed_hook_environment(values: dict[str, str]) -> dict[str, str]:
    allowed_host = ("PATH", "TMPDIR", "TEMP", "TMP", "SYSTEMROOT", "COMSPEC", "PATHEXT", "WINDIR")
    env = {key: os.environ[key] for key in allowed_host if key in os.environ}
    for key, value in values.items():
        if key not in {"HARNESS_RUN_ID", "HARNESS_ISSUE_KEY", "HARNESS_STAGE", "HARNESS_ARTIFACT_DIR", "HARNESS_WORKTREE"}:
            raise HarnessError(f"unsupported hook environment key: {key}")
        env[key] = value
    return env


def command_validate_config(args: argparse.Namespace) -> None:
    config = load_simple_yaml(Path(args.path))
    errors = validate_config(config)
    if errors:
        fail("invalid harness config", details=errors)
    emit({"ok": True, "config": config})


def command_validate_pipeline(args: argparse.Namespace) -> None:
    pipeline = load_simple_yaml(Path(args.path))
    errors = validate_pipeline(pipeline, Path(args.repo).resolve() if args.repo else None)
    if errors:
        fail("invalid harness pipeline", details=errors)
    emit({"ok": True, "stage_order": stage_order(pipeline), "pipeline": pipeline})


def command_resolve_stages(args: argparse.Namespace) -> None:
    pipeline = load_simple_yaml(Path(args.pipeline))
    errors = validate_pipeline(pipeline)
    if errors:
        fail("invalid harness pipeline", details=errors)
    result = resolve_stages(pipeline, args.stages, set(filter(None, args.completed.split(","))))
    if result["missing_prerequisites"]:
        fail("selected stages have missing prerequisites", details=result)
    emit({"ok": True, **result})


def command_waves(args: argparse.Namespace) -> None:
    value = read_json(Path(args.input))
    tickets = value.get("tickets") if isinstance(value, dict) else value
    if not isinstance(tickets, list):
        fail("input must be a ticket list or product output object")
    emit({"ok": True, "waves": dependency_waves(tickets)})


def command_validate_agent(args: argparse.Namespace) -> None:
    data = read_json(Path(args.input))
    if args.agent == "product":
        errors = validate_product_output(data)
    elif args.agent == "architect":
        ticket_keys = None
        if args.tickets:
            product = read_json(Path(args.tickets))
            ticket_keys = {ticket["key"] for ticket in product.get("tickets", [])}
        errors = validate_architect_output(data, ticket_keys)
    else:
        errors = validate_generic_agent_output(args.agent, data)
    if errors:
        fail(f"invalid {args.agent} output", details=errors)
    payload: dict[str, Any] = {"ok": True, "agent": args.agent, "status": data["status"]}
    if args.agent == "product":
        payload["waves"] = dependency_waves(data["tickets"])
    emit(payload)


def command_materialize_source(args: argparse.Namespace) -> None:
    pipeline = load_simple_yaml(Path(args.pipeline))
    errors = validate_pipeline(pipeline)
    if errors:
        fail("invalid harness pipeline", details=errors)
    stage = pipeline_stage(pipeline, args.stage)
    input_contract = next((entry for entry in stage["inputs"] if entry["id"] == args.input_id), None)
    if input_contract is None:
        raise HarnessError(f"input not found for stage {args.stage}: {args.input_id}")
    if args.source not in input_contract.get("sources", []):
        raise HarnessError(f"source {args.source} is not allowed for input {args.input_id}")
    content_path = Path(args.content_file)
    if input_contract["format"] == "json":
        content = read_json(content_path)
    else:
        content = content_path.read_text(encoding="utf-8")
    destination = run_file(Path(args.run_dir), input_contract["file"])
    write_contract_file(destination, input_contract["format"], content)
    emit({"ok": True, "stage": args.stage, "input": args.input_id, "source": args.source, "file": str(destination)})


def command_materialize_generated_inputs(args: argparse.Namespace) -> None:
    pipeline = load_simple_yaml(Path(args.pipeline))
    errors = validate_pipeline(pipeline)
    if errors:
        fail("invalid harness pipeline", details=errors)
    stage = pipeline_stage(pipeline, args.stage)
    run_dir = Path(args.run_dir)
    generated_contracts = [entry for entry in stage["inputs"] if "generated_from" in entry]
    materialized: list[dict[str, Any]] = []
    for input_contract in generated_contracts:
        generated = input_contract["generated_from"]
        source = read_json(run_file(run_dir, generated["file"]))
        collection = extract_result(source, generated["collection"])
        if not isinstance(collection, list):
            raise HarnessError(f"generated input collection must be a list: {generated['file']}#{generated['collection']}")
        for item in collection:
            if not isinstance(item, dict) or not isinstance(item.get(generated["key"]), str):
                raise HarnessError(f"generated input item requires string key {generated['key']}")
            ticket_key = item[generated["key"]]
            destination = run_file(run_dir, input_contract["file"], {"ticket_key": ticket_key})
            write_contract_file(destination, input_contract["format"], item)
            materialized.append({"input": input_contract["id"], "ticket_key": ticket_key, "file": str(destination)})
    emit({"ok": True, "stage": args.stage, "materialized": materialized})


def command_materialize_result(args: argparse.Namespace) -> None:
    pipeline = load_simple_yaml(Path(args.pipeline))
    errors = validate_pipeline(pipeline)
    if errors:
        fail("invalid harness pipeline", details=errors)
    stage = pipeline_stage(pipeline, args.stage)
    result_contract = stage["result"]
    data = read_json(Path(args.input))
    agent = result_contract["agent"]
    if agent == "product":
        result_errors = validate_product_output(data)
    elif agent == "architect":
        result_errors = validate_architect_output(data)
    else:
        result_errors = validate_generic_agent_output(agent, data)
    if result_errors:
        fail(f"invalid {agent} output", details=result_errors)
    variables = {"ticket_key": args.ticket_key} if args.ticket_key else {}
    if stage["mode"] == "ticket_parallel" and not args.ticket_key:
        raise HarnessError("--ticket-key is required for ticket_parallel result materialization")
    run_dir = Path(args.run_dir)
    result_path = run_file(run_dir, result_contract["file"], variables)
    write_contract_file(result_path, result_contract["format"], data)
    outputs: list[dict[str, str]] = []
    for output in stage["outputs"]:
        value = extract_result(data, output["from_result"])
        output_path = run_file(run_dir, output["file"], variables)
        write_contract_file(output_path, output["format"], value)
        outputs.append({"id": output["id"], "file": str(output_path)})
    emit({"ok": True, "stage": args.stage, "agent": agent, "result_file": str(result_path), "outputs": outputs})


def command_init_run(args: argparse.Namespace) -> None:
    repo = Path(args.repo).resolve()
    pipeline_path = repo / ".harness" / "pipeline.yaml"
    pipeline = load_simple_yaml(pipeline_path)
    errors = validate_pipeline(pipeline, repo)
    if errors:
        fail("invalid harness pipeline", details=errors)
    runs_root = repo / Path(pipeline["run_root"]).parent
    active = None if args.new_run else find_active_run(runs_root, args.provider, args.issue_key)
    if active:
        state = load_state(active)
        run_dir = active
        resumed = True
        completed = {stage for stage, details in state.get("stages", {}).items() if details.get("status") == "completed"}
    else:
        run_id = f"run_{dt.datetime.now(dt.timezone.utc).strftime('%Y%m%dT%H%M%SZ')}_{secrets.token_hex(4)}"
        run_dir = repo / pipeline["run_root"].replace("{run_id}", run_id)
        completed = set()
        resumed = False
        state = {}
    selection = resolve_stages(pipeline, args.stages, completed)
    if selection["missing_prerequisites"]:
        fail("selected stages have missing prerequisites", details=selection)
    token = acquire_lease(repo, args.issue_key, state.get("run_id", run_dir.name), args.lease_seconds, args.reclaim_lease)
    if not resumed:
        now = utc_now()
        state = {
            "schema_version": 1,
            "run_id": run_dir.name,
            "issue": {"provider": args.provider, "key": args.issue_key, "id": None, "url": None, "title": None},
            "status": "running",
            "selected_stages": selection["selected"],
            "stages": {stage: {"status": "pending"} for stage in selection["selected"]},
            "tickets": [],
            "artifacts": {},
            "external": {"parent_comment_id": None, "notion_hub_id": None, "notion_hub_url": None, "sync_pending": []},
            "git": {"branch": None, "base": None, "commits": [], "pr_url": None},
            "events": [{"at": now, "type": "run.created", "details": {"selected_stages": selection["selected"]}}],
            "created_at": now,
            "updated_at": now,
        }
        run_dir.mkdir(parents=True, exist_ok=False)
        for directory in ("artifacts", "logs", "agent-output"):
            (run_dir / directory).mkdir()
        atomic_write_json(run_dir / "state.json", state)
    else:
        state["status"] = "running"
        state["selected_stages"] = list(dict.fromkeys(state.get("selected_stages", []) + selection["selected"]))
        for stage in selection["selected"]:
            state.setdefault("stages", {}).setdefault(stage, {"status": "pending"})
        state["updated_at"] = utc_now()
        state.setdefault("events", []).append({"at": state["updated_at"], "type": "run.resumed", "details": {"selected_stages": selection["selected"]}})
        atomic_write_json(run_dir / "state.json", state)
    emit({"ok": True, "resumed": resumed, "run_id": state["run_id"], "run_dir": str(run_dir), "session_token": token, "selected_stages": selection["selected"]})


def command_checkpoint(args: argparse.Namespace) -> None:
    run_dir = Path(args.run_dir).resolve()
    state = load_state(run_dir)
    patch = json.loads(args.patch_json)
    if not isinstance(patch, dict):
        raise HarnessError("patch-json must be an object")
    updated = deep_merge(state, patch)
    updated["updated_at"] = utc_now()
    if args.event:
        details = json.loads(args.event_details_json)
        updated.setdefault("events", []).append({"at": updated["updated_at"], "type": args.event, "details": details})
    atomic_write_json(run_dir / "state.json", updated)
    emit({"ok": True, "run_id": updated["run_id"], "updated_at": updated["updated_at"]})


def command_set_stage(args: argparse.Namespace) -> None:
    if args.status not in STAGE_STATUSES:
        raise HarnessError(f"invalid stage status: {args.status}")
    run_dir = Path(args.run_dir).resolve()
    state = load_state(run_dir)
    if args.stage not in state.get("stages", {}):
        raise HarnessError(f"stage is not selected for this run: {args.stage}")
    details = json.loads(args.details_json)
    stage = deep_merge(state["stages"][args.stage], details)
    stage["status"] = args.status
    now = utc_now()
    stage["updated_at"] = now
    state["stages"][args.stage] = stage
    state["updated_at"] = now
    state.setdefault("events", []).append({"at": now, "type": f"stage.{args.status}", "details": {"stage": args.stage, **details}})
    if args.status == "blocked":
        state["status"] = "paused"
    elif all(state["stages"][name].get("status") in {"completed", "skipped"} for name in state["selected_stages"]):
        state["status"] = "completed"
    atomic_write_json(run_dir / "state.json", state)
    emit({"ok": True, "run_status": state["status"], "stage": args.stage, "stage_status": args.status})


def command_render_comment(args: argparse.Namespace) -> None:
    state = load_state(Path(args.run_dir))
    if args.kind == "parent":
        print(parent_comment(state), end="")
        return
    if args.kind == "summary":
        print(terminal_summary(state), end="")
        return
    ticket = next((ticket for ticket in state.get("tickets", []) if ticket.get("key") == args.ticket_key), None)
    if ticket is None:
        raise HarnessError(f"ticket not found: {args.ticket_key}")
    print(ticket_comment(state["run_id"], ticket), end="")


def command_list_runs(args: argparse.Namespace) -> None:
    repo = Path(args.repo).resolve()
    found: list[dict[str, Any]] = []
    for state_path in sorted((repo / ".harness" / "runs").glob("run_*/state.json")):
        try:
            state = read_json(state_path)
        except HarnessError:
            continue
        if args.issue_key and str(state.get("issue", {}).get("key", "")).upper() != args.issue_key.upper():
            continue
        found.append({
            "run_id": state.get("run_id"),
            "issue": state.get("issue"),
            "status": state.get("status"),
            "selected_stages": state.get("selected_stages", []),
            "tickets": state.get("tickets", []),
            "artifacts": state.get("artifacts", {}),
            "git": state.get("git", {}),
            "updated_at": state.get("updated_at"),
            "run_dir": str(state_path.parent),
        })
    emit({"ok": True, "runs": found})


def command_markers(args: argparse.Namespace) -> None:
    result: dict[str, Any] = {
        "parent_comment": f"<!-- agent-harness:run:{args.run_id} -->",
        "terminal_summary": f"<!-- agent-harness:summary:{args.run_id} -->",
        "notion_hub": f"<!-- agent-harness:notion-hub:{args.provider}:{stable_slug(args.issue_key)} -->",
    }
    if args.ticket_key:
        result.update({
            "ticket_comment": f"<!-- agent-harness:ticket:{args.run_id}:{args.ticket_key} -->",
            "child_body": f"<!-- agent-harness:child:{args.run_id}:{args.ticket_key} -->",
        })
    if args.artifact:
        result["notion_artifact"] = f"<!-- agent-harness:notion-artifact:{args.run_id}:{stable_slug(args.artifact)} -->"
    emit({"ok": True, "markers": result})


def command_run_hook(args: argparse.Namespace) -> None:
    repo = Path(args.repo).resolve()
    spec = json.loads(args.spec_json)
    if not isinstance(spec, dict):
        raise HarnessError("hook spec must be an object")
    argv = spec.get("argv")
    if not isinstance(argv, list) or not argv or any(not isinstance(item, str) for item in argv):
        raise HarnessError("hook argv must be a non-empty string list")
    cwd_value = spec.get("cwd", ".")
    if not isinstance(cwd_value, str) or not _safe_relative(cwd_value):
        raise HarnessError("hook cwd must be repo-relative")
    cwd = (repo / cwd_value).resolve()
    if cwd != repo and repo not in cwd.parents:
        raise HarnessError("hook cwd escapes the repository")
    timeout = spec.get("timeout_seconds", 300)
    if not isinstance(timeout, int) or not 1 <= timeout <= 3600:
        raise HarnessError("hook timeout_seconds must be 1..3600")
    env = fixed_hook_environment(json.loads(args.env_json))
    started = utc_now()
    try:
        process = subprocess.run(argv, cwd=cwd, env=env, text=True, capture_output=True, timeout=timeout, check=False)
        result = {
            "ok": process.returncode == 0,
            "hook_id": spec.get("id"),
            "argv": argv,
            "cwd": str(cwd.relative_to(repo) or "."),
            "started_at": started,
            "finished_at": utc_now(),
            "exit_code": process.returncode,
            "stdout": process.stdout,
            "stderr": process.stderr,
        }
    except subprocess.TimeoutExpired as exc:
        result = {
            "ok": False,
            "hook_id": spec.get("id"),
            "argv": argv,
            "cwd": str(cwd.relative_to(repo) or "."),
            "started_at": started,
            "finished_at": utc_now(),
            "timed_out": True,
            "stdout": exc.stdout or "",
            "stderr": exc.stderr or "",
        }
    emit(result)
    if not result["ok"]:
        raise SystemExit(1)


def command_release_lease(args: argparse.Namespace) -> None:
    release_lease(Path(args.repo).resolve(), args.issue_key, args.session_token)
    emit({"ok": True, "released": args.issue_key})


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)

    command = sub.add_parser("validate-config")
    command.add_argument("path")
    command.set_defaults(function=command_validate_config)

    command = sub.add_parser("validate-pipeline")
    command.add_argument("path")
    command.add_argument("--repo")
    command.set_defaults(function=command_validate_pipeline)

    command = sub.add_parser("resolve-stages")
    command.add_argument("--pipeline", required=True)
    command.add_argument("--stages", required=True)
    command.add_argument("--completed", default="")
    command.set_defaults(function=command_resolve_stages)

    command = sub.add_parser("waves")
    command.add_argument("--input", required=True)
    command.set_defaults(function=command_waves)

    command = sub.add_parser("validate-agent-output")
    command.add_argument("--agent", choices=("product", "architect", "coder", "lint", "qa", "docs", "pr"), required=True)
    command.add_argument("--input", required=True)
    command.add_argument("--tickets")
    command.set_defaults(function=command_validate_agent)

    command = sub.add_parser("materialize-source")
    command.add_argument("--pipeline", required=True)
    command.add_argument("--run-dir", required=True)
    command.add_argument("--stage", required=True)
    command.add_argument("--input-id", required=True)
    command.add_argument("--source", required=True)
    command.add_argument("--content-file", required=True)
    command.set_defaults(function=command_materialize_source)

    command = sub.add_parser("materialize-generated-inputs")
    command.add_argument("--pipeline", required=True)
    command.add_argument("--run-dir", required=True)
    command.add_argument("--stage", required=True)
    command.set_defaults(function=command_materialize_generated_inputs)

    command = sub.add_parser("materialize-result")
    command.add_argument("--pipeline", required=True)
    command.add_argument("--run-dir", required=True)
    command.add_argument("--stage", required=True)
    command.add_argument("--input", required=True)
    command.add_argument("--ticket-key")
    command.set_defaults(function=command_materialize_result)

    command = sub.add_parser("init-run")
    command.add_argument("--repo", required=True)
    command.add_argument("--provider", choices=("linear", "jira"), required=True)
    command.add_argument("--issue-key", required=True)
    command.add_argument("--stages", required=True)
    command.add_argument("--new-run", action="store_true")
    command.add_argument("--reclaim-lease", action="store_true")
    command.add_argument("--lease-seconds", type=int, default=900)
    command.set_defaults(function=command_init_run)

    command = sub.add_parser("checkpoint")
    command.add_argument("--run-dir", required=True)
    command.add_argument("--patch-json", required=True)
    command.add_argument("--event")
    command.add_argument("--event-details-json", default="{}")
    command.set_defaults(function=command_checkpoint)

    command = sub.add_parser("set-stage")
    command.add_argument("--run-dir", required=True)
    command.add_argument("--stage", required=True)
    command.add_argument("--status", required=True)
    command.add_argument("--details-json", default="{}")
    command.set_defaults(function=command_set_stage)

    command = sub.add_parser("render-comment")
    command.add_argument("--run-dir", required=True)
    command.add_argument("--kind", choices=("parent", "ticket", "summary"), required=True)
    command.add_argument("--ticket-key")
    command.set_defaults(function=command_render_comment)

    command = sub.add_parser("list-runs")
    command.add_argument("--repo", required=True)
    command.add_argument("--issue-key")
    command.set_defaults(function=command_list_runs)

    command = sub.add_parser("markers")
    command.add_argument("--run-id", required=True)
    command.add_argument("--provider", choices=("linear", "jira"), required=True)
    command.add_argument("--issue-key", required=True)
    command.add_argument("--ticket-key")
    command.add_argument("--artifact")
    command.set_defaults(function=command_markers)

    command = sub.add_parser("run-hook")
    command.add_argument("--repo", required=True)
    command.add_argument("--spec-json", required=True)
    command.add_argument("--env-json", required=True)
    command.set_defaults(function=command_run_hook)

    command = sub.add_parser("release-lease")
    command.add_argument("--repo", required=True)
    command.add_argument("--issue-key", required=True)
    command.add_argument("--session-token", required=True)
    command.set_defaults(function=command_release_lease)
    return parser


def main() -> None:
    parser = build_parser()
    args = parser.parse_args()
    try:
        args.function(args)
    except (HarnessError, FileNotFoundError, json.JSONDecodeError, OSError) as exc:
        fail(str(exc))


if __name__ == "__main__":
    main()
