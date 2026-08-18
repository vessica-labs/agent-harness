from __future__ import annotations

import importlib.util
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


REPO = Path(__file__).resolve().parents[1]
BASE = REPO / "harness-templates" / "base"
PLUGIN = REPO / "plugins" / "agent-harness"
SCRIPT = BASE / ".harness" / "scripts" / "arch-lint.py"
RULES = BASE / ".harness" / "arch-lint-rules.json"
BOOTSTRAP = PLUGIN / "scripts" / "bootstrap.py"
HARNESSCTL = PLUGIN / "scripts" / "harnessctl.py"


def run_lint(root: Path, config: Path, *, json_output: bool = True) -> subprocess.CompletedProcess[str]:
    command = [sys.executable, str(SCRIPT), "--root", str(root), "--config", str(config)]
    if json_output:
        command.append("--json")
    return subprocess.run(command, text=True, capture_output=True, check=False)


def load_harnessctl():
    spec = importlib.util.spec_from_file_location("harnessctl_arch_lint", HARNESSCTL)
    assert spec and spec.loader
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class ArchitectureLintTests(unittest.TestCase):
    def test_standard_baseline_passes_canonical_template(self) -> None:
        process = run_lint(BASE, RULES)
        self.assertEqual(0, process.returncode, process.stderr)
        result = json.loads(process.stdout)
        self.assertTrue(result["ok"])
        self.assertEqual(3, result["rules"])
        self.assertEqual([], result["violations"])

    def test_standard_baseline_limits_source_lines_and_blocks_dotenv(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / ".harness").mkdir()
            (root / ".harness" / "ARCHITECTURE.md").write_text("# Architecture\n", encoding="utf-8")
            (root / "at-limit.py").write_text("line\n" * 800, encoding="utf-8")
            (root / "too-large.py").write_text("line\n" * 801, encoding="utf-8")
            (root / ".env").write_text("SECRET=value\n", encoding="utf-8")
            (root / ".env.example").write_text("SECRET=\n", encoding="utf-8")
            process = run_lint(root, RULES)
            self.assertEqual(1, process.returncode, process.stderr)
            result = json.loads(process.stdout)
            self.assertEqual(
                ["source-files-under-800-lines", "no-committed-environment-files"],
                [violation["rule"] for violation in result["violations"]],
            )
            self.assertEqual("too-large.py", result["violations"][0]["path"])
            self.assertEqual(801, result["violations"][0]["line"])
            self.assertEqual(".env", result["violations"][1]["path"])

    def test_standard_baseline_ignores_top_level_dependency_trees(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / ".harness").mkdir()
            (root / ".harness" / "ARCHITECTURE.md").write_text("# Architecture\n", encoding="utf-8")
            dependency = root / "node_modules" / "dependency" / "nested"
            dependency.mkdir(parents=True)
            (dependency / "large.js").write_text("line\n" * 900, encoding="utf-8")
            generated = root / "dist" / "assets"
            generated.mkdir(parents=True)
            (generated / "bundle.js").write_text("line\n" * 900, encoding="utf-8")
            process = run_lint(root, RULES)
            self.assertEqual(0, process.returncode, process.stderr)
            self.assertEqual([], json.loads(process.stdout)["violations"])

    def test_deterministic_rule_types_report_violations(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "src" / "domain").mkdir(parents=True)
            (root / "src" / "domain" / "model.py").write_text("from src.api import handler\n", encoding="utf-8")
            (root / "src" / "legacy").mkdir(parents=True)
            (root / "src" / "legacy" / "old.py").write_text("legacy = True\n", encoding="utf-8")
            (root / "src" / "main.py").write_text("def main(): pass\n", encoding="utf-8")
            config = root / "rules.json"
            config.write_text(
                json.dumps(
                    {
                        "version": 1,
                        "rules": [
                            {
                                "id": "no-api-import",
                                "type": "forbid_text",
                                "globs": ["src/domain/**/*.py"],
                                "pattern": "from src\\.api",
                                "message": "Domain must not import API.",
                            },
                            {
                                "id": "no-legacy",
                                "type": "forbid_path",
                                "glob": "src/legacy/*.py",
                                "message": "Legacy modules are forbidden.",
                            },
                            {
                                "id": "required-port",
                                "type": "require_path",
                                "path": "src/domain/ports.py",
                                "message": "The domain port must exist.",
                            },
                            {
                                "id": "wire-domain",
                                "type": "require_text",
                                "file": "src/main.py",
                                "pattern": "create_domain",
                                "message": "Entrypoint must wire the domain.",
                            },
                        ],
                    }
                ),
                encoding="utf-8",
            )
            process = run_lint(root, config)
            self.assertEqual(1, process.returncode, process.stderr)
            result = json.loads(process.stdout)
            self.assertFalse(result["ok"])
            self.assertEqual(
                ["no-api-import", "no-legacy", "required-port", "wire-domain"],
                [violation["rule"] for violation in result["violations"]],
            )
            self.assertEqual(1, result["violations"][0]["line"])

    def test_invalid_or_unknown_configuration_fails_closed(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            config = root / "rules.json"
            config.write_text(
                json.dumps(
                    {
                        "version": 1,
                        "rules": [
                            {
                                "id": "typo",
                                "type": "require_path",
                                "path": "src",
                                "message": "Required.",
                                "pth": "misspelled",
                            }
                        ],
                    }
                ),
                encoding="utf-8",
            )
            process = run_lint(root, config)
            self.assertEqual(2, process.returncode)
            self.assertIn("unknown fields", json.loads(process.stdout)["error"])

    def test_lint_stage_owns_the_authoritative_after_hook(self) -> None:
        harnessctl = load_harnessctl()
        pipeline = harnessctl.load_simple_yaml(PLUGIN / "pipelines" / "default.yaml")
        hooks = {
            stage["id"]: [hook["id"] for hook in stage["hooks"]["after"]]
            for stage in pipeline["stages"]
        }
        self.assertEqual(["architecture-lint"], hooks["lint"])
        self.assertFalse(any("architecture-lint" in ids for stage, ids in hooks.items() if stage != "lint"))
        hook = harnessctl.pipeline_stage(pipeline, "lint")["hooks"]["after"][0]
        self.assertEqual(["python3", ".harness/scripts/arch-lint.py"], hook["argv"])

    def test_bootstrap_installs_script_rules_and_hook(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            target = Path(temporary)
            process = subprocess.run(
                [
                    sys.executable,
                    str(BOOTSTRAP),
                    "bootstrap",
                    "--target",
                    str(target),
                    "--provider",
                    "linear",
                    "--workspace",
                    "workspace-id",
                    "--project",
                    "team-id",
                    "--notion-parent-page-id",
                    "page-id",
                    "--apply",
                ],
                text=True,
                capture_output=True,
                check=False,
            )
            self.assertEqual(0, process.returncode, process.stdout + process.stderr)
            installed_script = target / ".harness" / "scripts" / "arch-lint.py"
            installed_rules = target / ".harness" / "arch-lint-rules.json"
            self.assertTrue(installed_script.is_file())
            self.assertTrue(installed_rules.is_file())
            installed_run = run_lint(target, installed_rules)
            self.assertEqual(0, installed_run.returncode, installed_run.stdout + installed_run.stderr)


if __name__ == "__main__":
    unittest.main()
