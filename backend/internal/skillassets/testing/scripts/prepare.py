#!/usr/bin/env python3
"""Prepare a reusable local target, keeping dependencies and normal caches."""

import argparse
from contextlib import contextmanager
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys


def run(arguments, cwd):
    env = {key: value for key, value in os.environ.items() if not key.startswith("AO_")}
    try:
        return subprocess.run(arguments, cwd=cwd, env=env, check=True,
                              text=True, stdout=subprocess.PIPE,
                              stderr=subprocess.STDOUT).stdout.strip()
    except subprocess.CalledProcessError as error:
        print(error.stdout or "", file=sys.stderr, end="")
        raise


@contextmanager
def reserve(checkout, owner=None):
    path = checkout / ".ao-testing-active"
    if owner is not None:
        with path.open("r") as lease:
            if (lease.read() != owner or
                    not os.path.samestat(os.fstat(lease.fileno()), path.lstat())):
                raise RuntimeError("Target checkout reservation is not owned by this preparation")
            yield
        return
    with path.open("x") as lease:
        try:
            yield
        finally:
            if not os.path.samestat(os.fstat(lease.fileno()), path.lstat()):
                raise RuntimeError("Target checkout reservation changed; leaving it untouched")
            path.unlink()


def install(directory, stamp):
    if (directory / "node_modules").is_symlink():
        raise RuntimeError("Target node_modules must belong to its checkout, not a symlink")
    lockfile = directory / "package-lock.json"
    digest = hashlib.sha256(lockfile.read_bytes()).hexdigest()
    if stamp.exists() and stamp.read_text().strip() == digest and (directory / "node_modules").is_dir():
        return
    run(["npm", "ci", "--prefer-offline", "--no-audit", "--no-fund"], directory)
    stamp.write_text(hashlib.sha256(lockfile.read_bytes()).hexdigest() + "\n")


def preflight(checkout):
    backend, frontend = checkout / "backend", checkout / "frontend"
    main = frontend / "src/main.ts"
    launcher = frontend / "src/shared/daemon-launch.ts"
    if (not main.is_file() or not launcher.is_file() or
            '"./shared/daemon-launch"' not in main.read_text() or
            not re.search(r"resolveDaemonLaunch\s*\(", main.read_text())):
        raise RuntimeError("unsupported_revision: frontend/src/main.ts does not honor AO_DAEMON_COMMAND")
    match = re.search(r"^go (\d+)\.(\d+)(?:\.(\d+))?\s*$", (backend / "go.mod").read_text(), re.M)
    if not match:
        raise RuntimeError("unsupported_revision: backend/go.mod has no supported Go version directive")
    required_go = tuple(int(n or 0) for n in match.groups())
    try:
        go_version = run(["go", "env", "GOVERSION"], backend)
        node_version = run(["node", "--version"], frontend)
    except (OSError, subprocess.CalledProcessError) as error:
        raise RuntimeError(f"unsupported_revision: runtime probe failed: {error}") from error
    actual_go = re.fullmatch(r"go(\d+)\.(\d+)(?:\.(\d+))?", go_version)
    if not actual_go or tuple(int(n or 0) for n in actual_go.groups()) < required_go:
        raise RuntimeError(f"unsupported_revision: Go {go_version} does not satisfy go {match.group(0)[3:]}")
    package = json.loads((frontend / "package.json").read_text())
    if not re.fullmatch(r"v\d+\.\d+\.\d+", node_version):
        raise RuntimeError(f"unsupported_revision: unrecognized Node version {node_version}")
    required_node = package.get("engines", {}).get("node")
    if required_node:
        npm_root = run(["npm", "root", "--global"], frontend)
        script = ("const r=require('node:module').createRequire(process.argv[1]+'/npm/package.json');"
                  "console.log(r('semver').satisfies(process.argv[2],process.argv[3]));")
        if run(["node", "-e", script, npm_root, node_version, required_node], frontend) != "true":
            raise RuntimeError(f"unsupported_revision: Node {node_version} does not satisfy {required_node}")
    script = ("const m=await import(process.argv[1]);"
              "const command='AO_PREFLIGHT_COMMAND';"
              "const r=m.resolveDaemonLaunch({AO_DAEMON_COMMAND:command},false,'/unused',process.argv[2],'/unused','darwin');"
              "if(!r||r.command!==command||r.cwd!==process.argv[2]||r.source!=='configured'||r.shell!==true)"
              "throw new Error('daemon launcher does not honor AO_DAEMON_COMMAND');")
    try:
        run(["node", "--experimental-strip-types", "--input-type=module", "-e", script,
             launcher.as_uri(), str(frontend)], frontend)
    except (OSError, subprocess.CalledProcessError) as error:
        raise RuntimeError(f"unsupported_revision: configured daemon launch probe failed: {error}") from error
    return {"goVersion": go_version, "requiredGo": ".".join(map(str, required_go)),
            "nodeVersion": node_version, "requiredNode": required_node,
            "daemonCommandOverride": True}


def prepare(repository, commit, cache, reservation_owner=None):
    repository = repository.resolve()
    try:
        origin = run(["git", "remote", "get-url", "origin"], repository)
    except subprocess.CalledProcessError:
        origin = run(["git", "rev-parse", "--path-format=absolute", "--git-common-dir"], repository)
    root = (repository.parent if repository.name == "checkout" and repository.parent.parent == cache
            else cache / hashlib.sha256(origin.encode()).hexdigest()[:16])
    root.mkdir(parents=True, exist_ok=True)
    if root.resolve() != root:
        raise RuntimeError("Target cache must not contain symlinks")
    checkout = root / "checkout"
    with (root / "prepare.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        if not checkout.exists():
            run(["git", "clone", "--no-checkout", "--reference-if-able", str(repository),
                 "--dissociate", origin, str(checkout)], root)
        else:
            if checkout.is_symlink():
                raise RuntimeError("Target checkout must not be a symlink")
            if run(["git", "remote", "get-url", "origin"], checkout) != origin:
                raise RuntimeError("Target checkout belongs to a different repository")
            if run(["git", "status", "--porcelain", "--untracked-files=no"], checkout):
                raise RuntimeError("Target checkout has tracked edits; preserve them before preparing")
        with reserve(checkout, reservation_owner):
            run(["git", "fetch", "--no-tags", "origin"], checkout)
            # Admit local, unpushed revisions without changing the cached remote.
            source = "origin" if repository == checkout else str(repository)
            if repository != checkout:
                run(["git", "fetch", "--no-tags", source, commit], checkout)
            else:
                # PR intake may have fetched this commit from a fork, not origin.
                run(["git", "cat-file", "-e", commit + "^{commit}"], checkout)
            run(["git", "checkout", "--detach", commit], checkout)
            facts = preflight(checkout)
            for directory in ["frontend", "packages/product-ui"]:
                install(checkout / directory, root / (directory.replace("/", "-") + ".sha256"))
            frontend = checkout / "frontend"
            adapter = Path(__file__).resolve().parent
            # The existing runtime builder skips unchanged source signatures.
            run(["node", "scripts/build-acp-runtime.mjs"], frontend)
            run(["node", str(adapter / "prepare.cjs"), str(frontend)], frontend)
            manifest_path = frontend / ".vite/testing-target.json"
            manifest = json.loads(manifest_path.read_text())
            manifest["preflight"] = facts
            manifest_path.write_text(json.dumps(manifest) + "\n")
    return checkout


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repository", type=Path, default=Path(__file__).resolve().parents[5])
    parser.add_argument("--commit", help="Exact base or head commit; defaults to repository HEAD")
    parser.add_argument("--reservation-owner", help="Controller-owned checkout reservation")
    args = parser.parse_args()
    commit = args.commit or run(["git", "rev-parse", "HEAD"], args.repository)
    if len(commit) not in (40, 64) or any(c not in "0123456789abcdef" for c in commit):
        parser.error("--commit must be a full lowercase commit SHA")
    checkout = prepare(args.repository, commit,
                       Path.home() / ".ao/dev/agentic-target/repos", args.reservation_owner)
    print(f"Prepared target checkout: {checkout}")


if __name__ == "__main__":
    main()
