#!/usr/bin/env python3
"""Apply a verified server release with the existing systemd service settings."""

import argparse
from contextlib import contextmanager
import fcntl
import hashlib
import http.cookiejar
import json
import os
from pathlib import Path
import platform
import re
import subprocess
import tempfile
import time
import urllib.request

REPO = "yhrv666/metacubexd"
API = f"https://api.github.com/repos/{REPO}"
REGISTRY = f"ghcr.io/{REPO}-server"
SERVICE = "metacubexd-server.service"
VALIDATOR = "metacubexd-validator.service"
ROOT = Path("/opt/metacubexd-server")
STATE = Path("/var/lib/metacubexd-image-update")
PANEL = "http://127.0.0.1:18443"
BINARY = "metacubexd-server"
MANIFEST = "server-images.json"
ASSET_MEDIA_TYPE = "application/octet-stream"
HEX = re.compile(r"[0-9a-f]{64}")
TAG = re.compile(r"server-v(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})(?:-rc\.([1-9][0-9]{0,8}))?")


def version(tag):
    match = TAG.fullmatch(tag)
    if not match:
        return ()
    major, minor, patch, rc = match.groups()
    return int(major), int(minor), int(patch), rc is None, int(rc or 0)


def request(url, accept="application/vnd.github+json"):
    req = urllib.request.Request(url, headers={"User-Agent": "metacubexd-image-update", "Accept": accept})
    return urllib.request.urlopen(req, timeout=25)


def read_json(url, accept="application/vnd.github+json"):
    with request(url, accept) as response:
        data = response.read((4 << 20) + 1)
    if len(data) > 4 << 20:
        raise RuntimeError("GitHub response exceeds its limit")
    return json.loads(data)


def asset_url(release, name):
    for asset in release["assets"]:
        if asset["name"] == name:
            asset_id = asset["id"]
            if type(asset_id) is int and asset_id > 0:
                return f"{API}/releases/assets/{asset_id}"
            break
    raise RuntimeError(f"Release asset is unavailable: {name}")


def newest_release(releases):
    return max((
        item for item in releases
        if version(item["tag_name"]) and not item["draft"]
        and item["prerelease"] == ("-rc." in item["tag_name"])
        and any(asset["name"] == MANIFEST for asset in item["assets"])
    ), key=lambda item: version(item["tag_name"]), default=None)


def image_manifest(tag, manifest, arch):
    ref = manifest["image"]
    info = manifest["platforms"][arch]
    if (manifest["schema"] != 1 or manifest["tag"] != tag
            or not re.fullmatch(r"[0-9a-f]{40}", manifest["sha"])
            or not ref.startswith(REGISTRY + "@sha256:")
            or not HEX.fullmatch(ref.removeprefix(REGISTRY + "@sha256:"))
            or info["archive"] != f"{BINARY}-image-linux-{arch}.tar.gz"
            or not HEX.fullmatch(info["archiveSha256"])
            or not HEX.fullmatch(info["binarySha256"])):
        raise RuntimeError("Invalid server image manifest")
    return ref, info


def command(*args, check=True, timeout=40):
    result = subprocess.run(args, capture_output=True, text=True, timeout=timeout)
    if check and result.returncode:
        raise RuntimeError(f"{' '.join(args[:2])} failed")
    return result


def sha256(path):
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        while block := stream.read(1 << 20):
            digest.update(block)
    return digest.hexdigest()


def archive_image(release, info, work):
    archive = work / info["archive"]
    with request(asset_url(release, archive.name), ASSET_MEDIA_TYPE) as response, archive.open("xb") as out:
        total = 0
        while block := response.read(1 << 20):
            total += len(block)
            if total > 64 << 20:
                raise RuntimeError("Image archive exceeds its limit")
            out.write(block)
    if sha256(archive) != info["archiveSha256"]:
        raise RuntimeError("Image archive checksum does not match")
    command("docker", "load", "--input", str(archive), timeout=120)
    return f"{REGISTRY}:{release['tag_name']}"


def extract(release, ref, info, arch, revision, work):
    image = ref
    try:
        pulled = command("docker", "pull", "--platform", f"linux/{arch}", ref, check=False, timeout=90).returncode == 0
    except subprocess.TimeoutExpired:
        pulled = False
    if not pulled:
        print("Using the checksummed GitHub Release image archive.", flush=True)
        image = archive_image(release, info, work)
    details = json.loads(command("docker", "image", "inspect", image).stdout)[0]
    labels = details["Config"]["Labels"] or {}
    if (details["Os"] != "linux" or details["Architecture"] != arch
            or labels.get("org.opencontainers.image.source") != f"https://github.com/{REPO}"
            or labels.get("org.opencontainers.image.revision") != revision
            or labels.get("org.opencontainers.image.version") != release["tag_name"]):
        raise RuntimeError("Image platform or release labels do not match")
    container = command("docker", "create", "--network", "none", details["Id"]).stdout.strip()
    binary = work / BINARY
    try:
        command("docker", "cp", f"{container}:/{BINARY}", str(binary))
    finally:
        command("docker", "rm", container)
    if not binary.is_file() or binary.is_symlink() or sha256(binary) != info["binarySha256"]:
        raise RuntimeError("Image executable checksum does not match")
    binary.chmod(0o755)
    return binary


def load(name):
    location = STATE / name
    return json.loads(location.read_bytes()) if location.exists() else {}


def save(name, value):
    pending = STATE / (name + ".tmp")
    with pending.open("w") as stream:
        json.dump(value, stream)
        stream.flush()
        os.fsync(stream.fileno())
    pending.replace(STATE / name)


@contextmanager
def update_lock():
    with (STATE / "update.lock").open("a") as stream:
        fcntl.flock(stream, fcntl.LOCK_EX | fcntl.LOCK_NB)
        yield


def installed_directory():
    path = (ROOT / "current").resolve(strict=True)
    if path.parent != ROOT or not (path / BINARY).is_file():
        raise RuntimeError("Current panel does not use the expected version directory")
    return path


def panel_ready(writable):
    password = Path("/etc/metacubexd-server/admin").read_text().strip()
    panel = urllib.request.build_opener(
        urllib.request.ProxyHandler({}),
        urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()),
    )

    def call(path, method="GET", payload=None):
        req = urllib.request.Request(
            PANEL + "/api/" + path, method=method,
            headers={"Origin": PANEL, "Content-Type": "application/json"},
            data=payload,
        )
        with panel.open(req, timeout=3) as response:
            return response.read(8 << 20)

    deadline = time.monotonic() + 25
    while time.monotonic() < deadline:
        try:
            call("session", "POST", json.dumps({"password": password}).encode())
            try:
                kernel = json.loads(call("monitor/version"))
                managed = json.loads(call("managed/state"))
                if kernel.get("version") and managed.get("writable") is writable:
                    return
            finally:
                call("session", "DELETE")
        except (OSError, ValueError):
            time.sleep(1)
    raise RuntimeError("Panel login, controller or managed mode readiness failed")


def switch(target, validator, writable):
    command("systemctl", "stop", SERVICE)
    pending = ROOT / ".image-update-current"
    pending.unlink(missing_ok=True)
    pending.symlink_to(target)
    pending.replace(ROOT / "current")
    if validator:
        command("systemctl", "restart", VALIDATOR)
    command("systemctl", "start", SERVICE)
    panel_ready(writable)


def recover():
    journal = load("pending.json")
    if not journal:
        return
    receipt = load("installed.json")
    if receipt.get("tag") != journal["tag"] or installed_directory().name != journal["target"]:
        previous = ROOT / journal["previous"]
        if previous.resolve(strict=True).parent != ROOT or not (previous / BINARY).is_file():
            raise RuntimeError("Previous panel directory is unavailable")
        switch(previous, journal["validator"], journal["writable"])
        save("failed.json", {"tag": journal["tag"], "image": journal["image"]})
        print(f"Restored {previous.name}.", flush=True)
    (STATE / "pending.json").unlink()


def apply(release, current, ref, info, arch, revision):
    tag = release["tag_name"]
    receipt = {"tag": tag, "image": ref, "target": current.name}
    if sha256(current / BINARY) == info["binarySha256"]:
        save("installed.json", receipt)
        print(f"{tag} matches the installed panel executable.", flush=True)
        return
    target = ROOT / (tag + "-image")
    if target.exists():
        if target.is_symlink() or sha256(target / BINARY) != info["binarySha256"]:
            raise RuntimeError("Release directory conflicts with an existing installation")
    else:
        with tempfile.TemporaryDirectory(prefix=".image-update-", dir=ROOT) as temp:
            work = Path(temp)
            binary = extract(release, ref, info, arch, revision, work)
            stage = work / "version"
            stage.mkdir()
            stage.chmod(0o755)
            binary.replace(stage / BINARY)
            stage.rename(target)
    validator = command("systemctl", "is-active", "--quiet", VALIDATOR, check=False).returncode == 0
    options = command("systemctl", "show", "-p", "ExecStart", "--value", SERVICE).stdout
    writable = "-state-dir" in options
    save("pending.json", {
        "tag": tag, "image": ref, "previous": current.name, "target": target.name,
        "validator": validator, "writable": writable,
    })
    try:
        switch(target, validator, writable)
        receipt["target"] = target.name
        save("installed.json", receipt)
    except (OSError, RuntimeError, subprocess.TimeoutExpired):
        recover()
        raise
    (STATE / "pending.json").unlink()
    print(f"Verified panel login and controller on {tag}.", flush=True)


def update(check):
    if not check:
        recover()
        command("systemctl", "is-active", "--quiet", SERVICE)
    current = installed_directory()
    installed = load("installed.json")
    installed_tag = installed.get("tag", "") if installed.get("target") == current.name else current.name.removesuffix("-image")
    release = newest_release(read_json(f"{API}/releases?per_page=100"))
    if release is None or version(release["tag_name"]) <= version(installed_tag):
        print("No newer server release with a published image.")
        return
    arch = {"x86_64": "amd64", "aarch64": "arm64"}[platform.machine()]
    tag = release["tag_name"]
    manifest = read_json(asset_url(release, MANIFEST), ASSET_MEDIA_TYPE)
    ref, info = image_manifest(tag, manifest, arch)
    if check:
        print(f"Available {tag}: {ref}")
    elif load("failed.json") != {"tag": tag, "image": ref}:
        apply(release, current, ref, info, arch, manifest["sha"])
    else:
        print(f"{tag} was rolled back. Waiting for a newer release.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true", help="Inspect the published release without changing the panel")
    parser.add_argument("--recover", action="store_true", help="Recover an interrupted deployment")
    args = parser.parse_args()
    if args.check:
        update(check=True)
    else:
        with update_lock():
            if args.recover:
                recover()
            else:
                update(check=False)


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, KeyError, RuntimeError, subprocess.TimeoutExpired) as error:
        raise SystemExit(f"MetaCubeXD image update failed: {error}") from None
