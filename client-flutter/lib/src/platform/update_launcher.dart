/// The Windows one-click apply: launches the daemon-verified Inno installer
/// silently. A plain CreateProcess (`Process.start`) on purpose — NOT a
/// ShellExecute `runas`: the installer's SetupLdr is asInvoker and
/// self-elevates, so this raises exactly one UAC consent, and pre-elevating
/// would break its `runasoriginaluser` relaunch of the client.
library;

import 'dart:io';

import 'process_spawner.dart';

/// The silent-update argv. `/UPDATE=1` is the installer's own gate for its
/// unelevated relaunch-the-client [Run] entry (packaging/windows/iron-link.iss).
const updateInstallerArgs = [
  '/SILENT',
  '/SUPPRESSMSGBOXES',
  '/NORESTART',
  '/UPDATE=1',
];

/// The launch could not be started: a path that fails the sanity net, or
/// CreateProcess itself failed. A UAC cancel is NOT one of these — the
/// installer starts fine and exits non-zero out of our sight.
class UpdateLaunchError implements Exception {
  const UpdateLaunchError(this.message);

  final String message;

  @override
  String toString() => message;
}

/// Starts the installer at [setupPath] detached and returns once it is
/// running. The path must be the one the daemon's `apply_update` reply handed
/// back — the checks here are a sanity net (absolute, `.exe`, exists), not a
/// re-verification: the daemon just hashed the file, which lives in a
/// directory only it writes.
Future<void> launchUpdateInstaller(String setupPath,
    {ProcessSpawner spawn = Process.start}) async {
  final file = File(setupPath);
  if (setupPath.isEmpty || !file.isAbsolute) {
    throw UpdateLaunchError('installer path is not absolute: "$setupPath"');
  }
  if (!setupPath.toLowerCase().endsWith('.exe')) {
    throw UpdateLaunchError('installer path is not an .exe: "$setupPath"');
  }
  if (!file.existsSync()) {
    throw UpdateLaunchError('installer not found: "$setupPath"');
  }
  try {
    await spawn(setupPath, updateInstallerArgs,
        mode: ProcessStartMode.detached);
  } on ProcessException catch (e) {
    throw UpdateLaunchError('cannot start the installer: ${e.message}');
  }
}
