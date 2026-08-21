#!/usr/bin/env python3
"""Publish and deploy one versioned Agent Harness release."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import re
import shlex
import shutil
import subprocess
import sys
import time
from typing import Any
from urllib import error, request


ROOT = Path(__file__).resolve().parents[1]
REPOSITORY = "vessica-labs/agent-harness"
IMAGE_REPOSITORY = "ghcr.io/vessica-labs/agent-harness"
VERSION_PATTERN = re.compile(r"^v\d+\.\d+\.\d+(?:[-+][A-Za-z0-9.-]+)?$")
RELEASE_CANDIDATE_PATTERN = re.compile(r"^v(\d+)\.(\d+)\.(\d+)-rc\.(\d+)$")
EXPECTED_ASSETS = frozenset(
    {
        "agent-harness-darwin-amd64",
        "agent-harness-darwin-arm64",
        "agent-harness-linux-amd64",
        "agent-harness-linux-arm64",
        "harnessctl.py",
        "SHA256SUMS",
    }
)
FAILED_DEPLOYMENT_STATES = frozenset(
    {"CANCELLED", "CRASHED", "FAILED", "REMOVED", "SKIPPED"}
)
RAILWAY_ENV = {
    "RAILWAY_CALLER": "agent-harness-release",
    "RAILWAY_AGENT_SESSION": "railway-skill-agent-harness-release",
}


class ReleaseError(RuntimeError):
    pass


def command(
    args: list[str],
    *,
    capture: bool = False,
    env: dict[str, str] | None = None,
) -> str:
    print(f"+ {shlex.join(args)}", flush=True)
    process_env = os.environ.copy()
    if env:
        process_env.update(env)
    result = subprocess.run(
        args,
        cwd=ROOT,
        env=process_env,
        check=False,
        text=True,
        stdout=subprocess.PIPE if capture else None,
    )
    if result.returncode != 0:
        raise ReleaseError(f"command failed with exit code {result.returncode}: {shlex.join(args)}")
    return result.stdout.strip() if capture and result.stdout else ""


def json_command(args: list[str], *, railway: bool = False) -> Any:
    raw = command(args, capture=True, env=RAILWAY_ENV if railway else None)
    try:
        return json.loads(raw)
    except json.JSONDecodeError as exc:
        raise ReleaseError(f"command did not return valid JSON: {shlex.join(args)}") from exc


def require_tools(*names: str) -> None:
    missing = [name for name in names if shutil.which(name) is None]
    if missing:
        raise ReleaseError("missing required command(s): " + ", ".join(missing))


def validate_version(version: str) -> str:
    if not VERSION_PATTERN.fullmatch(version):
        raise ReleaseError("VERSION must be a release tag such as v0.1.0-rc.27")
    return version


def next_release_candidate(tags: list[str]) -> str:
    candidates: list[tuple[int, int, int, int]] = []
    for tag in tags:
        match = RELEASE_CANDIDATE_PATTERN.fullmatch(tag.strip())
        if match:
            candidates.append(tuple(int(value) for value in match.groups()))
    if not candidates:
        raise ReleaseError(
            "no release-candidate tags were found on origin; pass VERSION=vX.Y.Z-rc.N explicitly"
        )
    major, minor, patch, release_candidate = max(candidates)
    return f"v{major}.{minor}.{patch}-rc.{release_candidate + 1}"


def remote_tag_names(document: str) -> list[str]:
    prefix = "refs/tags/"
    names: list[str] = []
    for line in document.splitlines():
        fields = line.split()
        if (
            len(fields) == 2
            and fields[1].startswith(prefix)
            and not fields[1].endswith("^{}")
        ):
            names.append(fields[1].removeprefix(prefix))
    return names


def resolve_version(explicit: str | None) -> str:
    if explicit:
        return validate_version(explicit)
    require_tools("git")
    remote_tags = command(
        ["git", "ls-remote", "--tags", "--refs", "origin"], capture=True
    )
    version = next_release_candidate(remote_tag_names(remote_tags))
    print(f"Selected next release candidate: {version}")
    return version


def checkpoint_name(version: str) -> str:
    return "agent-harness-worker-" + version.removeprefix("v")


def target_image(version: str) -> str:
    return f"{IMAGE_REPOSITORY}:{version}"


def git(*args: str) -> str:
    return command(["git", *args], capture=True)


def ensure_releaseable(version: str, *, fetch: bool) -> str:
    require_tools("git", "gh", "go", "make", "python3")
    validate_version(version)
    if fetch:
        command(["git", "fetch", "origin", "main", "--tags"])
    if git("branch", "--show-current") != "main":
        raise ReleaseError("releases must be cut from main")
    if git("status", "--porcelain"):
        raise ReleaseError("the worktree must be clean before publishing a release")
    head = git("rev-parse", "HEAD")
    remote_main = git("rev-parse", "origin/main")
    ancestry = subprocess.run(
        ["git", "merge-base", "--is-ancestor", remote_main, head], cwd=ROOT, check=False
    )
    if ancestry.returncode != 0:
        raise ReleaseError("main is behind or diverged from origin/main; rebase before releasing")
    existing_tag = subprocess.run(
        ["git", "rev-parse", "-q", "--verify", f"refs/tags/{version}^{{commit}}"],
        cwd=ROOT,
        check=False,
        text=True,
        stdout=subprocess.PIPE,
    )
    if existing_tag.returncode == 0 and existing_tag.stdout.strip() != head:
        raise ReleaseError(f"tag {version} already points to a different commit")
    return head


def verify_local(version: str, *, fetch: bool = False) -> str:
    head = ensure_releaseable(version, fetch=fetch)
    command(["make", "verify", f"VERSION={version}"])
    if git("status", "--porcelain"):
        raise ReleaseError("verification changed the worktree; inspect and commit the generated changes")
    print(f"Release checks passed for {version} at {head[:12]}.")
    return head


def release_assets(version: str) -> set[str]:
    document = json_command(
        ["gh", "release", "view", version, "--repo", REPOSITORY, "--json", "assets"]
    )
    return {asset["name"] for asset in document.get("assets", [])}


def verify_release_assets(version: str) -> None:
    assets = release_assets(version)
    missing = EXPECTED_ASSETS - assets
    if missing:
        raise ReleaseError(
            f"GitHub release {version} is missing assets: {', '.join(sorted(missing))}"
        )


def wait_for_github_release(version: str, head: str, timeout: int = 1200) -> None:
    deadline = time.monotonic() + timeout
    run_id: str | None = None
    while time.monotonic() < deadline:
        runs = json_command(
            [
                "gh",
                "run",
                "list",
                "--repo",
                REPOSITORY,
                "--workflow",
                "cloud-runner.yml",
                "--commit",
                head,
                "--limit",
                "20",
                "--json",
                "databaseId,headBranch,headSha,status,conclusion",
            ]
        )
        matching = [run for run in runs if run.get("headBranch") == version]
        if matching:
            run_id = str(matching[0]["databaseId"])
            break
        time.sleep(5)
    if run_id is None:
        raise ReleaseError(f"GitHub Actions did not start for tag {version} within {timeout}s")
    command(["gh", "run", "watch", run_id, "--repo", REPOSITORY, "--exit-status"])
    verify_release_assets(version)
    print(f"GitHub release and GHCR image are ready for {version}.")


def publish(version: str) -> None:
    head = verify_local(version, fetch=True)
    command(["git", "push", "origin", "main"])
    existing = subprocess.run(
        ["git", "rev-parse", "-q", "--verify", f"refs/tags/{version}^{{commit}}"],
        cwd=ROOT,
        check=False,
    )
    if existing.returncode != 0:
        command(["git", "tag", "-a", version, "-m", f"Agent Harness {version}"])
    command(["git", "push", "origin", f"refs/tags/{version}"])
    wait_for_github_release(version, head)


def local_binary(version: str) -> Path:
    command(["make", "-C", "cloud-runner", "build", f"VERSION={version}"])
    return ROOT / "cloud-runner" / "bin" / "agent-harness"


def create_checkpoint(args: argparse.Namespace) -> None:
    require_tools("railway", "gh", "go", "make")
    version = validate_version(args.version)
    verify_release_assets(version)
    binary = local_binary(version)
    command(
        [
            str(binary),
            "railway",
            "upgrade",
            "--project",
            args.project,
            "--environment",
            args.environment,
            "--version",
            version,
            "--checkpoint",
            checkpoint_name(version),
        ]
    )


def railway_args(args: argparse.Namespace) -> list[str]:
    return [
        "--project",
        args.project,
        "--environment",
        args.environment,
        "--service",
        args.service,
    ]


def deployments(args: argparse.Namespace, limit: int = 10) -> list[dict[str, Any]]:
    document = json_command(
        ["railway", "deployment", "list", *railway_args(args), "--limit", str(limit), "--json"],
        railway=True,
    )
    if not isinstance(document, list):
        raise ReleaseError("Railway returned an unexpected deployment list")
    return document


def deployment_image(deployment: dict[str, Any]) -> str:
    meta = deployment.get("meta") or {}
    return str(meta.get("image") or "")


def connect_image_command(args: argparse.Namespace, image: str) -> list[str]:
    return [
        "railway",
        "service",
        "source",
        "connect",
        "--image",
        image,
        *railway_args(args),
        "--json",
    ]


def current_deployment(args: argparse.Namespace) -> dict[str, Any]:
    listed = deployments(args, limit=1)
    if not listed:
        raise ReleaseError("Railway returned no control-plane deployments")
    return listed[0]


def require_checkpoint(args: argparse.Namespace, version: str) -> None:
    document = json_command(
        [
            "railway",
            "sandbox",
            "checkpoint",
            "list",
            "--project",
            args.project,
            "--environment",
            args.environment,
            "--json",
        ],
        railway=True,
    )
    expected = checkpoint_name(version)
    if not isinstance(document, list) or not any(
        item.get("key") == expected or item.get("id") == expected
        for item in document
        if isinstance(item, dict)
    ):
        raise ReleaseError(
            f"Railway checkpoint {expected} does not exist; run make checkpoint VERSION={version} first"
        )


def healthcheck(url: str, timeout: int = 120) -> None:
    base = url.rstrip("/")
    for path in ("/healthz", "/readyz"):
        deadline = time.monotonic() + timeout
        last_error = "not attempted"
        while time.monotonic() < deadline:
            try:
                with request.urlopen(base + path, timeout=10) as response:
                    if 200 <= response.status < 300:
                        print(f"{path}: HTTP {response.status}")
                        break
                    last_error = f"HTTP {response.status}"
            except (error.URLError, TimeoutError) as exc:
                last_error = str(exc)
            time.sleep(3)
        else:
            raise ReleaseError(f"{base + path} did not become healthy: {last_error}")


def set_checkpoint_variable(args: argparse.Namespace, version: str) -> None:
    command(
        [
            "railway",
            "variable",
            "set",
            f"HARNESS_SANDBOX_CHECKPOINT={checkpoint_name(version)}",
            *railway_args(args),
            "--skip-deploys",
            "--json",
        ],
        env=RAILWAY_ENV,
    )


def wait_for_deployment(
    args: argparse.Namespace,
    image: str,
    previous_id: str,
    timeout: int = 1200,
) -> dict[str, Any]:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        candidates = [
            item
            for item in deployments(args)
            if item.get("id") != previous_id and deployment_image(item) == image
        ]
        if candidates:
            deployment = candidates[0]
            status = str(deployment.get("status") or "").upper()
            print(f"Railway deployment {deployment.get('id')}: {status or 'PENDING'}")
            if status == "SUCCESS":
                return deployment
            if status in FAILED_DEPLOYMENT_STATES:
                raise ReleaseError(
                    f"Railway deployment {deployment.get('id')} ended in {status}"
                )
        time.sleep(5)
    raise ReleaseError(f"Railway did not successfully deploy {image} within {timeout}s")


def deploy(args: argparse.Namespace) -> None:
    require_tools("railway", "gh")
    version = validate_version(args.version)
    verify_release_assets(version)
    require_checkpoint(args, version)
    image = target_image(version)
    previous = current_deployment(args)
    if deployment_image(previous) == image and previous.get("status") == "SUCCESS":
        set_checkpoint_variable(args, version)
        healthcheck(args.url)
        print(f"Production is already healthy on {image}.")
        return
    set_checkpoint_variable(args, version)
    if deployment_image(previous) == image:
        status_name = str(previous.get("status") or "").upper()
        if status_name in FAILED_DEPLOYMENT_STATES:
            command(
                ["railway", "redeploy", *railway_args(args), "--from-source", "--yes", "--json"],
                env=RAILWAY_ENV,
            )
            excluded_id = str(previous.get("id") or "")
        else:
            excluded_id = ""
    else:
        command(
            connect_image_command(args, image),
            env=RAILWAY_ENV,
        )
        excluded_id = str(previous.get("id") or "")
    deployed = wait_for_deployment(args, image, excluded_id)
    healthcheck(args.url)
    digest = (deployed.get("meta") or {}).get("imageDigest") or "unknown digest"
    print(f"Production is healthy on {image} ({digest}).")


def status(args: argparse.Namespace) -> None:
    require_tools("railway")
    deployed = current_deployment(args)
    print(
        json.dumps(
            {
                "id": deployed.get("id"),
                "status": deployed.get("status"),
                "image": deployment_image(deployed),
                "imageDigest": (deployed.get("meta") or {}).get("imageDigest"),
                "createdAt": deployed.get("createdAt"),
            },
            indent=2,
        )
    )
    if str(deployed.get("status") or "").upper() != "SUCCESS":
        raise ReleaseError("the latest Railway deployment is not successful")
    healthcheck(args.url, timeout=15)


def parser() -> argparse.ArgumentParser:
    root = argparse.ArgumentParser(description=__doc__)
    subparsers = root.add_subparsers(dest="action", required=True)
    for name in ("check", "publish", "release"):
        child = subparsers.add_parser(name)
        child.add_argument("--version")
        add_railway_arguments(child)
    for name in ("checkpoint", "deploy"):
        child = subparsers.add_parser(name)
        child.add_argument("--version", required=True)
        add_railway_arguments(child)
    status_parser = subparsers.add_parser("status")
    add_railway_arguments(status_parser)
    return root


def add_railway_arguments(target: argparse.ArgumentParser) -> None:
    target.add_argument("--project", required=True)
    target.add_argument("--environment", default="production")
    target.add_argument("--service", default="control-plane")
    target.add_argument("--url", required=True)


def main() -> int:
    args = parser().parse_args()
    try:
        if args.action == "check":
            verify_local(resolve_version(args.version), fetch=True)
        elif args.action == "publish":
            publish(resolve_version(args.version))
        elif args.action == "checkpoint":
            create_checkpoint(args)
        elif args.action == "deploy":
            deploy(args)
        elif args.action == "status":
            status(args)
        elif args.action == "release":
            args.version = resolve_version(args.version)
            publish(args.version)
            create_checkpoint(args)
            deploy(args)
        return 0
    except (ReleaseError, KeyboardInterrupt) as exc:
        print(f"release failed: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
