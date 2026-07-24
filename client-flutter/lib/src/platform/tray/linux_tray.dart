/// Linux tray backend: serves the menu model over the StatusNotifierItem +
/// com.canonical.dbusmenu DBus protocols — no libayatana-appindicator (see
/// ROADMAP). The window-close is trapped only once a tray host accepts us, so
/// a desktop with no SNI host (stock GNOME) never strands the user.
library;

import 'dart:async';
import 'dart:io';

import 'package:dbus/dbus.dart';
import 'package:flutter/foundation.dart';

import '../../ui/theme/status_palette.dart';
import 'dbus_menu.dart';
import 'status_notifier_item.dart';
import 'tray_controller.dart';
import 'tray_icon.dart';
import 'tray_menu_item.dart';

class LinuxTray extends TrayController {
  LinuxTray({required super.client, required super.session});

  static const _watcherName = 'org.kde.StatusNotifierWatcher';
  static const _watcherPath = '/StatusNotifierWatcher';

  // Status-dot colors (shared with the in-app indicators via StatusPalette):
  // grey idle, amber in-flight, green up.
  static const _dotOff = StatusPalette.off;
  static const _dotConnecting = StatusPalette.connecting;
  static const _dotConnected = StatusPalette.connected;

  DBusClient? _bus;
  StatusNotifierItem? _sni;
  TrayMenu? _menu;
  TrayIconRenderer? _icons;
  final _pixmaps = <TrayState, DBusArray>{};
  StreamSubscription<DBusNameOwnerChangedEvent>? _nameWatch;

  // Eager (no await dependency) so teardown can never read it unassigned.
  final String _busName = 'org.kde.StatusNotifierItem-$pid-1';
  bool _owned = false;

  @override
  Future<void> platformStart(List<TrayMenuItem> menu, TrayState state) async {
    _icons = await TrayIconRenderer.load();
    _pixmaps[TrayState.off] = _pixmapOf(await _icons!.render(_dotOff));
    _pixmaps[TrayState.connecting] =
        _pixmapOf(await _icons!.render(_dotConnecting));
    _pixmaps[TrayState.connected] =
        _pixmapOf(await _icons!.render(_dotConnected));

    final bus = DBusClient.session();
    _bus = bus;
    final reply = await bus.requestName(_busName);
    if (reply != DBusRequestNameReply.primaryOwner) {
      throw StateError('could not own $_busName ($reply)');
    }
    _owned = true;

    final m = TrayMenu(menu);
    _menu = m;
    _sni = StatusNotifierItem(
      menuPath: m.path,
      iconPixmap: _pixmaps[state]!,
      onActivate: toggleWindow,
    );
    await bus.registerObject(m);
    await bus.registerObject(_sni!);

    await _registerWithWatcher();
    _nameWatch = bus.nameOwnerChanged.listen((e) {
      if (e.name == _watcherName && (e.newOwner ?? '').isNotEmpty) {
        _registerWithWatcher();
      }
    });
  }

  @override
  Future<void> platformSetMenu(List<TrayMenuItem> menu) => _menu?.setItems(menu) ?? Future.value();

  @override
  Future<void> platformSetIcon(TrayState state) async {
    final pm = _pixmaps[state];
    if (pm != null) await _sni?.setIcon(pm);
  }

  @override
  Future<void> platformDispose() async {
    await _nameWatch?.cancel();
    _nameWatch = null;
    final bus = _bus;
    _bus = null;
    if (bus != null) {
      try {
        if (_sni != null) await bus.unregisterObject(_sni!);
        if (_menu != null) await bus.unregisterObject(_menu!);
        if (_owned) await bus.releaseName(_busName);
      } catch (_) {
        // Best-effort; the connection is about to close anyway.
      }
      await bus.close();
    }
    _icons?.dispose();
    _icons = null;
  }

  Future<void> _registerWithWatcher() async {
    final bus = _bus;
    if (bus == null) return;
    try {
      if (!await bus.nameHasOwner(_watcherName)) return; // no SNI host present
      final watcher = DBusRemoteObject(bus,
          name: _watcherName, path: DBusObjectPath(_watcherPath));
      await watcher.callMethod(_watcherName, 'RegisterStatusNotifierItem',
          [DBusString(_busName)],
          replySignature: DBusSignature(''));
      await enableCloseToTray(); // a host is showing us — safe to trap close
    } catch (e) {
      debugPrint('tray: watcher registration failed: $e');
    }
  }

  DBusArray _pixmapOf(List<TrayBitmap> bmps) => DBusArray(
      DBusSignature('(iiay)'),
      bmps
          .map((b) => DBusStruct([
                DBusInt32(b.size),
                DBusInt32(b.size),
                DBusArray.byte(_rgbaToArgb(b.rgba)),
              ]))
          .toList());

  // Straight RGBA → ARGB32 big-endian (bytes A,R,G,B per pixel), as SNI wants.
  static Uint8List _rgbaToArgb(Uint8List rgba) {
    final argb = Uint8List(rgba.length);
    for (var i = 0; i < rgba.length; i += 4) {
      argb[i] = rgba[i + 3]; // A
      argb[i + 1] = rgba[i]; // R
      argb[i + 2] = rgba[i + 1]; // G
      argb[i + 3] = rgba[i + 2]; // B
    }
    return argb;
  }
}
