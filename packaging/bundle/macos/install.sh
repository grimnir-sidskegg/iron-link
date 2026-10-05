#!/bin/sh
# iron-link installer (macOS). The daemon embeds its cores — there is no
# cores/ dir. `./install.sh --uninstall` reverses it.
#
# Installs the daemon to /usr/local/bin, the app to /Applications, and
# registers the daemon as a launchd LaunchDaemon (root, auto-start; the control
# socket is handed to the installing user). Bundles are not notarized, so the
# quarantine flag a browser download carries is removed first — see INSTALL.md.
set -eu

HERE=$(cd "$(dirname "$0")" && pwd)

# macOS paths. The daemon runs as root (a TUN needs it) and serves ONE user:
# whoever runs this script (or invoked sudo for it). Its control socket lands
# in that user's config dir, owned by them.
DAEMON_DST=/usr/local/bin/iron-link-daemon
APP_DST=/Applications/iron-link.app
LABEL=org.iron-link.daemon
PLIST_DST=/Library/LaunchDaemons/$LABEL.plist

darwin_user() {
    U=${SUDO_USER:-$(id -un)}
    U_UID=$(id -u "$U")
    U_GID=$(id -g "$U")
    U_HOME=$(dscl . -read "/Users/$U" NFSHomeDirectory 2>/dev/null | awk '{print $2}')
    [ -n "$U_HOME" ] || U_HOME=$(eval echo "~$U")
    CONFIG_DIR="$U_HOME/Library/Application Support/iron-link"
}

darwin_install() {
    darwin_user
    # A browser download is quarantined; Gatekeeper then refuses the
    # (unsigned) app and stalls the daemon's first exec behind a dialog.
    # Dropping the flag is what "Open Anyway" would do, for the whole bundle.
    xattr -dr com.apple.quarantine "$HERE" 2>/dev/null || true

    echo "→ daemon → $DAEMON_DST (sudo)"
    sudo install -d /usr/local/bin
    sudo install -m 0755 "$HERE/iron-link-daemon" "$DAEMON_DST"

    if [ -d "$HERE/iron-link.app" ]; then
        echo "→ app → $APP_DST (sudo)"
        sudo rm -rf "$APP_DST"
        sudo ditto "$HERE/iron-link.app" "$APP_DST"
    fi

    PLIST_SRC="$HERE/launchd/$LABEL.plist"
    [ -f "$PLIST_SRC" ] || PLIST_SRC="$HERE/../../launchd/$LABEL.plist"
    echo "→ launchd $LABEL (root daemon, serves $U)"
    sudo launchctl bootout "system/$LABEL" 2>/dev/null || true
    sed -e "s#__CONFIG_DIR__#$CONFIG_DIR#g" \
        -e "s#__UID__#$U_UID#g" \
        -e "s#__GID__#$U_GID#g" "$PLIST_SRC" | sudo tee "$PLIST_DST" >/dev/null
    sudo chown root:wheel "$PLIST_DST"
    sudo chmod 0644 "$PLIST_DST"
    sudo launchctl bootstrap system "$PLIST_DST"

    i=0
    while [ $i -lt 20 ] && [ ! -S "$CONFIG_DIR/iron-link.sock" ]; do
        sleep 0.5; i=$((i + 1))
    done
    if [ -S "$CONFIG_DIR/iron-link.sock" ]; then
        echo "daemon is up: $CONFIG_DIR/iron-link.sock"
    else
        echo "daemon did not come up — see $CONFIG_DIR/daemon.log" >&2
        exit 1
    fi
    echo
    echo "done. Launch iron-link from /Applications (or: open -a iron-link)."
    echo "The daemon is listed under System Settings → General → Login Items & Extensions."
}

darwin_uninstall() {
    darwin_user
    sudo launchctl bootout "system/$LABEL" 2>/dev/null || true
    sudo rm -f "$PLIST_DST" "$DAEMON_DST"
    sudo rm -rf "$APP_DST"
    echo "removed the daemon, its launchd unit and $APP_DST. Config kept: $CONFIG_DIR"
}

if [ "${1:-}" = "--uninstall" ]; then darwin_uninstall; else darwin_install; fi
