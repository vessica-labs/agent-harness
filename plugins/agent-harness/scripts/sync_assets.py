#!/usr/bin/env python3
"""Synchronize canonical harness templates into the plugin package."""

from __future__ import annotations

import argparse
import json
import shutil
from pathlib import Path


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true", help="Fail when packaged assets differ")
    args = parser.parse_args()
    plugin_root = Path(__file__).resolve().parent.parent
    repo_root = plugin_root.parent.parent
    source = repo_root / "harness-templates" / "base"
    target = plugin_root / "assets" / "harness" / "base"
    if not source.is_dir():
        raise SystemExit(f"source templates do not exist: {source}")
    def snapshot(root: Path) -> dict[str, bytes]:
        if not root.is_dir():
            return {}
        return {str(path.relative_to(root)): path.read_bytes() for path in root.rglob("*") if path.is_file()}

    equal = target.is_dir() and snapshot(source) == snapshot(target)
    if args.check:
        print(json.dumps({"ok": equal, "source": str(source), "target": str(target)}))
        raise SystemExit(0 if equal else 1)
    target.parent.mkdir(parents=True, exist_ok=True)
    if target.exists():
        shutil.rmtree(target)
    shutil.copytree(source, target)
    print(json.dumps({"ok": True, "source": str(source), "target": str(target)}))


if __name__ == "__main__":
    main()
