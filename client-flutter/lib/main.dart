import 'dart:async';
import 'dart:io';

import 'package:flutter/material.dart';
import 'package:window_manager/window_manager.dart';

import 'src/app/session.dart';
import 'src/app/theme_controller.dart';
import 'src/ipc/client.dart';
import 'src/platform/tray/linux_tray.dart';
import 'src/platform/tray/tray_controller.dart';
import 'src/platform/tray/windows_tray.dart';
import 'src/ui/app.dart';

Future<void> main() async {
  WidgetsFlutterBinding.ensureInitialized();
  final client = DaemonClient();
  final session = DaemonSession(client)..start();
  // The persisted appearance theme, read before first paint.
  final themeController = await ThemeController.load();

  // System tray + hide-to-tray. The backend is platform-specific (Linux via
  // StatusNotifierItem/DBus, Windows via a Win32 method channel; macOS lands
  // next). init() is async and self-contained — it never throws past its own
  // guard and only traps the window-close once a tray actually shows, so don't
  // block first paint.
  final TrayController? tray = Platform.isLinux
      ? LinuxTray(client: client, session: session)
      : Platform.isWindows
          ? WindowsTray(client: client, session: session)
          : null;
  if (tray != null) {
    await windowManager.ensureInitialized();
    // Use the platform's NATIVE title bar — the app no longer draws its own.
    // This avoids the two-stacked-bars glitch some backends showed and gives
    // each OS its standard window controls; we only tune sizing here.
    try {
      await windowManager.setTitle('iron-link');
      // The shell (nav + content) is a vertical layout: clamp how small it can
      // shrink — below ~600 wide the six-item nav rail overflows — and on
      // Windows open it PORTRAIT instead of the Flutter runner's default
      // 1280x720 landscape, which looked stretched-wide.
      await windowManager.setMinimumSize(const Size(600, 640));
      if (Platform.isWindows) {
        await windowManager.setSize(const Size(680, 900));
        await windowManager.center();
      }
    } catch (_) {}
    unawaited(tray.init());
  }

  runApp(IronLinkApp(
      client: client, session: session, themeController: themeController));
}
