/// The designer's type system, bundled (see pubspec `fonts:`) so the app stays
/// offline-first. Three families carry three jobs:
///
///  * **Space Grotesk** — display / headings / the status word (600, 700)
///  * **Plus Jakarta Sans** — all body, labels, list rows (400–700); the
///    app-wide default family
///  * **JetBrains Mono** — numbers, latency, IPs, uptime, log lines ([kMonoFamily])
///
/// [buildIronTextTheme] maps the spec's type scale onto Material's [TextTheme]
/// roles (colourless — Material tints each style from the scheme's `onSurface`),
/// and the [AppTextX] extension exposes the mono + display helpers widgets reach
/// for directly.
library;

import 'package:flutter/material.dart';

/// The bundled monospace face — JetBrains Mono (was the platform `monospace`).
/// Used for every fixed-width readout so a live counter doesn't reflow its
/// neighbours each tick and the tool keeps its terminal-adjacent feel.
const String kMonoFamily = 'JetBrainsMono';

/// The display/headings family.
const String kDisplayFamily = 'SpaceGrotesk';

/// The body/UI family (app-wide default).
const String kUiFamily = 'PlusJakartaSans';

/// Builds the app's [TextTheme] from the spec's scale. Colours are left null on
/// purpose: [ThemeData] merges this over its brightness default, so each style
/// inherits the right `onSurface`-derived colour without us pinning it here.
///
///  * Space Grotesk → the big display word + screen titles + section headers
///    (`displaySmall`, `headlineMedium/Small`, `titleLarge`).
///  * Plus Jakarta Sans → everything else (body, labels, buttons).
TextTheme buildIronTextTheme() {
  // Tight negative tracking on the large display faces, per the spec
  // (titles −0.01em); everything else sits at default tracking.
  const display = kDisplayFamily;
  const ui = kUiFamily;
  return const TextTheme(
    // The Home status word ("Connected" / "Disconnected") — 27/700.
    displaySmall: TextStyle(
        fontFamily: display,
        fontSize: 27,
        fontWeight: FontWeight.w700,
        letterSpacing: -0.4,
        height: 1.1),
    // Screen titles ("Logs", "Settings", …) — 24/700.
    headlineMedium: TextStyle(
        fontFamily: display,
        fontSize: 24,
        fontWeight: FontWeight.w700,
        letterSpacing: -0.3),
    headlineSmall: TextStyle(
        fontFamily: display,
        fontSize: 22,
        fontWeight: FontWeight.w700,
        letterSpacing: -0.2),
    // Section headers — 16–17/600.
    titleLarge: TextStyle(
        fontFamily: display, fontSize: 17, fontWeight: FontWeight.w600),
    titleMedium: TextStyle(
        fontFamily: display, fontSize: 15.5, fontWeight: FontWeight.w600),
    // Below here is body/UI in Jakarta.
    titleSmall: TextStyle(
        fontFamily: ui, fontSize: 13.5, fontWeight: FontWeight.w600),
    bodyLarge:
        TextStyle(fontFamily: ui, fontSize: 15, fontWeight: FontWeight.w500),
    bodyMedium:
        TextStyle(fontFamily: ui, fontSize: 14, fontWeight: FontWeight.w500),
    bodySmall:
        TextStyle(fontFamily: ui, fontSize: 12.5, fontWeight: FontWeight.w500),
    labelLarge:
        TextStyle(fontFamily: ui, fontSize: 13, fontWeight: FontWeight.w600),
    labelMedium:
        TextStyle(fontFamily: ui, fontSize: 12, fontWeight: FontWeight.w600),
    labelSmall: TextStyle(
        fontFamily: ui,
        fontSize: 11,
        fontWeight: FontWeight.w500,
        letterSpacing: 0.4),
  );
}

extension AppTextX on BuildContext {
  TextTheme get _text => Theme.of(this).textTheme;

  // ---- Mono (JetBrains Mono) — numbers, rates, IPs, uptimes, log lines ----

  /// Monospace `labelSmall` — for the tiny inline numbers in route chips.
  TextStyle? get monoLabelSmall =>
      _text.labelSmall?.copyWith(fontFamily: kMonoFamily);

  /// Monospace `bodySmall` — the workhorse for inline byte/rate figures.
  TextStyle? get monoBodySmall =>
      _text.bodySmall?.copyWith(fontFamily: kMonoFamily);

  /// Monospace `bodyMedium` — for context values (uptime, node) in the header.
  TextStyle? get monoBodyMedium =>
      _text.bodyMedium?.copyWith(fontFamily: kMonoFamily);

  /// Monospace `titleMedium` — for the prominent rate readout in the dashboard.
  TextStyle? get monoTitleMedium => _text.titleMedium
      ?.copyWith(fontFamily: kMonoFamily, fontWeight: FontWeight.w500);

  /// A free-size mono style (latency cells, the big Traffic stat number).
  TextStyle mono(double size,
          {FontWeight weight = FontWeight.w500, Color? color}) =>
      TextStyle(
          fontFamily: kMonoFamily,
          fontSize: size,
          fontWeight: weight,
          color: color);

  // ---- Display (Space Grotesk) ----

  /// The big status word on Home.
  TextStyle? get displayWord => _text.displaySmall;

  /// A screen title.
  TextStyle? get screenTitle => _text.headlineMedium;
}
