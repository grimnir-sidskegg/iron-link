/// Windows tray backend: drives a hand-rolled Win32 tray icon + popup menu in
/// the app's C++ runner over a method channel (no plugin — see ROADMAP). The
/// shared [TrayController] builds the menu/state; this serializes the menu and
/// the icon pixels to native and routes native clicks back into the actions.
library;

import 'package:flutter/services.dart';

import '../../ui/theme/status_palette.dart';
import 'tray_controller.dart';
import 'tray_icon.dart';
import 'tray_menu_item.dart';

class WindowsTray extends TrayController {
  WindowsTray({required super.client, required super.session});

  // Must match the channel name in windows/runner/flutter_window.cpp.
  static const _channel = MethodChannel('iron_link/tray');

  // Status-dot colors shared with the in-app indicators via StatusPalette.
  static const _dotOff = StatusPalette.off;
  static const _dotConnecting = StatusPalette.connecting;
  static const _dotConnected = StatusPalette.connected;

  TrayIconRenderer? _icons;
  final _byId = <int, TrayMenuItem>{}; // current menu, for click routing

  @override
  Future<void> platformStart(List<TrayMenuItem> menu, TrayState state) async {
    _channel.setMethodCallHandler(_onNative);
    _icons = await TrayIconRenderer.load();
    await platformSetIcon(state); // sets the HICON before the icon is added
    await platformSetMenu(menu);
    await _channel.invokeMethod('show');
    await enableCloseToTray(); // Windows always has a tray area
  }

  Future<dynamic> _onNative(MethodCall call) async {
    switch (call.method) {
      case 'onTrayClick':
        await toggleWindow();
      case 'onShowRequested':
        // A second launch was suppressed (single-instance); surface this one.
        // The runner already brought the window up natively — this keeps
        // window_manager's state in sync and runs its show/focus path.
        await showWindow();
      case 'onMenuItem':
        final id = (call.arguments as Map)['id'] as int;
        final it = _byId[id];
        if (it != null && it.isEnabled && !it.isSubmenu) it.onClick?.call();
    }
    return null;
  }

  @override
  Future<void> showWindow() async {
    await super.showWindow();
    // window_manager.show() is a bare SW_SHOW with no resize, so the Flutter
    // engine is never asked to repaint when the window returns from the tray or
    // a menu "Show". A surface left blank/stale while hidden would otherwise
    // stay white until the process restarts — force a fresh present natively.
    try {
      await _channel.invokeMethod('repaint');
    } catch (_) {
      // Engine tearing down or the channel isn't up yet — nothing to recover.
    }
  }

  @override
  Future<void> platformSetMenu(List<TrayMenuItem> menu) async {
    _byId.clear();
    _index(menu);
    await _channel.invokeMethod('setMenu', {'items': _serialize(menu)});
  }

  void _index(List<TrayMenuItem> items) {
    for (final it in items) {
      _byId[it.id] = it;
      if (it.isSubmenu) _index(it.children);
    }
  }

  List<Object?> _serialize(List<TrayMenuItem> items) => [
        for (final it in items)
          if (it.isSeparator)
            {'type': 'separator'}
          else
            {
              'id': it.id,
              'label': it.displayLabel,
              'enabled': it.isEnabled,
              if (it.isSubmenu) 'submenu': _serialize(it.children),
            },
      ];

  @override
  Future<void> platformSetIcon(TrayState state) async {
    final icons = _icons;
    if (icons == null) return;
    final color = switch (state) {
      TrayState.off => _dotOff,
      TrayState.connecting => _dotConnecting,
      TrayState.connected => _dotConnected,
    };
    final bmps = await icons.render(color);
    final b = bmps.last; // largest size; Windows scales to the tray slot
    await _channel.invokeMethod('setIcon', {
      'pixels': _rgbaToBgra(b.rgba),
      'width': b.size,
      'height': b.size,
    });
  }

  // Straight RGBA → straight BGRA (swap R↔B), top-down — what the Win32 HICON
  // path (CreateDIBSection + CreateIconIndirect) expects.
  static Uint8List _rgbaToBgra(Uint8List rgba) {
    final bgra = Uint8List(rgba.length);
    for (var i = 0; i < rgba.length; i += 4) {
      bgra[i] = rgba[i + 2]; // B
      bgra[i + 1] = rgba[i + 1]; // G
      bgra[i + 2] = rgba[i]; // R
      bgra[i + 3] = rgba[i + 3]; // A
    }
    return bgra;
  }

  @override
  Future<void> platformDispose() async {
    try {
      await _channel.invokeMethod('hide');
    } catch (_) {
      // The engine may already be tearing down; nothing to clean up Dart-side.
    }
    _channel.setMethodCallHandler(null);
    _icons?.dispose();
    _icons = null;
  }
}
