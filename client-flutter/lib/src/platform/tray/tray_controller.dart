/// Platform-agnostic tray controller: owns the menu model, the daemon-session
/// wiring, the connect/disconnect/select-node/show/quit actions and the
/// hide-to-tray window hook. A platform subclass renders the icon + menu and
/// routes clicks back into these shared actions:
///   - [LinuxTray] serves the model over the StatusNotifierItem DBus protocol,
///   - [WindowsTray] serializes it across a method channel to a native menu.
library;

import 'dart:async';

import 'package:flutter/foundation.dart';
import 'package:window_manager/window_manager.dart';

import '../../app/session.dart';
import '../../ipc/client.dart';
import '../../wire/wire.dart';
import 'tray_menu_item.dart';

/// The visual tray state, driving the icon's status dot.
enum TrayState { off, connecting, connected }

abstract class TrayController with WindowListener {
  TrayController({required this.client, required this.session});

  final DaemonClient client;
  final DaemonSession session;

  bool _busy = false; // a connect/disconnect/switch is in flight
  bool _windowHooked = false;
  TrayState _shown = TrayState.off;
  String? _lastActive; // last activeNodeLive, to repaint the node radio
  List<NodeInfo> _nodes = const [];
  int _lastStoreRev = -1;

  // ---- platform backend (subclass implements) -----------------------------

  /// Bring up the platform tray with [menu] and the [state] icon. Should not
  /// throw; on failure the controller stays inert. The subclass calls
  /// [enableCloseToTray] once a tray is actually shown.
  @protected
  Future<void> platformStart(List<TrayMenuItem> menu, TrayState state);

  /// Push a freshly-built menu to the platform tray.
  @protected
  Future<void> platformSetMenu(List<TrayMenuItem> menu);

  /// Swap the tray icon to reflect [state].
  @protected
  Future<void> platformSetIcon(TrayState state);

  /// Release the platform tray (icon, channel/connection).
  @protected
  Future<void> platformDispose();

  /// Trap the window-close into hide-to-tray. Idempotent; the subclass calls
  /// it only once a tray host is actually showing us, so a desktop with no
  /// tray never strands the user (close still quits there).
  @protected
  Future<void> enableCloseToTray() async {
    if (_windowHooked) return;
    _windowHooked = true;
    await windowManager.setPreventClose(true);
    windowManager.addListener(this);
  }

  // ---- lifecycle ----------------------------------------------------------

  Future<void> init() async {
    _shown = _state();
    _lastActive = session.activeNodeLive;
    try {
      await platformStart(buildMenu(), _shown);
    } catch (e) {
      debugPrint('tray: platform start failed, running without a tray: $e');
      await platformDispose();
      return;
    }
    _lastStoreRev = session.storeRev;
    session.addListener(_onSession);
    unawaited(_fetchNodes());
  }

  Future<void> dispose() async {
    session.removeListener(_onSession);
    if (_windowHooked) windowManager.removeListener(this);
    await platformDispose();
  }

  @override
  void onWindowClose() {
    // preventClose is on once a host shows us: hide to tray instead of quitting.
    windowManager.hide();
  }

  // ---- shared menu model --------------------------------------------------

  @protected
  List<TrayMenuItem> buildMenu() => [
        TrayMenuItem(
            id: 1,
            label: 'Connect',
            enabled: () => !_busy && !session.isRunning,
            onClick: _connect),
        TrayMenuItem(
            id: 2,
            label: 'Disconnect',
            enabled: () => !_busy && session.isRunning,
            onClick: _disconnect),
        TrayMenuItem(id: 3, isSeparator: true),
        TrayMenuItem(id: 6, label: 'Select Node', children: _buildNodeItems()),
        TrayMenuItem(id: 7, isSeparator: true),
        TrayMenuItem(id: 4, label: 'Show iron-link', onClick: showWindow),
        TrayMenuItem(id: 5, label: 'Quit', onClick: _quit),
      ];

  // Node-picker submenu: one row per node of the active profile, the current
  // one marked. Ids 100+. Empty / daemon unreachable → a disabled note.
  List<TrayMenuItem> _buildNodeItems() {
    if (_nodes.isEmpty) {
      return [TrayMenuItem(id: 99, label: '(no nodes)', enabled: () => false)];
    }
    var id = 100;
    return [
      // A text radio marker (●/○) in the label — visible on every host/theme
      // (some panels don't draw a native radio indicator). Live so it tracks
      // the current node each time the menu is drawn.
      for (final node in _nodes)
        TrayMenuItem(
          id: id++,
          labelFn: () => '${_isCurrentNode(node) ? '●' : '○'}  ${node.name}',
          enabled: () => !_busy,
          onClick: () => unawaited(_selectNode(node)),
        ),
    ];
  }

  // When running, the live node wins (a urltest auto-switch can differ from the
  // persisted default); otherwise the persisted default (NodeInfo.active).
  bool _isCurrentNode(NodeInfo node) {
    final live = session.activeNodeLive;
    if (session.isRunning && live != null) return node.name == live;
    return node.active;
  }

  TrayState _state() => _busy
      ? TrayState.connecting
      : (session.isRunning ? TrayState.connected : TrayState.off);

  // ---- state plumbing -----------------------------------------------------

  void _onSession() {
    // A store change (nodes added/removed, profile switched, subscription
    // refreshed) bumps storeRev — refetch the node list for the submenu.
    if (session.storeRev != _lastStoreRev) {
      _lastStoreRev = session.storeRev;
      unawaited(_fetchNodes());
    }
    unawaited(_refresh());
  }

  /// Repaint the icon and/or push the menu only when something the tray shows
  /// actually changed — NOT on every per-second traffic tick.
  Future<void> _refresh() async {
    final next = _state();
    final active = session.activeNodeLive;
    final stateChanged = next != _shown;
    final activeChanged = active != _lastActive;
    if (!stateChanged && !activeChanged) return;
    _shown = next;
    _lastActive = active;
    if (stateChanged) await platformSetIcon(next);
    await platformSetMenu(buildMenu());
  }

  Future<void> _fetchNodes() async {
    try {
      _nodes = await client.listNodes();
    } on ClientException catch (e) {
      debugPrint('tray: list nodes failed: $e');
      _nodes = const [];
    }
    await platformSetMenu(buildMenu());
  }

  // ---- actions (also the menu items' onClick) -----------------------------

  Future<void> _selectNode(NodeInfo node) async {
    if (_busy) return;
    final wasRunning = session.isRunning;
    if (wasRunning) {
      _busy = true;
      await _refresh();
    }
    try {
      // Mirror the UI: live-switch when up, then persist the profile default.
      if (wasRunning) await client.switchNode(node.name);
      await client.selectNode(node.id);
    } on ClientException catch (e) {
      debugPrint('tray: select node failed: $e');
    }
    if (wasRunning) _busy = false;
    await session.refreshStatus();
    await _refresh();
    // Refetch so the marker reflects the new persisted default (NodeInfo.active
    // changed daemon-side; selectNode emits no store event to trigger it).
    await _fetchNodes();
  }

  Future<void> _connect() async {
    if (_busy) return;
    _busy = true;
    await _refresh();
    try {
      // Match the UI's default (status_header starts with _tun = true): a tray
      // quick-connect with no prior session brings up the TUN, not proxy-only.
      await client.activate(tun: session.active?.tun ?? true);
    } on ClientException catch (e) {
      debugPrint('tray: connect failed: $e');
    }
    await session.refreshStatus();
    _busy = false;
    await _refresh();
  }

  Future<void> _disconnect() async {
    if (_busy) return;
    _busy = true;
    await _refresh();
    try {
      await client.stop();
    } on ClientException catch (e) {
      debugPrint('tray: disconnect failed: $e');
    }
    await session.refreshStatus();
    _busy = false;
    await _refresh();
  }

  @protected
  Future<void> showWindow() async {
    await windowManager.show();
    await windowManager.focus();
  }

  /// Left-click / Activate: toggle the window's visibility.
  @protected
  Future<void> toggleWindow() async {
    if (await windowManager.isVisible()) {
      await windowManager.hide();
    } else {
      await showWindow();
    }
  }

  Future<void> _quit() async {
    await dispose();
    await windowManager.setPreventClose(false);
    await windowManager.destroy();
  }
}
