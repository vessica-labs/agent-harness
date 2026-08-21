import importlib.util
from argparse import Namespace
from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("agent_harness_release", ROOT / "scripts" / "release.py")
release = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(release)


class ReleaseWorkflowTests(unittest.TestCase):
    def test_release_candidate_version_maps_to_artifacts(self):
        version = release.validate_version("v0.1.0-rc.27")

        self.assertEqual(release.checkpoint_name(version), "agent-harness-worker-0.1.0-rc.27")
        self.assertEqual(
            release.target_image(version),
            "ghcr.io/vessica-labs/agent-harness:v0.1.0-rc.27",
        )

    def test_invalid_version_is_rejected(self):
        for version in ("dev", "0.1.0", "v0.1", "vnext", "v0.1.0 rc.27"):
            with self.subTest(version=version):
                with self.assertRaises(release.ReleaseError):
                    release.validate_version(version)

    def test_next_release_candidate_uses_highest_numeric_rc(self):
        self.assertEqual(
            release.next_release_candidate(
                [
                    "v0.1.0-rc.9",
                    "v0.1.0-rc.26",
                    "v0.1.0",
                    "v0.2.0-beta.1",
                    "not-a-version",
                ]
            ),
            "v0.1.0-rc.27",
        )

    def test_next_release_candidate_uses_newest_version_line(self):
        self.assertEqual(
            release.next_release_candidate(
                ["v0.1.0-rc.99", "v0.2.0-rc.1", "v0.2.0-rc.3"]
            ),
            "v0.2.0-rc.4",
        )

    def test_next_release_candidate_requires_an_existing_convention(self):
        with self.assertRaisesRegex(release.ReleaseError, "pass VERSION"):
            release.next_release_candidate(["v0.1.0", "v0.2.0-beta.1"])

    def test_remote_tag_names_uses_only_tag_refs(self):
        document = "\n".join(
            [
                "abc refs/tags/v0.1.0-rc.26",
                "def refs/heads/main",
                "ghi refs/tags/v0.1.0-rc.26^{}",
                "malformed",
            ]
        )

        self.assertEqual(
            release.remote_tag_names(document),
            ["v0.1.0-rc.26"],
        )

    def test_release_parser_allows_automatic_or_explicit_version(self):
        common = ["--project", "project", "--url", "https://example.test"]

        automatic = release.parser().parse_args(["release", *common])
        explicit = release.parser().parse_args(
            ["release", "--version", "v0.2.0-rc.1", *common]
        )

        self.assertIsNone(automatic.version)
        self.assertEqual(explicit.version, "v0.2.0-rc.1")

    def test_deployment_image_reads_railway_metadata(self):
        deployment = {
            "id": "deployment-id",
            "status": "SUCCESS",
            "meta": {"image": "ghcr.io/vessica-labs/agent-harness:v0.1.0-rc.27"},
        }

        self.assertEqual(
            release.deployment_image(deployment),
            "ghcr.io/vessica-labs/agent-harness:v0.1.0-rc.27",
        )

    def test_failed_deployment_states_are_terminal(self):
        self.assertTrue({"FAILED", "CRASHED", "CANCELLED"} <= release.FAILED_DEPLOYMENT_STATES)
        self.assertNotIn("DEPLOYING", release.FAILED_DEPLOYMENT_STATES)

    def test_deploy_connects_the_versioned_image_source(self):
        args = Namespace(project="project-id", environment="production", service="control-plane")

        self.assertEqual(
            release.connect_image_command(
                args, "ghcr.io/vessica-labs/agent-harness:v0.1.0-rc.28"
            ),
            [
                "railway",
                "service",
                "source",
                "connect",
                "--image",
                "ghcr.io/vessica-labs/agent-harness:v0.1.0-rc.28",
                "--project",
                "project-id",
                "--environment",
                "production",
                "--service",
                "control-plane",
                "--json",
            ],
        )

    def test_release_requires_all_runtime_assets(self):
        self.assertEqual(
            release.EXPECTED_ASSETS,
            {
                "agent-harness-darwin-amd64",
                "agent-harness-darwin-arm64",
                "agent-harness-linux-amd64",
                "agent-harness-linux-arm64",
                "harnessctl.py",
                "SHA256SUMS",
            },
        )


if __name__ == "__main__":
    unittest.main()
