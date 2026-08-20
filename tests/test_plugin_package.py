from __future__ import annotations

import json
import unittest
from pathlib import Path


REPO = Path(__file__).resolve().parents[1]
PLUGIN = REPO / "plugins" / "agent-harness"


class PluginPackageTests(unittest.TestCase):
    def test_manifest_companions_and_app_ids(self) -> None:
        manifest = json.loads((PLUGIN / ".codex-plugin" / "plugin.json").read_text())
        self.assertEqual("agent-harness", manifest["name"])
        self.assertEqual("./skills/", manifest["skills"])
        self.assertEqual("./.app.json", manifest["apps"])
        apps = json.loads((PLUGIN / ".app.json").read_text())["apps"]
        self.assertEqual(
            {"linear", "atlassian-rovo", "notion"},
            set(apps),
        )
        self.assertTrue(all(value["id"] for value in apps.values()))

    def test_skills_have_no_placeholders(self) -> None:
        skills = sorted((PLUGIN / "skills").glob("*/SKILL.md"))
        self.assertEqual(
            {
                "inspect-harness",
                "onboard-cloud-runner",
                "run-harness",
                "setup-harness",
                "test-control-plane-ui",
            },
            {skill.parent.name for skill in skills},
        )
        for skill in skills:
            content = skill.read_text()
            self.assertNotIn("TODO", content)
            self.assertTrue((skill.parent / "agents" / "openai.yaml").is_file())

    def test_repo_marketplace_exposes_plugin(self) -> None:
        marketplace = json.loads((REPO / ".agents" / "plugins" / "marketplace.json").read_text())
        self.assertEqual("agent-harness", marketplace["name"])
        entry = marketplace["plugins"][0]
        self.assertEqual("agent-harness", entry["name"])
        self.assertEqual("./plugins/agent-harness", entry["source"]["path"])
        self.assertEqual("AVAILABLE", entry["policy"]["installation"])

    def test_readme_documents_codex_driven_cloud_onboarding(self) -> None:
        readme = (REPO / "README.md").read_text()
        for required in (
            "curl -fsSL agents.railway.com | sh",
            "codex plugin marketplace add vessica-labs/agent-harness",
            "codex plugin add agent-harness@agent-harness",
            "Use $onboard-cloud-runner",
            "LINEAR_WEBHOOK_SECRET",
            "NOTION_TOKEN",
            "agent-harness cloud auth codex add --slots 3",
            "agent-harness cloud repo discover-linear",
            "agent-harness cloud repo add",
        ):
            self.assertIn(required, readme)

    def test_packaged_templates_match_canonical_templates(self) -> None:
        source = REPO / "harness-templates" / "base"
        packaged = PLUGIN / "assets" / "harness" / "base"
        source_files = {str(path.relative_to(source)): path.read_bytes() for path in source.rglob("*") if path.is_file()}
        packaged_files = {str(path.relative_to(packaged)): path.read_bytes() for path in packaged.rglob("*") if path.is_file()}
        self.assertEqual(source_files, packaged_files)

    def test_schemas_are_valid_json(self) -> None:
        schemas = sorted((PLUGIN / "schemas").glob("*.json"))
        self.assertGreaterEqual(len(schemas), 5)
        for schema in schemas:
            self.assertIsInstance(json.loads(schema.read_text()), dict)

    def test_cloud_runner_packages_the_canonical_deterministic_helper(self) -> None:
        runner = REPO / "cloud-runner"
        self.assertTrue((runner / "go.mod").is_file())
        self.assertTrue((runner / "Dockerfile").is_file())
        self.assertTrue((runner / "railway.json").is_file())
        self.assertEqual(
            (PLUGIN / "scripts" / "harnessctl.py").read_bytes(),
            (runner / "scripts" / "harnessctl.py").read_bytes(),
        )


if __name__ == "__main__":
    unittest.main()
