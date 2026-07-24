/// The app's *semantic* colour roles, carried as a [ThemeExtension] so they
/// travel with the [ThemeData] and can be swapped wholesale per palette.
///
/// Two layers live here:
///
///  * [IronTheme] — the designer's 13 surface/text/accent tokens (the new
///    visual language). Every redesigned widget reads colours from these by
///    name (`context.iron.panel`, `context.iron.accent`, …) and NEVER hard-codes
///    a hex. This is the source of truth for the look.
///  * [AppColors] — the older meaning-by-name roles ("this is the proxy route",
///    "this is an error log", "the session is up"). These stay because real
///    screens (Traffic, Logs, Doctor, the tray) wire to them; they are derived
///    from / coherent with the [IronTheme] of the same palette.
///
/// One palette → one [IronTheme] + one [AppColors] (which embeds the IronTheme),
/// assembled in `app_theme.dart`.
library;

import 'package:flutter/material.dart';

import 'status_palette.dart';

/// The designer's token system: 13 semantic colours every surface reads from.
/// The user picks a scheme; swapping the scheme swaps this object and recolours
/// the whole app. Authored verbatim for the 5 design schemes, derived from a
/// palette for the rest (see `app_theme.dart`).
///
/// Token roles (from the spec):
///  * [app]          window backdrop (behind the app frame)
///  * [titlebar]     title bar + nav bar (both top)
///  * [sidebar]      deepest surface (the log console background)
///  * [panel]        base app surface
///  * [raised]       cards, rows, controls (one step above [panel])
///  * [border]       all hairlines / outlines (everything is outlined, 1px)
///  * [text]         primary text
///  * [dim]          secondary text
///  * [faint]        tertiary text / disabled
///  * [accent]       brand / connected / active (solid)
///  * [accentSoft]   accent at low alpha — active fills, chips
///  * [accentStrong] accent text-on-dark (slightly lighter / more saturated)
///  * [danger]       block rules, errors, the close dot
@immutable
class IronTheme {
  const IronTheme({
    required this.brightness,
    required this.app,
    required this.titlebar,
    required this.sidebar,
    required this.panel,
    required this.raised,
    required this.border,
    required this.text,
    required this.dim,
    required this.faint,
    required this.accent,
    required this.accentSoft,
    required this.accentStrong,
    required this.danger,
  });

  final Brightness brightness;
  final Color app;
  final Color titlebar;
  final Color sidebar;
  final Color panel;
  final Color raised;
  final Color border;
  final Color text;
  final Color dim;
  final Color faint;
  final Color accent;
  final Color accentSoft;
  final Color accentStrong;
  final Color danger;

  bool get isDark => brightness == Brightness.dark;

  /// Reads the active [IronTheme] off the theme (via [AppColors]); falls back
  /// to a derivation from the ambient [ColorScheme] under a bare MaterialApp
  /// (e.g. a widget test that never installed the app theme).
  static IronTheme of(BuildContext context) => context.iron;

  IronTheme lerp(IronTheme other, double t) => IronTheme(
        brightness: t < 0.5 ? brightness : other.brightness,
        app: Color.lerp(app, other.app, t)!,
        titlebar: Color.lerp(titlebar, other.titlebar, t)!,
        sidebar: Color.lerp(sidebar, other.sidebar, t)!,
        panel: Color.lerp(panel, other.panel, t)!,
        raised: Color.lerp(raised, other.raised, t)!,
        border: Color.lerp(border, other.border, t)!,
        text: Color.lerp(text, other.text, t)!,
        dim: Color.lerp(dim, other.dim, t)!,
        faint: Color.lerp(faint, other.faint, t)!,
        accent: Color.lerp(accent, other.accent, t)!,
        accentSoft: Color.lerp(accentSoft, other.accentSoft, t)!,
        accentStrong: Color.lerp(accentStrong, other.accentStrong, t)!,
        danger: Color.lerp(danger, other.danger, t)!,
      );

  /// Derives a sensible token set from a Material [ColorScheme] — the safety
  /// net for any code path that has a scheme but no authored [IronTheme].
  factory IronTheme.fromScheme(ColorScheme s) {
    final dark = s.brightness == Brightness.dark;
    Color toward(Color c, Color t, double amt) => Color.lerp(c, t, amt)!;
    final bg = s.surface;
    return IronTheme(
      brightness: s.brightness,
      app: toward(bg, dark ? Colors.black : Colors.white, dark ? 0.4 : 0.06),
      titlebar: toward(bg, dark ? Colors.black : Colors.white, dark ? 0.18 : 0.4),
      sidebar: toward(bg, dark ? Colors.black : Colors.white, dark ? 0.35 : 0.5),
      panel: bg,
      raised: s.surfaceContainerHigh,
      border: toward(s.outline, bg, 0.6),
      text: s.onSurface,
      dim: s.onSurfaceVariant,
      faint: toward(s.onSurfaceVariant, bg, 0.4),
      accent: s.primary,
      accentSoft: s.primary.withValues(alpha: 0.14),
      accentStrong: toward(s.primary, dark ? Colors.white : Colors.black, 0.18),
      danger: s.error,
    );
  }
}

@immutable
class AppColors extends ThemeExtension<AppColors> {
  const AppColors({
    required this.iron,
    required this.statusRunning,
    required this.statusConnecting,
    required this.statusOff,
    required this.warning,
    required this.routeProxy,
    required this.routeDirect,
    required this.routeBlocked,
    required this.routeDpi,
    required this.logError,
    required this.logWarn,
    required this.logInfo,
    required this.logTrace,
    required this.connectionPalette,
  });

  /// The designer's 13-token surface/text/accent system for this palette.
  /// Redesigned widgets read everything visual from here.
  final IronTheme iron;

  /// Session state: running (the tunnel is up), connecting (in-flight),
  /// off/idle. Mirrors [StatusPalette] so the in-app indicators (Home power
  /// icon, the Connected shield) match the tray dot exactly.
  final Color statusRunning;
  final Color statusConnecting;
  final Color statusOff;

  /// A general "attention / warning" accent — an insecure subscription source,
  /// a doctor check that warns. Distinct from the log-level [logWarn] by
  /// intent; the same amber by default.
  final Color warning;

  /// Per-route outcome on the Traffic tab: the desired path (proxy), neutral
  /// (direct), error (blocked), and the DPI-circumvented path (dpi).
  final Color routeProxy;
  final Color routeDirect;
  final Color routeBlocked;
  final Color routeDpi;

  /// Log-level tints on the Logs tab.
  final Color logError;
  final Color logWarn;
  final Color logInfo;
  final Color logTrace;

  /// A small fixed palette cycled by connection id, so every line of one
  /// connection tints the same way within a run and the eye can group them.
  final List<Color> connectionPalette;

  /// The default dark theme's semantic colours, derived from a Material
  /// [scheme]. Used as the safety-net fallback when no authored [AppColors] is
  /// installed (see [AppColorsX.appColors]).
  factory AppColors.dark(ColorScheme scheme) => AppColors(
        iron: IronTheme.fromScheme(scheme),
        statusRunning: StatusPalette.connected,
        statusConnecting: StatusPalette.connecting,
        statusOff: StatusPalette.off,
        warning: const Color(0xFFE3A13C),
        routeProxy: scheme.primary,
        routeDirect: scheme.onSurfaceVariant,
        routeBlocked: scheme.error,
        routeDpi: const Color(0xFFB8860B), // amber (dark goldenrod)
        logError: scheme.error,
        logWarn: const Color(0xFFE3A13C),
        logInfo: scheme.primary,
        logTrace: scheme.outline,
        connectionPalette: const [
          Color(0xFF80CBC4), // teal
          Color(0xFFFFCC80), // amber
          Color(0xFFF48FB1), // pink
          Color(0xFFA5D6A7), // green
          Color(0xFF90CAF9), // blue
          Color(0xFFCE93D8), // purple
          Color(0xFFFFAB91), // deep orange
          Color(0xFF9FA8DA), // indigo
        ],
      );

  /// Picks the connection-palette colour for a connection [id] — stable within
  /// a run (same id → same tint).
  Color connectionTint(String id) =>
      connectionPalette[id.hashCode.abs() % connectionPalette.length];

  @override
  AppColors copyWith({
    IronTheme? iron,
    Color? statusRunning,
    Color? statusConnecting,
    Color? statusOff,
    Color? warning,
    Color? routeProxy,
    Color? routeDirect,
    Color? routeBlocked,
    Color? routeDpi,
    Color? logError,
    Color? logWarn,
    Color? logInfo,
    Color? logTrace,
    List<Color>? connectionPalette,
  }) =>
      AppColors(
        iron: iron ?? this.iron,
        statusRunning: statusRunning ?? this.statusRunning,
        statusConnecting: statusConnecting ?? this.statusConnecting,
        statusOff: statusOff ?? this.statusOff,
        warning: warning ?? this.warning,
        routeProxy: routeProxy ?? this.routeProxy,
        routeDirect: routeDirect ?? this.routeDirect,
        routeBlocked: routeBlocked ?? this.routeBlocked,
        routeDpi: routeDpi ?? this.routeDpi,
        logError: logError ?? this.logError,
        logWarn: logWarn ?? this.logWarn,
        logInfo: logInfo ?? this.logInfo,
        logTrace: logTrace ?? this.logTrace,
        connectionPalette: connectionPalette ?? this.connectionPalette,
      );

  @override
  AppColors lerp(ThemeExtension<AppColors>? other, double t) {
    if (other is! AppColors) return this;
    return AppColors(
      iron: iron.lerp(other.iron, t),
      statusRunning: Color.lerp(statusRunning, other.statusRunning, t)!,
      statusConnecting: Color.lerp(statusConnecting, other.statusConnecting, t)!,
      statusOff: Color.lerp(statusOff, other.statusOff, t)!,
      warning: Color.lerp(warning, other.warning, t)!,
      routeProxy: Color.lerp(routeProxy, other.routeProxy, t)!,
      routeDirect: Color.lerp(routeDirect, other.routeDirect, t)!,
      routeBlocked: Color.lerp(routeBlocked, other.routeBlocked, t)!,
      routeDpi: Color.lerp(routeDpi, other.routeDpi, t)!,
      logError: Color.lerp(logError, other.logError, t)!,
      logWarn: Color.lerp(logWarn, other.logWarn, t)!,
      logInfo: Color.lerp(logInfo, other.logInfo, t)!,
      logTrace: Color.lerp(logTrace, other.logTrace, t)!,
      // Discrete list: snap rather than element-wise lerp (lengths may differ
      // across themes).
      connectionPalette: t < 0.5 ? connectionPalette : other.connectionPalette,
    );
  }
}

/// Ergonomic access: `context.appColors.routeProxy` and `context.iron.panel`.
/// [buildAppTheme] always registers the extension; the fallback keeps the
/// getters safe under a bare `MaterialApp` (e.g. a widget test that didn't
/// install the app theme) by deriving from whatever [ColorScheme] is in scope.
extension AppColorsX on BuildContext {
  AppColors get appColors =>
      Theme.of(this).extension<AppColors>() ??
      AppColors.dark(Theme.of(this).colorScheme);

  /// The active 13-token design palette — the canonical way redesigned widgets
  /// read colours: `final t = context.iron; … color: t.text`.
  IronTheme get iron => appColors.iron;
}
