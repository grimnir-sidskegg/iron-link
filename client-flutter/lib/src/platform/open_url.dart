/// Opens a URL in the user's default browser through the OS opener — no
/// url_launcher plugin, just the per-OS command over dart:io. Only an https
/// URL is ever handed to the opener.
library;

import 'dart:io';

import 'process_spawner.dart';

/// The desktop OS the client runs on, for the few places that branch on it
/// (the opener command, the update affordance). Injected in tests.
enum HostOs {
  linux,
  windows,
  macos,
  other;

  static HostOs get current => Platform.isWindows
      ? HostOs.windows
      : Platform.isLinux
          ? HostOs.linux
          : Platform.isMacOS
              ? HostOs.macos
              : HostOs.other;
}

/// True when [url] is an absolute https URL with a host — the only shape
/// [openUrl] passes on. The notes link comes from a signed manifest, but the
/// OS opener would just as happily run a file:/javascript:/custom scheme.
bool isOpenableUrl(String url) {
  final uri = Uri.tryParse(url);
  return uri != null && uri.scheme == 'https' && uri.host.isNotEmpty;
}

/// Opens [url] in the default browser. Returns false — without spawning
/// anything — when the URL is not https, the OS has no opener, or the spawn
/// itself fails.
Future<bool> openUrl(String url,
    {HostOs? host, ProcessSpawner spawn = Process.start}) async {
  if (!isOpenableUrl(url)) return false;
  final (String? exe, List<String> args) = switch (host ?? HostOs.current) {
    HostOs.linux => ('xdg-open', [url]),
    HostOs.windows => ('rundll32', ['url.dll,FileProtocolHandler', url]),
    HostOs.macos => ('open', [url]),
    HostOs.other => (null, const []),
  };
  if (exe == null) return false;
  try {
    await spawn(exe, args, mode: ProcessStartMode.detached);
    return true;
  } on ProcessException {
    return false;
  }
}
