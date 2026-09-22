#!/usr/bin/env python3
"""Assemble a release bundle and archive it.

The bundle is the self-contained Go daemon (sing-box and xray are embedded
libraries, so there is no cores/ directory and nothing to fetch) plus the
per-OS install docs/script and the license texts. Produces
`<bundle-name>.{tar.gz,zip}` plus a `.sha256`.

The GUI: on Windows it ships as the Inno Setup installer (daemon + Flutter
client); on Linux pass `--flutter-dir` with the built Flutter bundle and it
lands in the archive as `gui/`, together with the systemd unit, the desktop
entry, and the icon — everything a binary package needs. On macOS pass
`--app-dir` with the built `iron-link.app`; it lands next to the daemon
together with the launchd unit template, and install.sh wires both up.

  python3 packaging/assemble.py --target linux-amd64 --version 0.1.0 \
      --daemon-bin daemon-go/bin/iron-link-daemon \
      --flutter-dir client-flutter/build/linux/x64/release/bundle \
      --staging dist --archive tar.gz

Pure stdlib; runs unchanged on every CI runner.
"""

import argparse
import hashlib
import os
import shutil
import sys
import tarfile
import zipfile

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)


def copy_in(bundle, src, rename=None):
    dst = os.path.join(bundle, rename or os.path.basename(src))
    shutil.copy2(src, dst)
    return dst


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--target", required=True)
    ap.add_argument("--version", required=True)
    ap.add_argument("--daemon-bin", required=True, help="built Go daemon binary")
    ap.add_argument("--flutter-dir", help="built Flutter bundle dir (Linux)")
    ap.add_argument("--app-dir", help="built iron-link.app bundle (macOS)")
    ap.add_argument("--staging", required=True, help="output dir")
    ap.add_argument("--archive", required=True, choices=["tar.gz", "zip"])
    args = ap.parse_args()

    is_windows = args.target.startswith("windows")
    exe = ".exe" if is_windows else ""
    bundle_name = f"iron-link-{args.version}-{args.target}"
    bundle = os.path.join(args.staging, bundle_name)
    os.makedirs(bundle, exist_ok=True)

    # The Go daemon (embeds the cores; no external sing-box/xray binaries).
    if not os.path.exists(args.daemon_bin):
        sys.exit(f"missing built daemon: {args.daemon_bin}")
    copy_in(bundle, args.daemon_bin, "iron-link-daemon" + exe)
    print(f"  + iron-link-daemon{exe}")

    # The Flutter GUI bundle (Linux): the exe + its lib/ and data/ trees.
    if args.flutter_dir:
        if not os.path.isdir(args.flutter_dir):
            sys.exit(f"missing Flutter bundle dir: {args.flutter_dir}")
        shutil.copytree(args.flutter_dir, os.path.join(bundle, "gui"))
        print("  + gui/")

    # The macOS app bundle. symlinks=True keeps the framework layout
    # (Versions/Current → A) that the ad-hoc code signature covers; a
    # dereferenced copy would fail signature validation on launch.
    if args.app_dir:
        if not os.path.isdir(args.app_dir):
            sys.exit(f"missing app bundle: {args.app_dir}")
        shutil.copytree(
            args.app_dir, os.path.join(bundle, "iron-link.app"), symlinks=True
        )
        print("  + iron-link.app")

    # The launchd unit template; install.sh fills in the user and config dir.
    if args.target.startswith("darwin"):
        os.makedirs(os.path.join(bundle, "launchd"), exist_ok=True)
        shutil.copy2(
            os.path.join(HERE, "launchd", "org.iron-link.daemon.plist"),
            os.path.join(bundle, "launchd", "org.iron-link.daemon.plist"),
        )
        print("  + launchd/org.iron-link.daemon.plist")

    # Linux integration files, so a binary package (or a careful human) can
    # install straight from this archive: systemd unit, desktop entry, icon.
    if args.target.startswith("linux"):
        os.makedirs(os.path.join(bundle, "systemd"), exist_ok=True)
        shutil.copy2(
            os.path.join(HERE, "systemd", "iron-link.service"),
            os.path.join(bundle, "systemd", "iron-link.service"),
        )
        copy_in(bundle, os.path.join(HERE, "arch", "iron-link.desktop"))
        copy_in(
            bundle,
            os.path.join(ROOT, "client-flutter", "assets", "logo-emblem.png"),
            "iron-link.png",
        )
        print("  + systemd/iron-link.service, iron-link.desktop, iron-link.png")

    # License texts — the daemon embeds GPL/MPL cores, so every binary
    # distribution carries them.
    copy_in(bundle, os.path.join(ROOT, "LICENSE"))
    copy_in(bundle, os.path.join(ROOT, "THIRD_PARTY_LICENSES"))

    # Bundle docs + installer for this OS.
    copy_in(bundle, os.path.join(HERE, "bundle", "INSTALL.md"))
    if is_windows:
        copy_in(bundle, os.path.join(HERE, "bundle", "install.ps1"))
    else:
        os.chmod(copy_in(bundle, os.path.join(HERE, "bundle", "install.sh")), 0o755)

    # Archive.
    out = os.path.join(args.staging, f"{bundle_name}.{args.archive}")
    if args.archive == "tar.gz":
        with tarfile.open(out, "w:gz") as tf:
            tf.add(bundle, arcname=bundle_name)
    else:
        with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED) as zf:
            for root, _, files in os.walk(bundle):
                for f in files:
                    full = os.path.join(root, f)
                    zf.write(full, os.path.join(bundle_name, os.path.relpath(full, bundle)))

    digest = hashlib.sha256(open(out, "rb").read()).hexdigest()
    with open(out + ".sha256", "w") as f:
        f.write(f"{digest}  {os.path.basename(out)}\n")
    print(f"built {out}")
    print(f"  sha256 {digest}")


if __name__ == "__main__":
    main()
