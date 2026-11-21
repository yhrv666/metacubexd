#!/usr/bin/env python3
"""Apply a verified server release with the existing systemd service settings."""

import argparse
from collections import deque
from contextlib import contextmanager
import fcntl
import hashlib
import http.cookiejar
import json
import os
from pathlib import Path
import platform
import re
import shlex
import shutil
import subprocess
import tempfile
import time
import urllib.request

REPO = "yhrv666/metacubexd"
API = f"https://api.github.com/repos/{REPO}"
REGISTRY = f"ghcr.io/{REPO}-server"
SERVICE = "metacubexd-server.service"
UPDATE_SERVICE = "metacubexd-image-update.service"
VALIDATOR = "metacubexd-validator.service"
ROOT = Path("/opt/metacubexd-server")
STATE = Path("/var/lib/metacubexd-image-update")
PANEL = "http://127.0.0.1:18443"
BINARY = "metacubexd-server"
MANIFEST = "server-images.json"
MODE_DROPIN = Path(f"/etc/systemd/system/{SERVICE}.d/90-metacubexd-mode.conf")
VALIDATOR_DROPIN = Path(f"/etc/systemd/system/{VALIDATOR}.d/90-metacubexd-mode.conf")
MODE_PROFILE = Path("/etc/metacubexd-server/write-profile.json")
VALIDATOR_SOCKET = "/run/metacubexd-validator/validate.sock"
ASSET_MEDIA_TYPE = "application/octet-stream"
HEX = re.compile(r"[0-9a-f]{64}")
TAG = re.compile(r"server-v(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})(?:-rc\.([1-9][0-9]{0,8}))?")
NO_UPDATE = "No newer server release with a published image."


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


def panel_ready(writable=None, *, require_controller=True):
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
                controller_ready = not require_controller or json.loads(call("monitor/version")).get("version")
                managed = json.loads(call("managed/state"))
                actual = managed.get("writable")
                if controller_ready and isinstance(actual, bool) and (writable is None or actual is writable):
                    return actual
            finally:
                call("session", "DELETE")
        except (OSError, ValueError):
            pass
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


def configured_writable():
    environment = command("systemctl", "show", "-p", "Environment", "--value", SERVICE).stdout
    for item in reversed(shlex.split(environment)):
        if item.startswith("MCXD_WRITE_MODE="):
            return item == "MCXD_WRITE_MODE=writable"
    options = command("systemctl", "show", "-p", "ExecStart", "--value", SERVICE).stdout
    return bool(re.search(r"(?:^|\s)--?state-dir(?:=|\s)", options))


def docker_host_path(mounts, value, *, writable=False):
    path = Path(value)
    if not path.is_absolute() or ".." in path.parts:
        raise RuntimeError("Container config and kernel home must be absolute paths without '..'")
    matches = [mount for mount in mounts if path.is_relative_to(mount["Destination"])]
    if not matches:
        raise RuntimeError("No Docker mount covers the specified container path")
    mount = max(matches, key=lambda item: len(Path(item["Destination"]).parts))
    root = Path(mount["Source"])
    if mount["Type"] != "bind" or not root.is_dir() or (writable and not mount["RW"]):
        raise RuntimeError("Use a host directory bind mount, writable for the startup config")
    return root / path.relative_to(mount["Destination"])


def systemd_arg(value, *, exec_arg=False):
    value = str(value)
    if any(ord(char) < 32 or ord(char) == 127 for char in value):
        raise RuntimeError("Unit values must not contain control characters")
    value = value.replace("\\", "\\\\").replace('"', '\\"').replace("%", "%%")
    return '"' + (value.replace("$", "$$") if exec_arg else value) + '"'


def mode_settings(profile):
    config = Path(profile["config"])
    home = Path(profile["kernel_home"])
    probe = Path(profile["probe_file"])
    container = profile["container"]
    if container:
        docker = shutil.which("docker")
        if docker is None:
            raise RuntimeError("Docker CLI is required for the configured container")
        detail, = json.loads(command(docker, "container", "inspect", "--", container).stdout)
        if not detail["State"]["Running"] or not re.fullmatch(r"sha256:[a-f0-9]{64}", detail["Image"]):
            raise RuntimeError("Select a running container with an immutable image ID")
        config = docker_host_path(detail["Mounts"], config, writable=True)
        home = docker_host_path(detail["Mounts"], home)
        entrypoint = profile["docker_entrypoint"] or detail["Path"]
        if not Path(entrypoint).is_absolute():
            raise RuntimeError("Supply --docker-entrypoint with the absolute Mihomo executable inside the image")
        validator = ["-docker-binary", docker, "-docker-image", detail["Image"], "-docker-entrypoint", entrypoint]
    else:
        binary = Path(profile["kernel_binary"])
        if not binary.is_absolute() or not binary.is_file() or not os.access(binary, os.X_OK):
            raise RuntimeError("--kernel-binary must be an absolute path to an executable file")
        validator = ["-kernel-binary", binary]
    if not config.is_absolute() or config.is_symlink() or not config.is_file() or not home.is_absolute() or not home.is_dir():
        raise RuntimeError("Use an absolute startup file and kernel home directory on the host")
    if not probe.is_absolute() or not probe.is_file():
        raise RuntimeError("--probe-file must be an absolute path to the runtime probe JSON")
    config = config.resolve(strict=True)
    if config.parent == Path("/"):
        raise RuntimeError("Put the startup configuration in a dedicated directory")
    user = command("systemctl", "show", "-p", "User", "--value", SERVICE).stdout.strip() or "root"
    checks = [(config, "-r"), (probe, "-r"), (config.parent, "-w"), (config.parent, "-x")]
    state_dir = Path(profile.get("state_dir") or "/var/lib/metacubexd")
    state_unit = "StateDirectory=metacubexd\nStateDirectoryMode=0700\n"
    if state_dir != Path("/var/lib/metacubexd"):
        if (not state_dir.is_absolute() or state_dir.is_symlink() or not state_dir.is_dir()
                or state_dir.stat().st_mode & 0o077):
            raise RuntimeError("--state-dir must be an existing absolute private directory")
        state_dir = state_dir.resolve(strict=True)
        checks.extend(((state_dir, "-w"), (state_dir, "-x")))
        state_unit = f"ReadWritePaths={systemd_arg(state_dir)}\n"
    for path, access in checks:
        if command("runuser", "-u", user, "--", "test", access, str(path), check=False).returncode:
            raise RuntimeError("The panel service account needs to read the config and probes and write to the config and state directories")

    panel_unit = (
        f"[Unit]\nRequires={VALIDATOR}\nAfter={VALIDATOR}\n"
        '[Service]\nEnvironment="MCXD_WRITE_MODE=writable"\n'
        f"Environment={systemd_arg(f'MCXD_BASE_CONFIG={config}')}\n"
        f"Environment={systemd_arg(f'MCXD_PROBE_FILE={probe.resolve(strict=True)}')}\n"
        f"Environment={systemd_arg(f'MCXD_STATE_DIR={state_dir}')}\n"
        f"ReadWritePaths={systemd_arg(config.parent)}\n"
        + state_unit
    )
    args = [ROOT / "current" / BINARY, "-validator-service", "-validator-socket", VALIDATOR_SOCKET,
            "-kernel-home", home.resolve(strict=True), *validator]
    validator_unit = "[Service]\nEnvironmentFile=\nExecStart=\nExecStart=" + " ".join(
        systemd_arg(arg, exec_arg=True) for arg in args
    ) + "\n"
    return config, user, panel_unit.encode(), validator_unit.encode()


def replace_settings(path, contents):
    if contents is None:
        path.unlink(missing_ok=True)
        return
    path.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile(dir=path.parent, prefix=".metacubexd-", delete=False) as pending:
        temp = Path(pending.name)
        try:
            pending.write(contents)
            pending.flush()
            os.fsync(pending.fileno())
            temp.replace(path)
        finally:
            temp.unlink(missing_ok=True)


def set_mode(args):
    if args.mode == "status":
        if panel_ready(require_controller=False):
            print("writable")
        else:
            print("configured for writes, currently blocked" if configured_writable() else "read-only")
        return
    if os.geteuid() != 0:
        raise RuntimeError("Run mode changes as root")
    STATE.mkdir(mode=0o700, exist_ok=True)
    with update_lock():
        if load("pending.json"):
            raise RuntimeError("An interrupted image update is pending; complete --recover before switching modes")
        support = command(str(ROOT / "current" / BINARY), "-h")
        if "-docker-entrypoint" not in support.stderr:
            raise RuntimeError("Install a panel release that supports scripted mode changes first")
        previous_writable = panel_ready(require_controller=False)
        validator_active = command("systemctl", "is-active", "--quiet", VALIDATOR, check=False).returncode == 0
        writable = args.mode == "writable"
        changes = {MODE_DROPIN: b'[Service]\nEnvironment="MCXD_WRITE_MODE=read-only"\n'}
        if writable:
            if command("systemctl", "cat", VALIDATOR, check=False).returncode:
                raise RuntimeError(f"Install {VALIDATOR} under /etc/systemd/system/ before enabling writes")
            fields = ("config", "kernel_home", "probe_file", "kernel_binary", "container", "docker_entrypoint", "state_dir")
            profile = {field: getattr(args, field) for field in fields}
            if not any(profile.values()):
                if not MODE_PROFILE.is_file():
                    raise RuntimeError("Supply config, kernel home, probe file and a Mihomo executable or container on first use")
                profile = json.loads(MODE_PROFILE.read_bytes())
            config, user, panel_unit, validator_unit = mode_settings(profile)
            changes = {VALIDATOR_DROPIN: validator_unit, MODE_DROPIN: panel_unit,
                       MODE_PROFILE: json.dumps(profile).encode() + b"\n"}
        previous = {path: path.read_bytes() if path.exists() else None for path in changes}
        command("systemctl", "stop", SERVICE)
        try:
            for path, body in changes.items():
                replace_settings(path, body)
            command("systemctl", "daemon-reload")
            if writable:
                command("systemctl", "restart", VALIDATOR)
                deadline = time.monotonic() + 5
                while not Path(VALIDATOR_SOCKET).is_socket():
                    if time.monotonic() >= deadline:
                        raise RuntimeError("Mihomo validator did not open its socket")
                    time.sleep(0.1)
                result = command("runuser", "-u", user, "--", "curl", "--disable", "--noproxy", "*",
                                 "--fail", "--silent", "--retry", "3", "--retry-connrefused", "--max-time", "40",
                                 "--output", "/dev/null", "--unix-socket", VALIDATOR_SOCKET, "--data-binary",
                                 "@" + str(config), "http://localhost/validate", check=False, timeout=55)
                if result.returncode:
                    raise RuntimeError(f"Mihomo validator could not validate the startup file as the panel service account (curl {result.returncode})")
            command("systemctl", "start", SERVICE)
            panel_ready(writable, require_controller=writable)
            if not writable and validator_active:
                command("systemctl", "stop", VALIDATOR)
        except (OSError, RuntimeError, subprocess.TimeoutExpired, KeyboardInterrupt):
            command("systemctl", "stop", SERVICE)
            for path, body in previous.items():
                replace_settings(path, body)
            command("systemctl", "daemon-reload")
            command("systemctl", "reset-failed", SERVICE, VALIDATOR, check=False)
            command("systemctl", "restart" if validator_active else "stop", VALIDATOR)
            command("systemctl", "start", SERVICE)
            panel_ready(previous_writable, require_controller=False)
            raise
        print(args.mode)


def apply(release, current, ref, info, arch, revision):
    tag = release["tag_name"]
    receipt = {"tag": tag, "image": ref, "target": current.name}
    if sha256(current / BINARY) == info["binarySha256"]:
        with tempfile.TemporaryDirectory(prefix=".release-tag-", dir=current) as temp:
            pending_tag = Path(temp) / "release-tag"
            pending_tag.write_text(tag + "\n", encoding="ascii")
            pending_tag.chmod(0o644)
            pending_tag.replace(current / "release-tag")
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
    writable = configured_writable()
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
        print(NO_UPDATE)
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


def show_logs(all_logs):
    args = ["journalctl", "--unit", UPDATE_SERVICE, "--no-pager"]
    if all_logs:
        return subprocess.run([*args, "--lines", "50"]).returncode
    description = "Check for and apply a verified MetaCubeXD server image"
    routine = "|".join(map(re.escape, (
        NO_UPDATE,
        f"Starting {description}...",
        f"Finished {description}.",
        f"{UPDATE_SERVICE}: Deactivated successfully.",
    )))
    # Older journalctl versions apply --lines before --grep.
    args.append(f"--grep=^(?!(?:{routine})$).+")
    with subprocess.Popen(args, stdout=subprocess.PIPE, text=True, errors="replace") as journal:
        recent = deque(journal.stdout, maxlen=50)
        print("".join(recent), end="")
        return journal.wait()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    action = parser.add_mutually_exclusive_group()
    action.add_argument("--check", action="store_true", help="Inspect the published release without changing the panel")
    action.add_argument("--recover", action="store_true", help="Recover an interrupted deployment")
    action.add_argument("--logs", action="store_true", help="Show recent deployment updates and errors")
    action.add_argument("--all-logs", action="store_true", help="Show recent deployment logs including routine checks")
    action.add_argument("--mode", choices=("status", "read-only", "writable"), help="Inspect or switch the installed panel's write mode")
    parser.add_argument("--config", default="", help="Startup config path on the host, or inside --container")
    parser.add_argument("--kernel-home", default="", help="Kernel resource directory on the host, or inside --container")
    parser.add_argument("--probe-file", default="", help="Absolute host path to the runtime probe JSON")
    parser.add_argument("--state-dir", default="", help="Existing private state directory (default: /var/lib/metacubexd)")
    kernel = parser.add_mutually_exclusive_group()
    kernel.add_argument("--kernel-binary", default="", help="Absolute path to a native Mihomo executable")
    kernel.add_argument("--container", default="", help="Running Mihomo container name or ID")
    parser.add_argument("--docker-entrypoint", default="", help="Override the inspected absolute executable inside --container")
    args = parser.parse_args()
    paths = (args.config, args.kernel_home, args.probe_file, args.kernel_binary, args.container, args.docker_entrypoint, args.state_dir)
    if any(paths) and args.mode != "writable":
        parser.error("Mihomo settings require --mode writable")
    if any(paths) and (not all(paths[:3]) or not (args.kernel_binary or args.container)):
        parser.error("Supply config, kernel home, probe file and either a kernel binary or a running container")
    if args.docker_entrypoint and not args.container:
        parser.error("--docker-entrypoint requires --container")
    if args.mode:
        return set_mode(args)
    if args.logs or args.all_logs:
        return show_logs(args.all_logs)
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
        raise SystemExit(main())
    except (OSError, ValueError, KeyError, RuntimeError, subprocess.TimeoutExpired) as error:
        raise SystemExit(f"MetaCubeXD image update failed: {error}") from None
