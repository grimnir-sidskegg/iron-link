/// Holds the selected appearance — the colour-scheme *family*, the light/dark/
/// system *mode*, and the reduce-motion preference — and notifies the app when
/// any changes. All persist to a small JSON file in the iron-link config root
/// (the same location the daemon and clients already share, resolved by
/// [Endpoint]) so they survive restarts — no extra dependency, best-effort: a
/// read or write failure just falls back to the defaults with an in-memory
/// selection.
library;

import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter/material.dart';

import '../ipc/endpoint.dart';
import '../ui/theme/app_theme.dart';

class ThemeController extends ChangeNotifier {
  ThemeController._(this._store, this._familyId, this._mode, this._reduceMotion);

  final _ThemeStore _store;
  String _familyId;
  ThemeMode _mode;
  bool _reduceMotion;

  /// The active colour-scheme family's stable id (e.g. `mint`, `tokyo`).
  String get familyId => _familyId;

  /// Whether to use the light variant, the dark one, or follow the OS.
  ThemeMode get mode => _mode;

  ThemeFamily get _family => familyById(_familyId);

  /// The family's two variants + the mode, handed to the root [MaterialApp] as
  /// `theme` / `darkTheme` / `themeMode`. In system mode Flutter follows the OS
  /// brightness — and switches live when it changes — for free.
  ThemeData get lightTheme => _family.light;
  ThemeData get darkTheme => _family.dark;
  ThemeMode get themeMode => _mode;

  /// The single resolved [ThemeData] (system mode resolved against the current
  /// platform brightness) — a convenience for callers/tests that want one.
  ThemeData get themeData => _family.variant(_resolvedBrightness());

  Brightness _resolvedBrightness() => switch (_mode) {
        ThemeMode.light => Brightness.light,
        ThemeMode.dark => Brightness.dark,
        ThemeMode.system =>
          WidgetsBinding.instance.platformDispatcher.platformBrightness,
      };

  /// Whether the user asked to suppress the looping ambient animations (the
  /// pipeline thread/dot, the power-orb pulse/breathe). State transitions still
  /// animate; only the perpetual motion stops.
  bool get reduceMotion => _reduceMotion;

  /// Switches to colour-scheme family [id] (a no-op if unchanged or unknown),
  /// repaints the app, and persists the choice in the background.
  void selectFamily(String id) {
    if (id == _familyId) return;
    if (familyById(id).id != id) return; // ignore ids with no real family
    _familyId = id;
    notifyListeners();
    unawaited(_store.write(_familyId, _mode, _reduceMotion));
  }

  /// Switches the light/dark/system mode (a no-op if unchanged), repaints, and
  /// persists.
  void setMode(ThemeMode mode) {
    if (mode == _mode) return;
    _mode = mode;
    notifyListeners();
    unawaited(_store.write(_familyId, _mode, _reduceMotion));
  }

  /// Toggles the reduce-motion preference, repaints, and persists.
  void setReduceMotion(bool value) {
    if (value == _reduceMotion) return;
    _reduceMotion = value;
    notifyListeners();
    unawaited(_store.write(_familyId, _mode, _reduceMotion));
  }

  /// Reads the persisted selection (falling back to the defaults, and migrating
  /// any pre-pairing single-theme id) before the app builds, so the first frame
  /// is already the right theme.
  static Future<ThemeController> load() async {
    final store = _ThemeStore();
    final saved = await store.read();
    String family;
    ThemeMode mode;
    if (saved.family != null) {
      // New format: normalise through the registry (unknown → default).
      family = familyById(saved.family).id;
      mode = _modeFromString(saved.mode);
    } else if (saved.legacyTheme != null) {
      // Old format: a single `theme` id → (family, mode).
      final migrated = migrateLegacyTheme(saved.legacyTheme!);
      family = familyById(migrated?.family).id;
      mode = migrated?.mode ?? ThemeMode.dark;
    } else {
      family = themeFamilies.first.id; // Mint
      mode = ThemeMode.dark; // default: Mint Dark
    }
    return ThemeController._(store, family, mode, saved.reduceMotion);
  }
}

ThemeMode _modeFromString(String? s) => switch (s) {
      'light' => ThemeMode.light,
      'system' => ThemeMode.system,
      _ => ThemeMode.dark,
    };

String _modeToString(ThemeMode m) => switch (m) {
      ThemeMode.light => 'light',
      ThemeMode.system => 'system',
      ThemeMode.dark => 'dark',
    };

/// The persisted appearance preferences. [legacyTheme] carries the pre-pairing
/// `theme` id (migrated on load) when the new [family]/[mode] keys are absent.
class _Appearance {
  const _Appearance({
    this.family,
    this.mode,
    this.legacyTheme,
    this.reduceMotion = false,
  });
  final String? family;
  final String? mode;
  final String? legacyTheme;
  final bool reduceMotion;
}

class _ThemeStore {
  File _file() => File('${Endpoint().configRoot()}/appearance.json');

  Future<_Appearance> read() async {
    try {
      final f = _file();
      if (!await f.exists()) return const _Appearance();
      final data = jsonDecode(await f.readAsString());
      if (data is! Map) return const _Appearance();
      return _Appearance(
        family: data['family'] as String?,
        mode: data['mode'] as String?,
        legacyTheme: data['theme'] as String?,
        reduceMotion: data['reduceMotion'] == true,
      );
    } catch (_) {
      return const _Appearance(); // missing/corrupt → defaults
    }
  }

  Future<void> write(String family, ThemeMode mode, bool reduceMotion) async {
    try {
      final f = _file();
      await f.parent.create(recursive: true);
      await f.writeAsString(jsonEncode({
        'family': family,
        'mode': _modeToString(mode),
        'reduceMotion': reduceMotion,
      }));
    } catch (_) {
      // Best-effort: the selection still applies in-memory for this run.
    }
  }
}
