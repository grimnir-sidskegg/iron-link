/// Best-effort resolution of an executable path → its real desktop icon file.
///
/// The per-app Traffic tab knows each app only by the owning process's
/// executable path (e.g. `/usr/bin/Telegram`). To show the app's real icon we
/// mirror what desktops do: read the freedesktop `.desktop` database to map an
/// executable to an `Icon=` name, then resolve that name to a concrete PNG in
/// the `hicolor` theme (the cross-desktop fallback where apps install icons) or
/// `/usr/share/pixmaps`. PNG only — no SVG dependency; SVG-only apps simply
/// fall back to the caller's letter-avatar.
///
/// Linux reads the freedesktop database (above); Windows extracts the icon from
/// the EXE's PE resource table (see [windows_app_icon]). macOS still returns
/// null (letter-avatar) — TODO(macos): the .app bundle's .icns → PNG.
library;

import 'dart:io';

import 'windows_app_icon.dart';

/// Maps an executable path to its desktop icon `File` (a PNG), or null when no
/// icon can be resolved. Results are cached — positively AND negatively — so a
/// given path is scanned against the filesystem at most once.
///
/// The search roots are injectable so tests can point them at temp dirs; they
/// default to the real system locations and are only meaningful on Linux.
class AppIconResolver {
  AppIconResolver({
    List<String>? applicationDirs,
    List<String>? iconThemeDirs,
    List<String>? pixmapDirs,
    bool? isLinux,
    bool? isWindows,
  })  : _applicationDirs = applicationDirs ?? _defaultApplicationDirs(),
        _iconThemeDirs = iconThemeDirs ?? _defaultIconThemeDirs(),
        _pixmapDirs = pixmapDirs ?? const ['/usr/share/pixmaps'],
        _isLinux = isLinux ?? Platform.isLinux,
        _isWindows = isWindows ?? Platform.isWindows;

  /// `.desktop` database roots, searched in order; later files don't override
  /// earlier index entries (first writer wins, which is fine for our keys).
  final List<String> _applicationDirs;

  /// Icon theme roots that contain `hicolor/<NxN>/apps/<name>.png`.
  final List<String> _iconThemeDirs;

  /// Flat icon dirs holding `<name>.png` directly (legacy pixmaps).
  final List<String> _pixmapDirs;

  /// Gates the default-root behavior so a test with injected roots can run on
  /// non-Linux CI; with real defaults this is just `Platform.isLinux`.
  final bool _isLinux;

  /// Selects the Windows PE-icon path; defaults to `Platform.isWindows`.
  final bool _isWindows;

  /// path → resolved icon file (null = resolved-to-nothing, still cached).
  final Map<String, File?> _cache = {};

  /// Lowercased lookup key (exec basename / .desktop stem / WM class) → the
  /// raw `Icon=` value. Built lazily, once, on first resolve.
  Map<String, String>? _iconNames;

  static List<String> _defaultApplicationDirs() {
    final home = Platform.environment['HOME'] ?? '';
    return [
      '/usr/share/applications',
      '/usr/local/share/applications',
      if (home.isNotEmpty) '$home/.local/share/applications',
      '/var/lib/flatpak/exports/share/applications',
      if (home.isNotEmpty)
        '$home/.local/share/flatpak/exports/share/applications',
    ];
  }

  static List<String> _defaultIconThemeDirs() {
    final home = Platform.environment['HOME'] ?? '';
    return [
      if (home.isNotEmpty) '$home/.local/share/icons',
      '/usr/local/share/icons',
      '/usr/share/icons',
    ];
  }

  /// Resolves [executablePath] to an icon file, or null. Cached (positively and
  /// negatively); touches the filesystem on the first call for a given path
  /// (Linux also builds the `.desktop` index once), so call this off the widget
  /// build. On Windows the icon is extracted from the EXE and written to a temp
  /// PNG once per session; other platforms return null (letter-avatar).
  Future<File?> resolve(String executablePath) async {
    if (executablePath.isEmpty) return null;
    if (_cache.containsKey(executablePath)) return _cache[executablePath];
    File? file;
    if (_isLinux) {
      file = _resolveUncached(executablePath);
    } else if (_isWindows) {
      file = await _resolveWindows(executablePath);
    }
    _cache[executablePath] = file;
    return file;
  }

  /// Extracts [executablePath]'s embedded icon (best-effort) and caches it as a
  /// PNG under the system temp dir, returning that file. Pseudo-paths the daemon
  /// reports for kernel processes (`:System`, `:System Idle Process`) and any
  /// EXE with no extractable/readable icon resolve to null → letter-avatar.
  Future<File?> _resolveWindows(String executablePath) async {
    if (executablePath.startsWith(':')) return null;
    final png = await extractWindowsExeIconPng(executablePath);
    if (png == null) return null;
    try {
      final dir = Directory('${Directory.systemTemp.path}\\iron_link_icons');
      await dir.create(recursive: true);
      final file = File('${dir.path}\\${_cacheName(executablePath)}.png');
      await file.writeAsBytes(png, flush: true);
      return file;
    } on FileSystemException {
      return null; // temp not writable — fall back to the letter-avatar
    }
  }

  /// A stable, collision-resistant temp filename for [path]: its basename
  /// (sanitised) plus an FNV-1a hash of the full path, so two EXEs with the same
  /// name don't share a cache file. No crypto dependency — the hash only needs
  /// to disambiguate paths, not resist attack.
  String _cacheName(String path) {
    var hash = 0x811c9dc5;
    for (final c in path.codeUnits) {
      hash = ((hash ^ c) * 0x01000193) & 0xFFFFFFFF;
    }
    final base = _basename(path).replaceAll(RegExp(r'[^A-Za-z0-9._-]'), '_');
    return '$base-${hash.toRadixString(16)}';
  }

  File? _resolveUncached(String executablePath) {
    final index = _iconNames ??= _buildIndex();
    final base = _basename(executablePath).toLowerCase();
    // 1) Exact key — exec basename / .desktop stem / WM class.
    final exact = index[base];
    if (exact != null) {
      final f = _resolveIconName(exact);
      if (f != null) return f;
    }
    // 2) The binary's own basename as an icon name (some apps: name == icon).
    final selfNamed = _resolveIconName(_basename(executablePath));
    if (selfNamed != null) return selfNamed;
    // 3) Token fallback for WRAPPER apps whose running binary differs from the
    //    `.desktop` — e.g. the process is /opt/google/chrome/chrome (basename
    //    `chrome`) but google-chrome.desktop's Exec is the /usr/bin/
    //    google-chrome-stable wrapper. Match when the process basename EQUALS a
    //    hyphen/dot/underscore-separated token of a key, and prefer the SHORTEST
    //    such key whose icon actually resolves: that favours the base app
    //    ("google-chrome") over per-site PWAs / longer variants, and — because a
    //    token must equal the basename — "chrome" hits "google-chrome" but never
    //    "chromium".
    if (base.length >= 4) {
      final keys = index.keys.where((k) => _tokens(k).contains(base)).toList()
        ..sort((a, b) =>
            a.length != b.length ? a.length.compareTo(b.length) : a.compareTo(b));
      for (final k in keys) {
        final f = _resolveIconName(index[k]!);
        if (f != null) return f;
      }
    }
    return null;
  }

  /// A key's hyphen/dot/underscore-separated tokens (keys are already lower).
  Set<String> _tokens(String key) => key.split(RegExp(r'[-._]')).toSet();

  /// Walks the `.desktop` database once and builds the executable→icon index.
  Map<String, String> _buildIndex() {
    final index = <String, String>{};
    for (final dirPath in _applicationDirs) {
      final dir = Directory(dirPath);
      if (!dir.existsSync()) continue;
      for (final entry in dir.listSync(followLinks: true)) {
        if (entry is! File || !entry.path.endsWith('.desktop')) continue;
        _indexDesktopFile(entry, index);
      }
    }
    return index;
  }

  /// Parses one `.desktop` file's `[Desktop Entry]` group and registers its
  /// Icon under every key it can be found by: exec basename, file stem, and
  /// StartupWMClass. NoDisplay=true entries are skipped (not user-facing).
  void _indexDesktopFile(File file, Map<String, String> index) {
    String? exec;
    String? icon;
    String? wmClass;
    var noDisplay = false;
    var inEntry = false;
    final List<String> lines;
    try {
      lines = file.readAsLinesSync();
    } on FileSystemException {
      return; // unreadable file — skip it
    }
    for (final raw in lines) {
      final line = raw.trim();
      if (line.startsWith('[')) {
        // A new group begins; we only care about [Desktop Entry].
        inEntry = line == '[Desktop Entry]';
        continue;
      }
      if (!inEntry || line.isEmpty || line.startsWith('#')) continue;
      final eq = line.indexOf('=');
      if (eq < 0) continue;
      final key = line.substring(0, eq).trim();
      final value = line.substring(eq + 1).trim();
      switch (key) {
        case 'Exec':
          exec = value;
        case 'Icon':
          icon = value;
        case 'StartupWMClass':
          wmClass = value;
        case 'NoDisplay':
          noDisplay = value.toLowerCase() == 'true';
      }
    }
    if (noDisplay || icon == null || icon.isEmpty) return;
    // Skip browser web-apps / PWAs (Chrome `--app-id=` / `--app=`, or a `crx_`
    // WM class): their Icon is PER-SITE, and their Exec basename
    // (e.g. `google-chrome`) would otherwise shadow the real browser's key with
    // that per-site icon. Their traffic comes from the browser binary anyway.
    if ((exec != null &&
            (exec.contains('--app-id=') || exec.contains('--app='))) ||
        (wmClass != null && wmClass.startsWith('crx_'))) {
      return;
    }

    // file stem, e.g. org.telegram.desktop.desktop → org.telegram.desktop
    var stem = _basename(file.path);
    if (stem.endsWith('.desktop')) {
      stem = stem.substring(0, stem.length - '.desktop'.length);
    }
    index.putIfAbsent(stem.toLowerCase(), () => icon!);

    if (exec != null && exec.isNotEmpty) {
      // Exec first token, basename — `Telegram -- %U` → `Telegram`.
      final first = exec.split(RegExp(r'\s+')).first;
      final execBase = _basename(first);
      if (execBase.isNotEmpty) {
        index.putIfAbsent(execBase.toLowerCase(), () => icon!);
      }
    }
    if (wmClass != null && wmClass.isNotEmpty) {
      index.putIfAbsent(wmClass.toLowerCase(), () => icon!);
    }
  }

  /// Resolves an `Icon=` value to a concrete PNG file, or null. An absolute
  /// path is used as-is (if it's an existing `.png`); a bare name is searched
  /// across the hicolor `apps` size dirs (largest size first — Flutter
  /// downscales) and then the pixmaps dirs.
  File? _resolveIconName(String iconName) {
    if (iconName.isEmpty) return null;
    // An Icon= value carrying a path separator is a direct file reference
    // (`/abs/x.png` on Linux; a Windows temp path under `flutter test`) — a bare
    // icon name never contains one.
    if (iconName.contains('/') || iconName.contains('\\')) {
      if (iconName.toLowerCase().endsWith('.png')) {
        final f = File(iconName);
        if (f.existsSync()) return f;
      }
      return null; // a path but not an existing PNG — no SVG support
    }
    for (final themeRoot in _iconThemeDirs) {
      final apps = Directory('$themeRoot/hicolor');
      if (!apps.existsSync()) continue;
      for (final size in _hicolorSizesDescending(apps)) {
        final f = File('${apps.path}/$size/apps/$iconName.png');
        if (f.existsSync()) return f;
      }
    }
    for (final pixmaps in _pixmapDirs) {
      final f = File('$pixmaps/$iconName.png');
      if (f.existsSync()) return f;
    }
    return null;
  }

  /// The hicolor size dirs (`512x512`, `256x256`, …) under [hicolor], sorted by
  /// pixel size descending. The `@2`/scalable/non-`NxN` dirs are ignored — we
  /// want plain raster sizes and the biggest first.
  List<String> _hicolorSizesDescending(Directory hicolor) {
    final sizes = <(int, String)>[];
    for (final entry in hicolor.listSync()) {
      if (entry is! Directory) continue;
      final name = _basename(entry.path);
      final m = RegExp(r'^(\d+)x\1$').firstMatch(name);
      if (m == null) continue; // skip `128x128@2`, `scalable`, `symbolic`, …
      sizes.add((int.parse(m.group(1)!), name));
    }
    sizes.sort((a, b) => b.$1.compareTo(a.$1));
    return [for (final s in sizes) s.$2];
  }

  /// Last path segment after a `/` (or `\`). The resolver's inputs are POSIX in
  /// production (Linux-only), but `Directory.listSync` yields native `\`
  /// separators on a Windows test runner — accept both so the algorithm tests
  /// pass everywhere.
  String _basename(String path) {
    var cut = path.lastIndexOf('/');
    final back = path.lastIndexOf('\\');
    if (back > cut) cut = back;
    return cut < 0 ? path : path.substring(cut + 1);
  }
}
