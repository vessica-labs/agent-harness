#!/usr/bin/env python3
"""Deterministically enforce repository-specific architecture rules.

Rule file format:

{
  "version": 1,
  "rules": [
    {
      "id": "source-file-size",
      "type": "max_file_lines",
      "globs": ["src/**/*.py"],
      "exclude": ["**/generated/**"],
      "max_lines": 800,
      "message": "Split large source files along a clear responsibility boundary."
    },
    {
      "id": "domain-does-not-import-api",
      "type": "forbid_text",
      "globs": ["src/domain/**/*.py"],
      "exclude": ["**/tests/**"],
      "pattern": "from src\\.api|import src\\.api",
      "message": "Domain code must not depend on the API layer."
    },
    {
      "id": "required-boundary",
      "type": "require_path",
      "path": "src/domain",
      "message": "The domain boundary must exist."
    },
    {
      "id": "forbidden-legacy-module",
      "type": "forbid_path",
      "glob": "src/legacy/**",
      "message": "New code must not use the retired legacy module."
    },
    {
      "id": "entrypoint-wires-domain",
      "type": "require_text",
      "file": "src/main.py",
      "pattern": "create_domain",
      "message": "The entrypoint must construct the domain boundary."
    }
  ]
}

The shipped rule file contains a small, language-neutral baseline. Add only
rules that encode an accepted architecture decision and can be checked without
model judgment.
"""

from __future__ import annotations

import argparse
import fnmatch
import json
import re
import sys
from pathlib import Path
from typing import Any


IGNORED_PREFIXES = (".git/", ".harness/runs/", ".harness/worktrees/", ".harness/adrs/")
RULE_TYPES = {"forbid_path", "require_path", "forbid_text", "require_text", "max_file_lines"}


class ConfigError(Exception):
    pass


def safe_relative(value: str, field: str) -> str:
    path = Path(value)
    if not value or path.is_absolute() or ".." in path.parts:
        raise ConfigError(f"{field} must be a non-empty repository-relative path")
    return value


def is_ignored(relative: str) -> bool:
    normalized = relative.replace("\\", "/")
    return any(normalized == prefix.rstrip("/") or normalized.startswith(prefix) for prefix in IGNORED_PREFIXES)


def matches_pattern(relative: str, pattern: str) -> bool:
    path = Path(relative)
    return (
        path.match(pattern)
        or fnmatch.fnmatchcase(relative, pattern)
        or (pattern.startswith("**/") and fnmatch.fnmatchcase(relative, pattern[3:]))
    )


def inside_root(root: Path, path: Path) -> bool:
    resolved = path.resolve()
    return resolved == root or root in resolved.parents


def matching_paths(root: Path, pattern: str, excludes: list[str] | None = None) -> list[Path]:
    safe_relative(pattern, "glob")
    matches: list[Path] = []
    for path in root.glob(pattern):
        if not inside_root(root, path):
            raise ConfigError(f"glob resolves outside repository: {pattern}")
        relative = path.relative_to(root).as_posix()
        if is_ignored(relative):
            continue
        if any(matches_pattern(relative, exclude) for exclude in (excludes or [])):
            continue
        matches.append(path)
    return sorted(matches, key=lambda path: path.relative_to(root).as_posix())


def line_number(text: str, offset: int) -> int:
    return text.count("\n", 0, offset) + 1


def validate_rule(rule: Any, index: int) -> dict[str, Any]:
    if not isinstance(rule, dict):
        raise ConfigError(f"rules[{index}] must be an object")
    rule_id = rule.get("id")
    rule_type = rule.get("type")
    message = rule.get("message")
    if not isinstance(rule_id, str) or not re.fullmatch(r"[a-zA-Z0-9._-]+", rule_id):
        raise ConfigError(f"rules[{index}].id is invalid")
    if rule_type not in RULE_TYPES:
        raise ConfigError(f"rules[{index}].type must be one of {', '.join(sorted(RULE_TYPES))}")
    if not isinstance(message, str) or not message.strip():
        raise ConfigError(f"rules[{index}].message is required")
    common = {"id", "type", "message"}
    allowed = {
        "forbid_path": common | {"glob", "exclude"},
        "require_path": common | {"path"},
        "forbid_text": common | {"globs", "exclude", "pattern"},
        "require_text": common | {"file", "pattern"},
        "max_file_lines": common | {"globs", "exclude", "max_lines"},
    }[rule_type]
    unknown = sorted(set(rule) - allowed)
    if unknown:
        raise ConfigError(f"rules[{index}] contains unknown fields: {', '.join(unknown)}")
    if rule_type == "forbid_path":
        safe_relative(rule.get("glob", ""), f"rules[{index}].glob")
        validate_excludes(rule, index)
    elif rule_type == "require_path":
        safe_relative(rule.get("path", ""), f"rules[{index}].path")
    elif rule_type in {"forbid_text", "max_file_lines"}:
        globs = rule.get("globs")
        if not isinstance(globs, list) or not globs or any(not isinstance(item, str) for item in globs):
            raise ConfigError(f"rules[{index}].globs must be a non-empty string list")
        for pattern in globs:
            safe_relative(pattern, f"rules[{index}].globs")
        validate_excludes(rule, index)
        if rule_type == "forbid_text":
            compile_pattern(rule, index)
        else:
            maximum = rule.get("max_lines")
            if isinstance(maximum, bool) or not isinstance(maximum, int) or maximum < 1:
                raise ConfigError(f"rules[{index}].max_lines must be a positive integer")
    else:
        safe_relative(rule.get("file", ""), f"rules[{index}].file")
        compile_pattern(rule, index)
    return rule


def validate_excludes(rule: dict[str, Any], index: int) -> None:
    excludes = rule.get("exclude", [])
    if not isinstance(excludes, list) or any(not isinstance(item, str) for item in excludes):
        raise ConfigError(f"rules[{index}].exclude must be a string list")
    for pattern in excludes:
        safe_relative(pattern, f"rules[{index}].exclude")


def compile_pattern(rule: dict[str, Any], index: int) -> re.Pattern[str]:
    pattern = rule.get("pattern")
    if not isinstance(pattern, str) or not pattern:
        raise ConfigError(f"rules[{index}].pattern is required")
    try:
        return re.compile(pattern, re.MULTILINE)
    except re.error as exc:
        raise ConfigError(f"rules[{index}].pattern is invalid: {exc}") from exc


def load_config(path: Path) -> list[dict[str, Any]]:
    try:
        config = json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise ConfigError(f"rule file not found: {path}") from exc
    except json.JSONDecodeError as exc:
        raise ConfigError(f"invalid JSON in {path}: {exc}") from exc
    if not isinstance(config, dict) or config.get("version") != 1:
        raise ConfigError("rule file must be an object with version: 1")
    unknown = sorted(set(config) - {"version", "rules"})
    if unknown:
        raise ConfigError(f"rule file contains unknown fields: {', '.join(unknown)}")
    rules = config.get("rules")
    if not isinstance(rules, list):
        raise ConfigError("rule file must contain a rules array")
    validated = [validate_rule(rule, index) for index, rule in enumerate(rules)]
    ids = [rule["id"] for rule in validated]
    if len(ids) != len(set(ids)):
        raise ConfigError("rule ids must be unique")
    return validated


def lint(root: Path, rules: list[dict[str, Any]]) -> list[dict[str, Any]]:
    violations: list[dict[str, Any]] = []
    for index, rule in enumerate(rules):
        rule_type = rule["type"]
        if rule_type == "require_path":
            path = root / rule["path"]
            if not inside_root(root, path):
                raise ConfigError(f"rule {rule['id']} resolves outside repository")
            if not path.exists():
                violations.append({"rule": rule["id"], "path": rule["path"], "line": None, "message": rule["message"]})
        elif rule_type == "forbid_path":
            for path in matching_paths(root, rule["glob"], rule.get("exclude")):
                violations.append({"rule": rule["id"], "path": path.relative_to(root).as_posix(), "line": None, "message": rule["message"]})
        elif rule_type == "forbid_text":
            pattern = compile_pattern(rule, index)
            candidates: set[Path] = set()
            for glob in rule["globs"]:
                candidates.update(path for path in matching_paths(root, glob, rule.get("exclude")) if path.is_file())
            for path in sorted(candidates, key=lambda candidate: candidate.relative_to(root).as_posix()):
                try:
                    text = path.read_text(encoding="utf-8")
                except UnicodeDecodeError as exc:
                    raise ConfigError(f"rule {rule['id']} matched non-UTF-8 file {path.relative_to(root)}") from exc
                for match in pattern.finditer(text):
                    violations.append({
                        "rule": rule["id"],
                        "path": path.relative_to(root).as_posix(),
                        "line": line_number(text, match.start()),
                        "message": rule["message"],
                    })
        elif rule_type == "require_text":
            path = root / rule["file"]
            if not inside_root(root, path):
                raise ConfigError(f"rule {rule['id']} resolves outside repository")
            if not path.is_file():
                violations.append({"rule": rule["id"], "path": rule["file"], "line": None, "message": rule["message"]})
                continue
            try:
                text = path.read_text(encoding="utf-8")
            except UnicodeDecodeError as exc:
                raise ConfigError(f"rule {rule['id']} matched non-UTF-8 file {rule['file']}") from exc
            if compile_pattern(rule, index).search(text) is None:
                violations.append({"rule": rule["id"], "path": rule["file"], "line": None, "message": rule["message"]})
        else:
            candidates: set[Path] = set()
            for glob in rule["globs"]:
                candidates.update(path for path in matching_paths(root, glob, rule.get("exclude")) if path.is_file())
            for path in sorted(candidates, key=lambda candidate: candidate.relative_to(root).as_posix()):
                try:
                    text = path.read_text(encoding="utf-8")
                except UnicodeDecodeError as exc:
                    raise ConfigError(f"rule {rule['id']} matched non-UTF-8 file {path.relative_to(root)}") from exc
                actual = len(text.splitlines())
                if actual > rule["max_lines"]:
                    violations.append({
                        "rule": rule["id"],
                        "path": path.relative_to(root).as_posix(),
                        "line": rule["max_lines"] + 1,
                        "message": f"{rule['message']} ({actual} lines; maximum {rule['max_lines']})",
                    })
    return violations


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", default=".", help="Repository root; defaults to the current directory")
    parser.add_argument("--config", default=".harness/arch-lint-rules.json", help="Rule file relative to the repository root")
    parser.add_argument("--json", action="store_true", help="Emit machine-readable JSON")
    args = parser.parse_args()
    root = Path(args.root).resolve()
    config_path = Path(args.config)
    if not config_path.is_absolute():
        config_path = root / config_path
    try:
        rules = load_config(config_path)
        violations = lint(root, rules)
    except (ConfigError, OSError) as exc:
        if args.json:
            print(json.dumps({"ok": False, "error": str(exc), "violations": []}, sort_keys=True))
        else:
            print(f"arch-lint: CONFIG ERROR: {exc}", file=sys.stderr)
        raise SystemExit(2)
    if args.json:
        print(json.dumps({"ok": not violations, "rules": len(rules), "violations": violations}, sort_keys=True))
    elif violations:
        for violation in violations:
            location = violation["path"]
            if violation["line"] is not None:
                location += f":{violation['line']}"
            print(f"{location}: [{violation['rule']}] {violation['message']}")
        print(f"arch-lint: FAIL ({len(violations)} violation(s), {len(rules)} rule(s))")
    else:
        print(f"arch-lint: PASS ({len(rules)} rule(s))")
    raise SystemExit(1 if violations else 0)


if __name__ == "__main__":
    main()
