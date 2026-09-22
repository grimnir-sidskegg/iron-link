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
  // The window shell is the same on every desktop OS: native title bar, a
  // floor on how small the six-item nav rail can shrink, and a PORTRAIT
  // default on Windows/macOS instead of the runners' landscape defaults
  // (1280x720 on Windows, 800x600 in the macOS xib). Linux keeps its
  // runner's size.
  await windowManager.ensureInitialized();
  try {
    await windowManager.setTitle('iron-link');
    await windowManager.setMinimumSize(const Size(600, 640));
    if (Platform.isWindows || Platform.isMacOS) {
      await windowManager.setSize(const Size(680, 900));
      await windowManager.center();
    }
  } catch (_) {}
  if (tray != null) {
    unawaited(tray.init());
  }

  runApp(IronLinkApp(
      client: client, session: session, themeController: themeController));
}
