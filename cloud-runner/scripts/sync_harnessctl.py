#!/usr/bin/env python3
from pathlib import Path

root = Path(__file__).resolve().parents[2]
source = root / "plugins" / "agent-harness" / "scripts" / "harnessctl.py"
target = Path(__file__).resolve().parent / "harnessctl.py"
target.write_bytes(source.read_bytes())
target.chmod(0o755)
