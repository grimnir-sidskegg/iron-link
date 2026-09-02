/// Control-endpoint and config-root discovery, mirroring the layout the Go
/// daemon uses (`internal/ipc`, `internal/store`) — the daemon and every
/// client MUST resolve the same location or they cannot find each other:
///
/// | | Linux | macOS | Windows |
/// |---|---|---|---|
/// | config root | `~/.config/iron-link` | `~/Library/Application Support/iron-link` | `%APPDATA%\iron-link` |
/// | endpoint | `$XDG_RUNTIME_DIR/iron-link.sock` (fallback: config root) | `<config root>/iron-link.sock` | `\\.\pipe\iron-link` |
///
/// On Linux there is also a system-wide endpoint: when the daemon runs as the
/// systemd service (root, started at boot) it pins its socket at
/// [systemSocket] instead of a per-user runtime dir, since a boot-time service
/// cannot rely on `$XDG_RUNTIME_DIR` existing. The client probes for it as a
/// fallback when no per-user socket is present.
///
/// Test overrides, honoured everywhere: `IRON_LINK_CONFIG_DIR` (the value
/// IS the config root) and `IRON_LINK_SOCKET` (the endpoint itself).
library;

import 'dart:io';

/// Resolves iron-link filesystem locations from an environment map
/// (injectable for tests; defaults to the process environment).
class Endpoint {
  Endpoint({Map<String, String>? environment, bool Function(String)? exists})
      : _env = environment ?? Platform.environment,
        _exists = exists ?? _defaultExists;

  /// The system-service control socket (systemd unit `IRON_LINK_SOCKET`),
  /// pinned to a fixed system path so an unprivileged client and the root
  /// service rendezvous without a per-user runtime dir.
  static const systemSocket = '/run/iron-link/iron-link.sock';

  final Map<String, String> _env;

  /// Path-existence probe (injectable for tests; defaults to the real FS).
  /// Type-agnostic — the endpoint is a unix socket, not a regular file.
  final bool Function(String) _exists;

  static bool _defaultExists(String path) =>
      FileSystemEntity.typeSync(path) != FileSystemEntityType.notFound;

  String? _envPath(String name) {
    final v = _env[name];
    return (v == null || v.isEmpty) ? null : v;
  }

  /// The iron-link config root directory.
  String configRoot() {
    final override = _envPath('IRON_LINK_CONFIG_DIR');
    if (override != null) return override;
    if (Platform.isLinux) {
      final base = _envPath('XDG_CONFIG_HOME') ?? '${_home()}/.config';
      return '$base/iron-link';
    }
    if (Platform.isMacOS) {
      return '${_home()}/Library/Application Support/iron-link';
    }
    if (Platform.isWindows) {
      final appData = _envPath('APPDATA');
      if (appData == null) {
        throw StateError('APPDATA is not set; cannot resolve the config root');
      }
      return '$appData\\iron-link';
    }
    throw UnsupportedError('unsupported platform: ${Platform.operatingSystem}');
  }

  /// The control endpoint: a unix socket path on Linux/macOS, the
  /// `\\.\pipe\iron-link` name on Windows.
  String socketPath() {
    final override = _envPath('IRON_LINK_SOCKET');
    if (override != null) return override;
    if (Platform.isLinux) {
      final runtime = _envPath('XDG_RUNTIME_DIR');
      final perUser =
          runtime != null ? '$runtime/iron-link.sock' : '${configRoot()}/iron-link.sock';
      // A manually `sudo`-started daemon binds in the invoking user's runtime
      // dir (perUser); the systemd service pins [systemSocket]. Only one daemon
      // runs at a time, so prefer whichever is actually present, and fall back
      // to perUser (the historical default) when neither exists yet.
      if (_exists(perUser)) return perUser;
      if (_exists(systemSocket)) return systemSocket;
      return perUser;
    }
    if (Platform.isMacOS) {
      return '${configRoot()}/iron-link.sock';
    }
    if (Platform.isWindows) {
      return r'\\.\pipe\iron-link';
    }
    throw UnsupportedError('unsupported platform: ${Platform.operatingSystem}');
  }

  String _home() {
    final home = _envPath('HOME');
    if (home == null) {
      throw StateError('HOME is not set; cannot resolve the config root');
    }
    return home;
  }
}
