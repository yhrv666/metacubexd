from contextlib import ExitStack
import hashlib
import importlib.util
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


if __name__ == "__main__":
    unittest.main()
