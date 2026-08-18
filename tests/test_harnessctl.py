from __future__ import annotations

import importlib.util
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


REPO = Path(__file__).resolve().parents[1]
PLUGIN = REPO / "plugins" / "agent-harness"
HARNESSCTL = PLUGIN / "scripts" / "harnessctl.py"
BOOTSTRAP = PLUGIN / "scripts" / "bootstrap.py"


def load_harnessctl():
    spec = importlib.util.spec_from_file_location("harnessctl", HARNESSCTL)
    assert spec and spec.loader
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def run_json(*args: str, check: bool = True) -> tuple[subprocess.CompletedProcess[str], dict]:
    process = subprocess.run([sys.executable, *args], cwd=REPO, text=True, capture_output=True, check=False)
    if check and process.returncode != 0:
        raise AssertionError(f"command failed: {process.args}\nstdout={process.stdout}\nstderr={process.stderr}")
    return process, json.loads(process.stdout)


class YamlAndPipelineTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.harnessctl = load_harnessctl()

    def test_default_pipeline_is_valid_and_ordered(self) -> None:
        pipeline = self.harnessctl.load_simple_yaml(PLUGIN / "pipelines" / "default.yaml")
        self.assertEqual([], self.harnessctl.validate_pipeline(pipeline))
        self.assertEqual(
            ["product", "arch", "coder", "lint", "qa", "pr"],
            self.harnessctl.stage_order(pipeline),
        )
        coder = self.harnessctl.pipeline_stage(pipeline, "coder")
        self.assertEqual(3, coder["parallelism"])
        self.assertEqual("ticket_parallel", coder["mode"])
        self.assertEqual("inputs/tickets/{ticket_key}.json", coder["inputs"][0]["file"])
        self.assertEqual(
            [{"from": "qa", "to": "coder", "through": "qa", "max_reentries": 2}],
            pipeline["repair_loops"],
        )

    def test_exact_stage_selection_requires_unselected_prerequisites(self) -> None:
        pipeline = self.harnessctl.load_simple_yaml(PLUGIN / "pipelines" / "default.yaml")
        selected = self.harnessctl.resolve_stages(pipeline, "product,architecture", set())
        self.assertEqual(["product", "arch"], selected["selected"])
        self.assertEqual({}, selected["missing_prerequisites"])
        later = self.harnessctl.resolve_stages(pipeline, "qa", {"lint"})
        self.assertEqual({}, later["missing_prerequisites"])
        blocked = self.harnessctl.resolve_stages(pipeline, "qa", set())
        self.assertEqual({"qa": ["lint"]}, blocked["missing_prerequisites"])

    def test_hook_shape_and_unsafe_cwd_validation(self) -> None:
        pipeline = self.harnessctl.load_simple_yaml(PLUGIN / "pipelines" / "default.yaml")
        pipeline["stages"][0]["hooks"]["before"] = [
            {"id": "check", "argv": ["python3", "-V"], "cwd": "../outside", "timeout_seconds": 20}
        ]
        errors = self.harnessctl.validate_pipeline(pipeline)
        self.assertTrue(any("cwd must be repo-relative" in error for error in errors))

    def test_user_can_customize_parallelism_and_stage_dag(self) -> None:
        pipeline = self.harnessctl.load_simple_yaml(PLUGIN / "pipelines" / "default.yaml")
        self.harnessctl.pipeline_stage(pipeline, "coder")["parallelism"] = 5
        pipeline["stages"] = [stage for stage in pipeline["stages"] if stage["id"] != "lint"]
        self.harnessctl.pipeline_stage(pipeline, "qa")["needs"] = ["coder"]
        self.assertEqual([], self.harnessctl.validate_pipeline(pipeline))
        self.assertEqual(["product", "arch", "coder", "qa", "pr"], self.harnessctl.stage_order(pipeline))

    def test_repair_loop_must_reenter_an_earlier_stage(self) -> None:
        pipeline = self.harnessctl.load_simple_yaml(PLUGIN / "pipelines" / "default.yaml")
        pipeline["repair_loops"][0]["to"] = "pr"
        errors = self.harnessctl.validate_pipeline(pipeline)
        self.assertTrue(any("must re-enter an earlier stage" in error for error in errors))

    def test_custom_stage_and_agent_use_base_result_contract(self) -> None:
        pipeline = self.harnessctl.load_simple_yaml(PLUGIN / "pipelines" / "default.yaml")
        pipeline["stages"].insert(
            -1,
            {
                "id": "security-review",
                "agent": ".agents/security-review.md",
                "needs": ["qa"],
                "mode": "single",
                "parallelism": 1,
                "inputs": [],
                "outputs": [],
                "result": {"file": "agent-output/security-review.json", "format": "json", "agent": "security-review"},
                "hooks": {"before": [], "after": [], "on_failure": []},
            },
        )
        self.harnessctl.pipeline_stage(pipeline, "pr")["needs"] = ["security-review"]
        self.assertEqual([], self.harnessctl.validate_pipeline(pipeline))
        self.assertEqual(
            [],
            self.harnessctl.validate_generic_agent_output(
                "security-review",
                {"agent": "security-review", "status": "completed", "worktree_clean": True, "blocker": None, "residual_risks": []},
            ),
        )


class AgentOutputTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.harnessctl = load_harnessctl()

    def product(self) -> dict:
        headings = "\n\n".join(
            [
                "# PRD: Example",
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
        )
        return {
            "agent": "product",
            "status": "ready",
            "source_issue": {"key": "L-123", "url": "https://linear.app/x", "title": "Example"},
            "prd_markdown": headings,
            "tickets": [
                {
                    "key": "L-123-T01",
                    "type": "feature",
                    "title": "Add model",
                    "objective": "Add the model",
                    "acceptance_criteria": ["AC-1"],
                    "owned_paths": ["internal/model"],
                    "depends_on": [],
                    "focused_checks": ["go test ./internal/model"],
                    "commit_message": "add model",
                    "complexity": "s",
                },
                {
                    "key": "L-123-T02",
                    "type": "test",
                    "title": "Add API",
                    "objective": "Expose the model",
                    "acceptance_criteria": ["AC-1"],
                    "owned_paths": ["internal/api"],
                    "depends_on": ["L-123-T01"],
                    "focused_checks": ["go test ./internal/api"],
                    "commit_message": "add API",
                    "complexity": "m",
                },
            ],
            "coverage": [{"requirement": "R1", "tickets": ["L-123-T01", "L-123-T02"]}],
            "blockers": [],
        }

    def test_product_contract_and_waves(self) -> None:
        product = self.product()
        self.assertEqual([], self.harnessctl.validate_product_output(product))
        self.assertEqual([["L-123-T01"], ["L-123-T02"]], self.harnessctl.dependency_waves(product["tickets"]))

    def test_cycle_and_parallel_path_overlap_fail(self) -> None:
        product = self.product()
        product["tickets"][0]["depends_on"] = ["L-123-T02"]
        errors = self.harnessctl.validate_product_output(product)
        self.assertTrue(any("cycle" in error for error in errors))

        product = self.product()
        product["tickets"][1]["depends_on"] = []
        product["tickets"][1]["owned_paths"] = ["internal/model/file.go"]
        errors = self.harnessctl.validate_product_output(product)
        self.assertTrue(any("overlap" in error for error in errors))

    def test_architect_contract_requires_headings_and_known_tickets(self) -> None:
        markdown = "\n\n".join(
            [
                "# ADR: Example",
                "## Context",
                "## Decision Drivers",
                "## Decision",
                "## Consequences",
                "## Alternatives Considered",
                "## Ticket Constraints",
            ]
        )
        output = {
            "agent": "architect",
            "status": "ready",
            "adr_filename": "ADR-L-123-example.md",
            "adr_markdown": markdown,
            "ticket_constraints": [
                {"ticket_key": "L-123-T01", "constraints": [], "required_owned_paths": [], "additional_dependencies": []}
            ],
            "ticket_graph_valid": True,
            "blockers": [],
        }
        self.assertEqual([], self.harnessctl.validate_architect_output(output, {"L-123-T01"}))
        output["ticket_constraints"][0]["ticket_key"] = "UNKNOWN"
        self.assertTrue(self.harnessctl.validate_architect_output(output, {"L-123-T01"}))

    def test_docs_and_qa_contracts(self) -> None:
        docs = {
            "agent": "docs",
            "status": "completed",
            "commits": [],
            "documents": [],
            "external_documents": [
                {"title": "Operator guide", "markdown": "# Operator guide", "purpose": "Operations", "verified_against": ["cmd"]}
            ],
            "checks": [],
            "worktree_clean": True,
            "blocker": None,
            "residual_risks": [],
        }
        self.assertEqual([], self.harnessctl.validate_generic_agent_output("docs", docs))
        qa = {
            "agent": "qa",
            "status": "requeue",
            "acceptance_results": [{"criterion": "AC-1", "status": "FAIL"}],
            "commits": [],
            "new_tickets": [],
            "worktree_clean": True,
            "blocker": None,
            "residual_risks": [],
        }
        self.assertTrue(any("requires new_tickets" in error for error in self.harnessctl.validate_generic_agent_output("qa", qa)))

    def test_lint_contract_requires_all_deterministic_gates(self) -> None:
        lint = {
            "agent": "lint",
            "status": "passed",
            "commits": [],
            "gates": {
                "lint": {"command": "lint", "status": "PASS", "result": "ok"},
                "lint_arch": {"command": "python3 .harness/scripts/arch-lint.py", "status": "PASS", "result": "ok"},
                "build": {"command": "build", "status": "PASS", "result": "ok"},
            },
            "worktree_clean": True,
            "blocker": None,
            "residual_risks": [],
        }
        self.assertEqual([], self.harnessctl.validate_generic_agent_output("lint", lint))
        lint["gates"]["lint_arch"]["status"] = "FAIL"
        self.assertTrue(any("every gate" in error for error in self.harnessctl.validate_generic_agent_output("lint", lint)))

    def test_yaml_file_contract_materializes_source_results_and_tickets(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            run_dir = Path(temporary) / "run_test"
            run_dir.mkdir()
            request = Path(temporary) / "request.md"
            request.write_text("Build the requested feature.\n", encoding="utf-8")
            run_json(
                str(HARNESSCTL),
                "materialize-source",
                "--pipeline",
                str(PLUGIN / "pipelines" / "default.yaml"),
                "--run-dir",
                str(run_dir),
                "--stage",
                "product",
                "--input-id",
                "feature_request",
                "--source",
                "user_prompt",
                "--content-file",
                str(request),
            )
            self.assertEqual("Build the requested feature.\n", (run_dir / "inputs" / "feature-request.md").read_text())

            product_file = Path(temporary) / "product.json"
            product_file.write_text(json.dumps(self.product()), encoding="utf-8")
            _, result = run_json(
                str(HARNESSCTL),
                "materialize-result",
                "--pipeline",
                str(PLUGIN / "pipelines" / "default.yaml"),
                "--run-dir",
                str(run_dir),
                "--stage",
                "product",
                "--input",
                str(product_file),
            )
            self.assertEqual("product", result["agent"])
            self.assertTrue((run_dir / "agent-output" / "product.json").is_file())
            self.assertTrue((run_dir / "artifacts" / "prd.md").is_file())
            self.assertEqual(2, len(json.loads((run_dir / "artifacts" / "ticket-plan.json").read_text())))

            _, generated = run_json(
                str(HARNESSCTL),
                "materialize-generated-inputs",
                "--pipeline",
                str(PLUGIN / "pipelines" / "default.yaml"),
                "--run-dir",
                str(run_dir),
                "--stage",
                "coder",
            )
            self.assertEqual(2, len(generated["materialized"]))
            self.assertTrue((run_dir / "inputs" / "tickets" / "L-123-T01.json").is_file())


class BootstrapAndStateTests(unittest.TestCase):
    def bootstrap(self, target: Path, *, apply: bool) -> dict:
        args = [
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
        ]
        if apply:
            args.append("--apply")
        return run_json(*args)[1]

    def test_preview_apply_validate_and_preserve_conflict(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            target = Path(temporary)
            preview = self.bootstrap(target, apply=False)
            self.assertEqual("preview", preview["mode"])
            self.assertTrue(all(item["action"] == "create" for item in preview["operations"]))
            self.bootstrap(target, apply=True)
            self.assertTrue((target / ".agents" / "product.md").is_file())
            self.assertTrue((target / ".harness" / "pipeline.yaml").is_file())
            self.assertEqual(
                (PLUGIN / "pipelines" / "default.yaml").read_bytes(),
                (target / ".harness" / "pipeline.yaml").read_bytes(),
            )
            self.assertNotIn("coder_concurrency", (target / ".harness" / "config.yaml").read_text())
            self.assertIn("label: \"agent-harness\"", (target / ".harness" / "config.yaml").read_text())
            run_json(str(HARNESSCTL), "validate-config", str(target / ".harness" / "config.yaml"))
            run_json(str(HARNESSCTL), "validate-pipeline", str(target / ".harness" / "pipeline.yaml"), "--repo", str(target))

            design = target / ".harness" / "DESIGN.md"
            design.write_text("user content\n", encoding="utf-8")
            process, result = run_json(
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
                check=False,
            )
            self.assertNotEqual(0, process.returncode)
            self.assertFalse(result["ok"])
            self.assertEqual("user content\n", design.read_text(encoding="utf-8"))

    def test_run_journal_lease_checkpoint_comments_and_resume(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            target = Path(temporary)
            self.bootstrap(target, apply=True)
            _, created = run_json(
                str(HARNESSCTL),
                "init-run",
                "--repo",
                str(target),
                "--provider",
                "linear",
                "--issue-key",
                "L-123",
                "--stages",
                "full",
            )
            run_dir = Path(created["run_dir"])
            self.assertFalse(created["resumed"])
            process, lease_error = run_json(
                str(HARNESSCTL),
                "init-run",
                "--repo",
                str(target),
                "--provider",
                "linear",
                "--issue-key",
                "L-123",
                "--stages",
                "full",
                check=False,
            )
            self.assertNotEqual(0, process.returncode)
            self.assertIn("leased", lease_error["error"])
            self.assertEqual(1, len(list((target / ".harness" / "runs").glob("run_*/state.json"))))
            run_json(
                str(HARNESSCTL),
                "checkpoint",
                "--run-dir",
                str(run_dir),
                "--patch-json",
                json.dumps({"tickets": [{"key": "L-123-T01", "depends_on": [], "status": "pending"}]}),
                "--event",
                "tickets.planned",
            )
            run_json(
                str(HARNESSCTL),
                "set-stage",
                "--run-dir",
                str(run_dir),
                "--stage",
                "product",
                "--status",
                "completed",
                "--details-json",
                json.dumps({"summary": "PRD published"}),
            )
            comment = subprocess.run(
                [sys.executable, str(HARNESSCTL), "render-comment", "--run-dir", str(run_dir), "--kind", "parent"],
                text=True,
                capture_output=True,
                check=True,
            ).stdout
            self.assertIn(f"agent-harness:run:{created['run_id']}", comment)
            self.assertIn("L-123-T01", comment)
            summary = subprocess.run(
                [sys.executable, str(HARNESSCTL), "render-comment", "--run-dir", str(run_dir), "--kind", "summary"],
                text=True,
                capture_output=True,
                check=True,
            ).stdout
            self.assertIn(f"agent-harness:summary:{created['run_id']}", summary)
            self.assertIn("Completed stages: product", summary)

            run_json(
                str(HARNESSCTL),
                "release-lease",
                "--repo",
                str(target),
                "--issue-key",
                "L-123",
                "--session-token",
                created["session_token"],
            )
            _, resumed = run_json(
                str(HARNESSCTL),
                "init-run",
                "--repo",
                str(target),
                "--provider",
                "linear",
                "--issue-key",
                "L-123",
                "--stages",
                "full",
            )
            self.assertTrue(resumed["resumed"])
            self.assertEqual(created["run_id"], resumed["run_id"])

    def test_hook_runner_has_fixed_env_and_fails_closed(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            target = Path(temporary)
            spec = {"id": "env", "argv": [sys.executable, "-c", "import os; print(os.environ['HARNESS_STAGE'])"], "cwd": ".", "timeout_seconds": 10}
            _, result = run_json(
                str(HARNESSCTL),
                "run-hook",
                "--repo",
                str(target),
                "--spec-json",
                json.dumps(spec),
                "--env-json",
                json.dumps(
                    {
                        "HARNESS_RUN_ID": "run_test",
                        "HARNESS_ISSUE_KEY": "L-123",
                        "HARNESS_STAGE": "product",
                        "HARNESS_ARTIFACT_DIR": str(target),
                        "HARNESS_WORKTREE": str(target),
                    }
                ),
            )
            self.assertTrue(result["ok"])
            self.assertEqual("product", result["stdout"].strip())

            spec["argv"] = [sys.executable, "-c", "raise SystemExit(7)"]
            process, result = run_json(
                str(HARNESSCTL),
                "run-hook",
                "--repo",
                str(target),
                "--spec-json",
                json.dumps(spec),
                "--env-json",
                json.dumps(
                    {
                        "HARNESS_RUN_ID": "run_test",
                        "HARNESS_ISSUE_KEY": "L-123",
                        "HARNESS_STAGE": "product",
                        "HARNESS_ARTIFACT_DIR": str(target),
                        "HARNESS_WORKTREE": str(target),
                    }
                ),
                check=False,
            )
            self.assertNotEqual(0, process.returncode)
            self.assertFalse(result["ok"])
            self.assertEqual(7, result["exit_code"])

    def test_markers_are_stable_for_provider_objects(self) -> None:
        _, result = run_json(
            str(HARNESSCTL),
            "markers",
            "--run-id",
            "run_123",
            "--provider",
            "linear",
            "--issue-key",
            "L-123",
            "--ticket-key",
            "L-123-T01",
            "--artifact",
            "Operator Guide",
        )
        markers = result["markers"]
        self.assertEqual("<!-- agent-harness:notion-hub:linear:l-123 -->", markers["notion_hub"])
        self.assertEqual("<!-- agent-harness:notion-artifact:run_123:operator-guide -->", markers["notion_artifact"])
        self.assertIn("L-123-T01", markers["child_body"])


if __name__ == "__main__":
    unittest.main()
