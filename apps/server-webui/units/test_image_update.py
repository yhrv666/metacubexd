from contextlib import ExitStack
import hashlib
import importlib.util
import json
from pathlib import Path
from tempfile import TemporaryDirectory
from types import SimpleNamespace
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location(
    "image_update", Path(__file__).with_name("metacubexd-image-update.py")
)
updater = importlib.util.module_from_spec(spec)
spec.loader.exec_module(updater)

OLD = "server-v0.1.0-rc.2"
NEW = "server-v0.1.0-rc.3"
DIGEST = "a" * 64
REF = updater.REGISTRY + "@sha256:" + DIGEST


class ImageUpdateTest(unittest.TestCase):
    def setUp(self):
        contexts = ExitStack()
        self.addCleanup(contexts.close)
        temp = contexts.enter_context(TemporaryDirectory())
        root = Path(temp) / "versions"
        root.mkdir()
        state = Path(temp) / "state"
        state.mkdir()
        contexts.enter_context(patch.multiple(updater, ROOT=root, STATE=state))
        self.previous = root / OLD
        self.previous.mkdir()
        (self.previous / updater.BINARY).write_bytes(b"old")
        (root / "current").symlink_to(self.previous)
        self.info = {"binarySha256": hashlib.sha256(b"new").hexdigest()}
        self.command = contexts.enter_context(patch.object(
            updater, "command", return_value=SimpleNamespace(returncode=0, stdout="-state-dir /private")
        ))

    def stage(self, _release, _ref, _info, _arch, _revision, work):
        binary = work / updater.BINARY
        binary.write_bytes(b"new")
        return binary

    def test_release_order_and_manifest(self):
        def release(tag, *, manifest=True, draft=False):
            return {
                "tag_name": tag, "prerelease": "-rc." in tag, "draft": draft,
                "assets": [{"name": updater.MANIFEST, "id": 123}] if manifest else [],
            }

        releases = [
            release("v99.1.0"), release("server-v0.2.0", draft=True),
            release("server-v0.1.1", manifest=False),
            release("server-v0.1.0-rc.10"), release(NEW), release("server-v0.1.0"),
        ]
        self.assertEqual(updater.newest_release(releases)["tag_name"], "server-v0.1.0")
        self.assertEqual(updater.newest_release(releases[:-1])["tag_name"], "server-v0.1.0-rc.10")
        selected = updater.newest_release(releases)
        self.assertEqual(updater.asset_url(selected, updater.MANIFEST), f"{updater.API}/releases/assets/123")
        with self.assertRaisesRegex(RuntimeError, "Release asset is unavailable"):
            updater.asset_url(selected, "missing.tar.gz")
        info = {
            "archive": "metacubexd-server-image-linux-amd64.tar.gz",
            "archiveSha256": DIGEST, **self.info,
        }
        manifest = {"schema": 1, "tag": NEW, "sha": "b" * 40, "image": REF, "platforms": {"amd64": info}}
        self.assertEqual(updater.image_manifest(NEW, manifest, "amd64"), (REF, info))
        manifest["image"] = f"ghcr.io/other/image@sha256:{DIGEST}"
        with self.assertRaisesRegex(RuntimeError, "Invalid server image manifest"):
            updater.image_manifest(NEW, manifest, "amd64")

    def test_install_and_same_binary_promotion(self):
        with patch.object(updater, "extract", side_effect=self.stage), patch.object(updater, "panel_ready") as ready:
            updater.apply({"tag_name": NEW}, self.previous, REF, self.info, "amd64", "b" * 40)
            ready.assert_called_once_with(True)
        target = updater.installed_directory()
        self.assertEqual(target.name, NEW + "-image")
        self.assertEqual(target.stat().st_mode & 0o777, 0o755)
        self.assertEqual(updater.load("installed.json")["tag"], NEW)
        self.assertFalse(updater.load("pending.json"))

        self.command.reset_mock()
        updater.apply({"tag_name": "server-v0.1.0"}, target, REF, self.info, "amd64", "b" * 40)
        self.command.assert_not_called()
        self.assertEqual(updater.installed_directory(), target)
        self.assertEqual(updater.load("installed.json")["tag"], "server-v0.1.0")
        tag_file = target / "release-tag"
        self.assertEqual(tag_file.read_text(), "server-v0.1.0\n")
        self.assertEqual(tag_file.stat().st_mode & 0o777, 0o644)

    def test_failed_health_restores_original_link(self):
        with patch.object(updater, "extract", side_effect=self.stage), patch.object(
            updater, "panel_ready", side_effect=[RuntimeError("candidate unavailable"), None]
        ) as ready:
            with self.assertRaisesRegex(RuntimeError, "candidate unavailable"):
                updater.apply({"tag_name": NEW}, self.previous, REF, self.info, "amd64", "b" * 40)
        self.assertEqual(ready.call_count, 2)
        self.assertEqual(updater.installed_directory(), self.previous)
        self.assertEqual(updater.load("failed.json"), {"tag": NEW, "image": REF})
        self.assertFalse(updater.load("pending.json"))

    def test_mode_override_takes_precedence_over_legacy_args(self):
        self.command.return_value.stdout = "OTHER=value MCXD_WRITE_MODE=writable"
        self.assertTrue(updater.configured_writable())
        self.command.return_value.stdout = "MCXD_WRITE_MODE=read-only"
        self.assertFalse(updater.configured_writable())
        self.command.return_value.stdout = "argv[]=/panel -state-dir=/private"
        self.assertTrue(updater.configured_writable())

    def test_container_config_requires_a_writable_directory_bind(self):
        host = self.previous
        mounts = [{"Type": "bind", "Source": str(host), "Destination": "/etc/mihomo", "RW": True}]
        self.assertEqual(
            updater.docker_host_path(mounts, "/etc/mihomo/a/config.yaml", writable=True),
            host / "a" / "config.yaml",
        )
        mounts.append({"Type": "bind", "Source": str(host / updater.BINARY),
                       "Destination": "/etc/mihomo/a/config.yaml", "RW": True})
        with self.assertRaisesRegex(RuntimeError, "directory bind mount"):
            updater.docker_host_path(mounts, "/etc/mihomo/a/config.yaml", writable=True)
        mounts[0]["RW"] = False
        with self.assertRaisesRegex(RuntimeError, "writable"):
            updater.docker_host_path(mounts, "/etc/mihomo/config.yaml", writable=True)

    def test_docker_mode_uses_inspected_image_and_entrypoint(self):
        config = self.previous / updater.BINARY
        inspect = [{
            "State": {"Running": True},
            "Image": "sha256:" + DIGEST,
            "Path": "/usr/local/bin/mihomo",
            "Mounts": [{"Type": "bind", "Source": str(self.previous), "Destination": "/etc/mihomo", "RW": True}],
        }]
        profile = {
            "container": "mihomo", "config": "/etc/mihomo/" + updater.BINARY,
            "kernel_home": "/etc/mihomo", "probe_file": str(config), "docker_entrypoint": "",
        }

        def run(*args, **kwargs):
            output = json.dumps(inspect) if args[0] == "/usr/bin/docker" else "metacubexd"
            return SimpleNamespace(stdout=output, returncode=0)

        self.command.side_effect = run
        with patch.object(updater.shutil, "which", return_value="/usr/bin/docker"):
            actual, user, panel, validator = updater.mode_settings(profile)
        self.assertEqual((actual, user), (config, "metacubexd"))
        self.assertIn(f"MCXD_BASE_CONFIG={config}".encode(), panel)
        self.assertIn(b'"-docker-entrypoint" "/usr/local/bin/mihomo"', validator)
        self.assertIn(("sha256:" + DIGEST).encode(), validator)


if __name__ == "__main__":
    unittest.main()
