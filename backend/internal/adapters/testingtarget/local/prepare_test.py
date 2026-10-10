import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("prepare", Path(__file__).resolve().parents[3] / "skillassets/testing/scripts/prepare.py")
prepare = importlib.util.module_from_spec(spec)
spec.loader.exec_module(prepare)


class WarmPreparationTest(unittest.TestCase):
    def test_reuses_checkout_and_installs_only_changed_locks(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp).resolve()
            calls = []

            def run(args, cwd):
                calls.append((args, cwd))
                if args[:2] == ["git", "clone"]:
                    checkout = Path(args[-1])
                    for name in ["frontend", "packages/product-ui"]:
                        folder = checkout / name
                        folder.mkdir(parents=True)
                        (folder / "package-lock.json").write_text(name)
                        (folder / "node_modules").mkdir()
                        (folder / "node_modules/kept").write_text("cached")
                if args[0] == "node" and Path(args[1]).name == "prepare.cjs":
                    (cwd / ".vite").mkdir(exist_ok=True)
                    (cwd / ".vite/testing-target.json").write_text("{}")
                return "https://example.test/repo.git" if "get-url" in args else ""

            with patch.object(prepare, "run", side_effect=run), patch.object(prepare, "preflight", return_value={"goVersion": "go1.27.1", "nodeVersion": "v22.23.2"}):
                first = prepare.prepare(root, "a" * 40, root / "cache")
                second = prepare.prepare(first, "b" * 40, root / "cache")
                self.assertEqual(first, second)
                self.assertIn((["git", "cat-file", "-e", "b" * 40 + "^{commit}"], first), calls)
                self.assertNotIn((["git", "fetch", "--no-tags", "origin", "b" * 40], first), calls)
                self.assertEqual(sum(args[:2] == ["git", "clone"] for args, _ in calls), 1)
                self.assertEqual(sum(args[:2] == ["npm", "ci"] for args, _ in calls), 2)
                (first / "frontend/package-lock.json").write_text("changed")
                prepare.prepare(root, "c" * 40, root / "cache")
                self.assertEqual(sum(args[:2] == ["npm", "ci"] for args, _ in calls), 3)
                (first / ".ao-testing-active").write_text("live launch")
                with self.assertRaises(FileExistsError):
                    prepare.prepare(root, "d" * 40, root / "cache")
                self.assertEqual((first / ".ao-testing-active").read_text(), "live launch")
                self.assertEqual((first / "frontend/node_modules/kept").read_text(), "cached")
                self.assertFalse(any("clean" in args for args, _ in calls))

    def test_controller_reservation_is_validated_and_left_to_its_owner(self):
        with tempfile.TemporaryDirectory() as tmp:
            checkout = Path(tmp)
            lease = checkout / ".ao-testing-active"
            lease.write_text("owned-attempt")
            with prepare.reserve(checkout, "owned-attempt"):
                self.assertEqual(lease.read_text(), "owned-attempt")
            self.assertEqual(lease.read_text(), "owned-attempt")
            with self.assertRaisesRegex(RuntimeError, "not owned by this preparation"):
                with prepare.reserve(checkout, "other-attempt"):
                    self.fail("foreign reservation admitted")
            self.assertEqual(lease.read_text(), "owned-attempt")

    def test_failed_install_does_not_stamp_or_override_caches(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "package-lock.json").write_text("lock")
            with patch.object(prepare, "run", side_effect=subprocess.CalledProcessError(1, "npm")):
                with self.assertRaises(subprocess.CalledProcessError):
                    prepare.install(root, root / "stamp")
            self.assertFalse((root / "stamp").exists())
        with patch.dict(os.environ, {"GOCACHE": "/normal/go", "npm_config_cache": "/normal/npm",
                                     "AO_DATA_DIR": "/supervisor"}):
            with patch.object(subprocess, "run") as command:
                command.return_value.stdout = ""
                prepare.run(["git", "status"], Path("."))
                env = command.call_args.kwargs["env"]
                self.assertEqual(env["GOCACHE"], "/normal/go")
                self.assertEqual(env["npm_config_cache"], "/normal/npm")
                self.assertNotIn("AO_DATA_DIR", env)


class PreflightTest(unittest.TestCase):
    def fixture(self, root, override=True, go="1.27.1", node=None):
        (root / "backend").mkdir()
        (root / "backend/go.mod").write_text(f"module example.test/ao\n\ngo {go}\n")
        (root / "frontend/src").mkdir(parents=True)
        (root / "frontend/src/main.ts").write_text('import {resolveDaemonLaunch} from "./shared/daemon-launch"; resolveDaemonLaunch();' if override else "const cmd = 'go run';")
        (root / "frontend/src/shared").mkdir()
        (root / "frontend/src/shared/daemon-launch.ts").write_text("export function resolveDaemonLaunch() {}")
        (root / "frontend/package.json").write_text(json.dumps({"engines": {"node": node}} if node else {}))

    def test_records_effective_runtimes_without_inventing_missing_node_constraint(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.fixture(root)
            with patch.object(prepare, "run", side_effect=["go1.27.1", "v22.23.2", ""]):
                facts = prepare.preflight(root)
            self.assertEqual(facts, {"goVersion": "go1.27.1", "requiredGo": "1.27.1",
                                     "nodeVersion": "v22.23.2", "requiredNode": None,
                                     "daemonCommandOverride": True})

    def test_rejects_missing_override_before_running_tools(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.fixture(root, override=False)
            with patch.object(prepare, "run") as run:
                with self.assertRaisesRegex(RuntimeError, "unsupported_revision: frontend/src/main.ts does not honor AO_DAEMON_COMMAND"):
                    prepare.preflight(root)
                run.assert_not_called()

    def test_rejects_incompatible_go_and_node_with_exact_requirement(self):
        for requirement in ["go", "node"]:
            with self.subTest(requirement=requirement), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                self.fixture(root, node=">=22")
                answers = ["go1.26.0", "v22.23.2"] if requirement == "go" else ["go1.27.1", "v20.19.0", "/npm", "false"]
                with patch.object(prepare, "run", side_effect=answers):
                    with self.assertRaisesRegex(RuntimeError, "unsupported_revision: (Go|Node).+does not satisfy"):
                        prepare.preflight(root)

    def test_rejects_launcher_that_ignores_configured_command(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.fixture(root)
            failure = subprocess.CalledProcessError(1, "node")
            with patch.object(prepare, "run", side_effect=["go1.27.1", "v22.23.2", failure]):
                with self.assertRaisesRegex(RuntimeError, "unsupported_revision: configured daemon launch probe failed"):
                    prepare.preflight(root)
