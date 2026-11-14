"""Build the offline fixture with the reviewed SDK source revision."""

import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile


COMMAND = Path(__file__).resolve().parents[1]
SDK_REVISION = "d12065d63d9dd327b2e42f14ada155822f31702e"
SDK_VERSION = "1.0.0-rc.9"


def run(*args, cwd, env=None):
    subprocess.run(args, cwd=cwd, env=env, check=True, timeout=300)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--sdk-checkout", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True, help="New disposable directory")
    options = parser.parse_args()
    checkout = options.sdk_checkout.resolve(strict=True)
    output = options.output.resolve()
    output.mkdir(mode=0o700)
    home = output / "home"
    home.mkdir(mode=0o700)
    build_env = {key: os.environ[key] for key in ("PATH", "SystemRoot", "WINDIR") if key in os.environ}
    for key in ("HOME", "USERPROFILE", "TEMP", "TMP", "TMPDIR", "APPDATA", "LOCALAPPDATA"):
        build_env[key] = str(home)
    source = output / "sdk-source"
    source.mkdir()
    archive = output / "sdk-source.tar"
    run("git", "archive", "-o", str(archive), SDK_REVISION, cwd=checkout, env=build_env)
    with tarfile.open(archive) as package:
        members = (member for member in package if member.name != ".agents/skills/grill-me")
        package.extractall(source, members=members, filter="data")
    archive.unlink()

    manifest = json.loads((source / "package.json").read_text())
    if (manifest["name"], manifest["version"]) != ("@assignso/sdk", SDK_VERSION):
        raise ValueError("Unexpected SDK package identity")
    npm = shutil.which("npm.cmd" if os.name == "nt" else "npm")
    if npm is None:
        raise RuntimeError("npm is required to build the SDK fixture")
    npm_env = {**build_env, "npm_config_cache": str(output / "npm-cache"),
               "npm_config_registry": "https://registry.npmjs.org/"}
    run(npm, "ci", "--no-audit", "--no-fund", cwd=source, env=npm_env)
    run(npm, "pack", "--ignore-scripts", "--pack-destination", str(output), cwd=source, env=npm_env)
    packed = output / f"assignso-sdk-{SDK_VERSION}.tgz"

    stage = output / "candidate"
    stage.mkdir()
    for filename in ("package.json", "bun.lock"):
        shutil.copyfile(COMMAND / filename, stage / filename)
    for directory in ("src", "fixtures", "scripts"):
        shutil.copytree(COMMAND / directory, stage / directory)
    bun = shutil.which("bun")
    if bun is None:
        raise RuntimeError("Bun is required to build the CLI fixture")
    bun_env = {**build_env, "BUN_INSTALL_CACHE_DIR": str(output / "bun-cache")}
    run(bun, "install", "--frozen-lockfile", cwd=stage, env=bun_env)
    sdk_namespace = stage / "node_modules" / "@assignso"
    sdk_namespace.mkdir()
    with tarfile.open(packed) as package:
        package.extractall(sdk_namespace, filter="data")
    (sdk_namespace / "package").rename(sdk_namespace / "sdk")
    run(bun, "scripts/build.ts", "--sdk-fixture", cwd=stage, env=build_env)

    integrity = base64.b64encode(hashlib.sha512(packed.read_bytes()).digest()).decode("ascii")
    receipt = {"sdk_revision": SDK_REVISION, "sdk_version": SDK_VERSION, "integrity": f"sha512-{integrity}"}
    (output / "sdk-identity.json").write_text(json.dumps(receipt, indent=2) + "\n")


if __name__ == "__main__":
    main()
