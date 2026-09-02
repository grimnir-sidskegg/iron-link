#!/bin/sh
# iron-link installer (Linux / macOS). Copies the self-contained daemon onto
# PATH. The daemon embeds its cores — there is no cores/ dir. Service
# registration is not wired up here; start the daemon manually. This script
# installs the DAEMON only — the Linux bundle's gui/ runs in place, or use
# the binary Arch recipe (packaging/arch/PKGBUILD-bin) for a full install.
set -eu

HERE=$(cd "$(dirname "$0")" && pwd)
OS=$(uname -s)
case "$OS" in
    Linux)
        BIN_DIR="${BIN_DIR:-$HOME/.local/bin}"
        ;;
    Darwin)
        BIN_DIR="${BIN_DIR:-/usr/local/bin}"
        ;;
    *)
        echo "unsupported OS: $OS" >&2
        exit 1
        ;;
esac

mkdir -p "$BIN_DIR"

echo "→ daemon → $BIN_DIR"
install -m 0755 "$HERE/iron-link-daemon" "$BIN_DIR/iron-link-daemon"

if [ "$OS" = "Linux" ]; then
    if command -v setcap >/dev/null 2>&1; then
        echo "→ granting cap_net_admin to the daemon (sudo)…"
        sudo setcap cap_net_admin,cap_net_raw=+ep "$BIN_DIR/iron-link-daemon" \
            || echo "  setcap failed — TUN sessions will need sudo"
    fi
fi

echo
echo "done. Start the daemon:  sudo $BIN_DIR/iron-link-daemon   (sudo for TUN)"
echo "(make sure $BIN_DIR is on your PATH)"
