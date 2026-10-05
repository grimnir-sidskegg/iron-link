#!/bin/sh
# iron-link installer (Linux). The daemon embeds its cores — there is no
# cores/ dir. `./install.sh --uninstall` reverses it.
#
# Copies the daemon onto PATH and grants it cap_net_admin. Service
# registration is not wired up here — the bundle's gui/ runs in place, or use
# the binary Arch recipe (packaging/arch/PKGBUILD-bin) for a full install.
set -eu

HERE=$(cd "$(dirname "$0")" && pwd)
BIN_DIR="${BIN_DIR:-$HOME/.local/bin}"

install_() {
    mkdir -p "$BIN_DIR"
    echo "→ daemon → $BIN_DIR"
    install -m 0755 "$HERE/iron-link-daemon" "$BIN_DIR/iron-link-daemon"
    if command -v setcap >/dev/null 2>&1; then
        echo "→ granting cap_net_admin to the daemon (sudo)…"
        sudo setcap cap_net_admin,cap_net_raw=+ep "$BIN_DIR/iron-link-daemon" \
            || echo "  setcap failed — TUN sessions will need sudo"
    fi
    echo
    echo "done. Start the daemon:  sudo $BIN_DIR/iron-link-daemon   (sudo for TUN)"
    echo "(make sure $BIN_DIR is on your PATH)"
}

uninstall() {
    rm -f "$BIN_DIR/iron-link-daemon"
    echo "removed $BIN_DIR/iron-link-daemon. Config kept: ${XDG_CONFIG_HOME:-$HOME/.config}/iron-link"
}

if [ "${1:-}" = "--uninstall" ]; then uninstall; else install_; fi
