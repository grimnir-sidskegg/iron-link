/// The app's theme registry: the designer's signature schemes plus the
/// project's curated palettes, all expressed through one token system
/// ([IronTheme], 13 tokens). Each entry assembles a Material [ColorScheme] +
/// the semantic [AppColors] extension (which embeds the [IronTheme]) into one
/// [ThemeData]; [ThemeController] picks the active one. Widgets read colours via
/// `context.iron` / `context.appColors` / `Theme.of(context)`, never by reaching
/// in here.
///
/// Two ways a scheme is authored:
///  * **Design schemes** — Mint Dark, Mint Light, Catppuccin Mocha, Nord, Tokyo
///    Night — carry the spec's *exact* 13 token values ([_design]).
///  * **Curated palettes** — the rest — give the older minimal [_Palette]; the
///    13 tokens are *derived* from it ([_deriveIron]) so every existing theme
///    works under the redesigned, token-driven UI.
library;

import 'package:flutter/material.dart';

import 'app_colors.dart';
import 'app_typography.dart';
import 'status_palette.dart';

/// A selectable colour scheme as a **dark + light pair**. The user picks the
/// family ([ThemeController.familyId]); a separate light/dark/system mode
/// ([ThemeController.themeMode]) decides which [variant] shows. Every family
/// ships both — the canonical upstream light where one exists (Tokyo Night Day,
/// Alucard, One Light, Rosé Pine Dawn, Everforest Light, …), otherwise a
/// hand-tuned light twin (Nord, Monokai, Iron).
class ThemeFamily {
  const ThemeFamily({
    required this.id,
    required this.label,
    required this.dark,
    required this.light,
  });

  /// Stable id persisted to disk — never rename once shipped.
  final String id;

  /// Human label for the Appearance picker.
  final String label;

  final ThemeData dark;
  final ThemeData light;

  /// The [ThemeData] for a resolved [brightness].
  ThemeData variant(Brightness brightness) =>
      brightness == Brightness.dark ? dark : light;

  /// The design palette of one variant — for swatch previews.
  IronTheme iron(Brightness brightness) =>
      variant(brightness).extension<AppColors>()!.iron;
}

/// All selectable families, in picker order. **Mint** (the designer's
/// signature) is first and the default; the mode defaults to dark (Mint Dark).
/// Every previously-shipped single-theme id still resolves via
/// [migrateLegacyTheme]. Each family pairs a dark variant with a light one —
/// the canonical upstream light where it exists, a hand-tuned twin otherwise.
final List<ThemeFamily> themeFamilies = [
  ThemeFamily(
      id: 'mint', label: 'Mint', dark: _mintDark(), light: _mintLight()),
  ThemeFamily(
      id: 'tokyo',
      label: 'Tokyo Night',
      dark: _tokyoNightDesign(),
      light: _fromPalette(_tokyoNightDay)),
  ThemeFamily(
      id: 'catppuccin',
      label: 'Catppuccin',
      dark: _catppuccinMochaDesign(),
      light: _fromPalette(_catppuccinLatte)),
  ThemeFamily(
      id: 'nord',
      label: 'Nord',
      dark: _nordDesign(),
      light: _fromPalette(_nordLight)),
  ThemeFamily(
      id: 'iron',
      label: 'Iron',
      dark: _fromPalette(_ironDark),
      light: _fromPalette(_ironLight)),
  ThemeFamily(
      id: 'dracula',
      label: 'Dracula',
      dark: _fromPalette(_dracula),
      light: _fromPalette(_alucard)),
  ThemeFamily(
      id: 'one',
      label: 'One',
      dark: _fromPalette(_oneDark),
      light: _fromPalette(_oneLight)),
  ThemeFamily(
      id: 'gruvbox',
      label: 'Gruvbox',
      dark: _fromPalette(_gruvboxDark),
      light: _fromPalette(_gruvboxLight)),
  ThemeFamily(
      id: 'monokai',
      label: 'Monokai',
      dark: _fromPalette(_monokai),
      light: _fromPalette(_monokaiLight)),
  ThemeFamily(
      id: 'solarized',
      label: 'Solarized',
      dark: _fromPalette(_solarizedDark),
      light: _fromPalette(_solarizedLight)),
  ThemeFamily(
      id: 'rose_pine',
      label: 'Rosé Pine',
      dark: _fromPalette(_rosePine),
      light: _fromPalette(_rosePineDawn)),
  ThemeFamily(
      id: 'everforest',
      label: 'Everforest',
      dark: _fromPalette(_everforestDark),
      light: _fromPalette(_everforestLight)),
];

/// The family for [id], or the default (Mint) when [id] is null/unknown — a
/// dropped or renamed family degrades gracefully rather than crashing.
ThemeFamily familyById(String? id) => themeFamilies.firstWhere(
    (f) => f.id == id,
    orElse: () => themeFamilies.first);

/// Maps a legacy single-theme id (the pre-pairing `appearance.json` `theme`
/// field) onto a (family, mode) pair, so an existing selection keeps working
/// across the dark/light split. Returns null for an unknown id.
({String family, ThemeMode mode})? migrateLegacyTheme(String id) {
  const map = <String, (String, ThemeMode)>{
    'mint_dark': ('mint', ThemeMode.dark),
    'mint_light': ('mint', ThemeMode.light),
    'tokyo_night': ('tokyo', ThemeMode.dark),
    'catppuccin_mocha': ('catppuccin', ThemeMode.dark),
    'catppuccin_latte': ('catppuccin', ThemeMode.light),
    'nord': ('nord', ThemeMode.dark),
    'iron_dark': ('iron', ThemeMode.dark),
    'dracula': ('dracula', ThemeMode.dark),
    'one_dark': ('one', ThemeMode.dark),
    'gruvbox_dark': ('gruvbox', ThemeMode.dark),
    'gruvbox_light': ('gruvbox', ThemeMode.light),
    'monokai': ('monokai', ThemeMode.dark),
    'solarized_dark': ('solarized', ThemeMode.dark),
    'solarized_light': ('solarized', ThemeMode.light),
    'rose_pine': ('rose_pine', ThemeMode.dark),
    'everforest_dark': ('everforest', ThemeMode.dark),
  };
  final m = map[id];
  return m == null ? null : (family: m.$1, mode: m.$2);
}

/// The default theme (Mint Dark) — also the back-compat entry point for any
/// caller/test that wants "the theme" without going through the controller.
ThemeData buildAppTheme() => themeFamilies.first.dark;

// ---------------------------------------------------------------------------
// Core builder: IronTheme (+ the few extra semantic colours) → ThemeData.
// ---------------------------------------------------------------------------

/// Assembles the full [ThemeData] from a [iron] token set plus the handful of
/// semantic colours the 13 tokens don't carry (the running-green, the
/// warn-amber, and the per-connection tint cycle). Builds a coherent Material
/// [ColorScheme] so stock widgets (dialogs, snackbars, text fields) stay on
/// palette, and installs the design [TextTheme] + the emoji-flag fallback.
ThemeData _buildTheme({
  required IronTheme iron,
  required Color green,
  required Color yellow,
  required List<Color> connection,
}) {
  final scheme = _schemeFrom(iron);
  final appColors = AppColors(
    iron: iron,
    statusRunning: green,
    statusConnecting: yellow,
    statusOff: StatusPalette.off,
    warning: yellow,
    routeProxy: iron.accent,
    routeDirect: iron.dim,
    routeBlocked: iron.danger,
    routeDpi: yellow,
    logError: iron.danger,
    logWarn: yellow,
    logInfo: iron.accentStrong,
    logTrace: iron.dim,
    connectionPalette: connection,
  );
  return ThemeData(
    colorScheme: scheme,
    brightness: iron.brightness,
    scaffoldBackgroundColor: iron.panel,
    canvasColor: iron.panel,
    // Desktop wants tighter controls than the touch default — dense lists
    // (Traffic, Logs) read better compact.
    visualDensity: VisualDensity.compact,
    fontFamily: kUiFamily,
    textTheme: buildIronTextTheme(),
    extensions: [appColors],
    // Flag emoji in node names render on Windows too: every text style falls
    // back to the bundled flags-only Noto Color Emoji subset (see pubspec).
    fontFamilyFallback: const ['NotoColorEmojiFlags'],
  );
}

/// A Material [ColorScheme] pinned to the [iron] tokens. Seeded from the accent
/// (so the tonal roles chips use stay coherent), then the roles the app reads —
/// surfaces, text, accent, error, outline — are pinned to the token colours.
ColorScheme _schemeFrom(IronTheme iron) {
  final onAccent = iron.isDark ? iron.app : const Color(0xFFFFFFFF);
  return ColorScheme.fromSeed(
    seedColor: iron.accent,
    brightness: iron.brightness,
  ).copyWith(
    primary: iron.accent,
    onPrimary: onAccent,
    primaryContainer: Color.alphaBlend(iron.accentSoft, iron.panel),
    onPrimaryContainer: iron.accentStrong,
    secondary: iron.accentStrong,
    onSecondary: onAccent,
    error: iron.danger,
    onError: const Color(0xFFFFFFFF),
    // The "daemon down" banner uses these — a subtle red-tinted card.
    errorContainer:
        Color.alphaBlend(iron.danger.withValues(alpha: 0.18), iron.panel),
    onErrorContainer: iron.text,
    surface: iron.panel,
    onSurface: iron.text,
    onSurfaceVariant: iron.dim,
    outline: iron.border,
    outlineVariant: iron.border,
    surfaceContainerLowest: iron.sidebar,
    surfaceContainerLow: iron.panel,
    surfaceContainer: iron.raised,
    surfaceContainerHigh: iron.raised,
    surfaceContainerHighest: iron.raised,
  );
}

// ---------------------------------------------------------------------------
// Design schemes — exact 13-token values from the spec.
// ---------------------------------------------------------------------------

ThemeData _mintDark() => _buildTheme(
      iron: const IronTheme(
        brightness: Brightness.dark,
        app: Color(0xFF070A0D),
        titlebar: Color(0xFF12161D),
        sidebar: Color(0xFF0A0D11),
        panel: Color(0xFF161B22),
        raised: Color(0xFF1D232C),
        border: Color(0xFF272E38),
        text: Color(0xFFE9EDF1),
        dim: Color(0xFF99A3AE),
        faint: Color(0xFF5F6976),
        accent: Color(0xFF5FD38D),
        accentSoft: Color.fromRGBO(95, 211, 141, 0.13),
        accentStrong: Color(0xFF84E0A6),
        danger: Color(0xFFEF6F6C),
      ),
      green: const Color(0xFF5FD38D),
      yellow: const Color(0xFFE3A13C),
      connection: const [
        Color(0xFF5FD38D),
        Color(0xFF84E0A6),
        Color(0xFF6FD1C4),
        Color(0xFFE3A13C),
        Color(0xFFEF6F6C),
        Color(0xFF9AA9F0),
        Color(0xFFC59BE6),
        Color(0xFFE08AB0),
      ],
    );

ThemeData _mintLight() => _buildTheme(
      iron: const IronTheme(
        brightness: Brightness.light,
        app: Color(0xFFDFE3DE),
        titlebar: Color(0xFFFFFFFF),
        sidebar: Color(0xFFF4F6F3),
        panel: Color(0xFFFFFFFF),
        raised: Color(0xFFF1F3EF),
        border: Color(0xFFE3E6E1),
        text: Color(0xFF171B1F),
        dim: Color(0xFF586069),
        faint: Color(0xFF9AA2AA),
        accent: Color(0xFF1F9D5F),
        accentSoft: Color.fromRGBO(31, 157, 95, 0.12),
        accentStrong: Color(0xFF178050),
        danger: Color(0xFFD24B48),
      ),
      green: const Color(0xFF1F9D5F),
      yellow: const Color(0xFFB5760E),
      connection: const [
        Color(0xFF1F9D5F),
        Color(0xFF178050),
        Color(0xFF0E7C86),
        Color(0xFFB5760E),
        Color(0xFFD24B48),
        Color(0xFF3A5BD0),
        Color(0xFF8A45C0),
        Color(0xFFC23F86),
      ],
    );

ThemeData _tokyoNightDesign() => _buildTheme(
      iron: const IronTheme(
        brightness: Brightness.dark,
        app: Color(0xFF0D0D12),
        titlebar: Color(0xFF13131A),
        sidebar: Color(0xFF13131A),
        panel: Color(0xFF1F2335),
        raised: Color(0xFF292E42),
        border: Color(0xFF272B3D),
        text: Color(0xFFC0CAF5),
        dim: Color(0xFF9AA5CE),
        faint: Color(0xFF545C7E),
        accent: Color(0xFF7AA2F7),
        accentSoft: Color.fromRGBO(122, 162, 247, 0.15),
        accentStrong: Color(0xFFA3C0FF),
        danger: Color(0xFFF7768E),
      ),
      green: const Color(0xFF9ECE6A),
      yellow: const Color(0xFFE0AF68),
      connection: const [
        Color(0xFF7AA2F7),
        Color(0xFF7DCFFF),
        Color(0xFFBB9AF7),
        Color(0xFF9ECE6A),
        Color(0xFFE0AF68),
        Color(0xFFF7768E),
        Color(0xFF2AC3DE),
        Color(0xFFFF9E64),
      ],
    );

ThemeData _catppuccinMochaDesign() => _buildTheme(
      iron: const IronTheme(
        brightness: Brightness.dark,
        app: Color(0xFF11111B),
        titlebar: Color(0xFF11111B),
        sidebar: Color(0xFF11111B),
        panel: Color(0xFF1E1E2E),
        raised: Color(0xFF313244),
        border: Color(0xFF2C2C3F),
        text: Color(0xFFCDD6F4),
        dim: Color(0xFFA6ADC8),
        faint: Color(0xFF6C7086),
        accent: Color(0xFFCBA6F7),
        accentSoft: Color.fromRGBO(203, 166, 247, 0.15),
        accentStrong: Color(0xFFDBBAFB),
        danger: Color(0xFFF38BA8),
      ),
      green: const Color(0xFFA6E3A1),
      yellow: const Color(0xFFF9E2AF),
      connection: const [
        Color(0xFF89B4FA),
        Color(0xFF94E2D5),
        Color(0xFFCBA6F7),
        Color(0xFFA6E3A1),
        Color(0xFFF9E2AF),
        Color(0xFFF38BA8),
        Color(0xFFFAB387),
        Color(0xFFF5C2E7),
      ],
    );

ThemeData _nordDesign() => _buildTheme(
      iron: const IronTheme(
        brightness: Brightness.dark,
        app: Color(0xFF1C2027),
        titlebar: Color(0xFF222730),
        sidebar: Color(0xFF222730),
        panel: Color(0xFF2E3440),
        raised: Color(0xFF3B4252),
        border: Color(0xFF3B4252),
        text: Color(0xFFECEFF4),
        dim: Color(0xFFAAB4C8),
        faint: Color(0xFF717C93),
        accent: Color(0xFF88C0D0),
        accentSoft: Color.fromRGBO(136, 192, 208, 0.16),
        accentStrong: Color(0xFF8FBCBB),
        danger: Color(0xFFBF616A),
      ),
      green: const Color(0xFFA3BE8C),
      yellow: const Color(0xFFEBCB8B),
      connection: const [
        Color(0xFF88C0D0),
        Color(0xFF8FBCBB),
        Color(0xFF81A1C1),
        Color(0xFFA3BE8C),
        Color(0xFFEBCB8B),
        Color(0xFFBF616A),
        Color(0xFFB48EAD),
        Color(0xFFD08770),
      ],
    );

// ---------------------------------------------------------------------------
// Curated palettes — the 13 tokens are derived from this minimal set.
// ---------------------------------------------------------------------------

/// Builds a [ThemeData] from a [_Palette] by deriving the design tokens.
ThemeData _fromPalette(_Palette p) => _buildTheme(
      iron: _deriveIron(p),
      green: p.green,
      yellow: p.yellow,
      connection: p.connection,
    );

/// Derives the 13 design tokens from a [_Palette]'s minimal set. Surfaces step
/// out from the palette's two backgrounds (deeper for app/sidebar, lifted for
/// raised); text gets a third "faint" tier between dim and the surface; the
/// accent gains a soft (low-alpha) and a strong (lighter/darker) variant.
IronTheme _deriveIron(_Palette p) {
  final dark = p.brightness == Brightness.dark;
  const deepen = Color(0xFF000000); // app/sidebar step below panel, both modes
  Color mix(Color a, Color b, double t) => Color.lerp(a, b, t)!;
  return IronTheme(
    brightness: p.brightness,
    app: mix(p.bg, deepen, dark ? 0.32 : 0.05),
    titlebar: dark
        ? mix(p.bg, deepen, 0.12)
        : mix(p.surface, const Color(0xFFFFFFFF), 0.55),
    sidebar: dark ? mix(p.bg, deepen, 0.18) : mix(p.bg, deepen, 0.02),
    panel: p.surface,
    raised: p.surfaceHigh,
    border: mix(p.surfaceHigh, p.fgDim, dark ? 0.22 : 0.30),
    text: p.fg,
    dim: p.fgDim,
    faint: mix(p.fgDim, p.surface, 0.45),
    accent: p.primary,
    accentSoft: p.primary.withValues(alpha: 0.14),
    accentStrong: dark
        ? mix(p.primary, const Color(0xFFFFFFFF), 0.20)
        : mix(p.primary, const Color(0xFF000000), 0.12),
    danger: p.red,
  );
}

/// The handful of colours a curated palette needs: two background levels + an
/// elevated one, primary/dim text, the accent, and the semantic red/green/
/// yellow. Everything else is derived (see [_deriveIron]).
class _Palette {
  const _Palette({
    required this.brightness,
    required this.bg,
    required this.surface,
    required this.surfaceHigh,
    required this.fg,
    required this.fgDim,
    required this.primary,
    required this.onPrimary,
    required this.red,
    required this.green,
    required this.yellow,
    required this.connection,
  });

  final Brightness brightness;
  final Color bg; // deepest background
  final Color surface; // cards / containers (→ panel)
  final Color surfaceHigh; // elevated containers, avatars (→ raised)
  final Color fg; // primary text
  final Color fgDim; // secondary text / outline
  final Color primary; // accent
  final Color onPrimary; // text on the accent
  final Color red; // error / blocked / danger
  final Color green; // running / success
  final Color yellow; // warn / connecting
  final List<Color> connection; // connection-id tint cycle
}

/// The original Iron look — the iron-link blue on dark slate.
const _ironDark = _Palette(
  brightness: Brightness.dark,
  bg: Color(0xFF0E1116),
  surface: Color(0xFF161B22),
  surfaceHigh: Color(0xFF1D232C),
  fg: Color(0xFFE9EDF1),
  fgDim: Color(0xFF99A3AE),
  primary: Color(0xFF5B8DEF),
  onPrimary: Color(0xFF0E1116),
  red: Color(0xFFEF6F6C),
  green: Color(0xFF00E676),
  yellow: Color(0xFFE3A13C),
  connection: [
    Color(0xFF5B8DEF),
    Color(0xFF56B6C2),
    Color(0xFFBB9AF7),
    Color(0xFF7FD1A6),
    Color(0xFFE3A13C),
    Color(0xFFEF6F6C),
    Color(0xFF9FA8DA),
    Color(0xFFF48FB1),
  ],
);

const _gruvboxDark = _Palette(
  brightness: Brightness.dark,
  bg: Color(0xFF282828),
  surface: Color(0xFF3C3836),
  surfaceHigh: Color(0xFF504945),
  fg: Color(0xFFEBDBB2),
  fgDim: Color(0xFFA89984),
  primary: Color(0xFF83A598),
  onPrimary: Color(0xFF282828),
  red: Color(0xFFFB4934),
  green: Color(0xFFB8BB26),
  yellow: Color(0xFFFABD2F),
  connection: [
    Color(0xFF8EC07C),
    Color(0xFFFABD2F),
    Color(0xFFD3869B),
    Color(0xFFB8BB26),
    Color(0xFF83A598),
    Color(0xFFFE8019),
    Color(0xFFFB4934),
    Color(0xFFD65D0E),
  ],
);

const _gruvboxLight = _Palette(
  brightness: Brightness.light,
  bg: Color(0xFFFBF1C7),
  surface: Color(0xFFEBDBB2),
  surfaceHigh: Color(0xFFD5C4A1),
  fg: Color(0xFF3C3836),
  fgDim: Color(0xFF7C6F64),
  primary: Color(0xFF076678),
  onPrimary: Color(0xFFFBF1C7),
  red: Color(0xFFCC241D),
  green: Color(0xFF79740E),
  yellow: Color(0xFFB57614),
  connection: [
    Color(0xFF427B58),
    Color(0xFFB57614),
    Color(0xFF8F3F71),
    Color(0xFF79740E),
    Color(0xFF076678),
    Color(0xFFAF3A03),
    Color(0xFF9D0006),
    Color(0xFFB16286),
  ],
);

const _solarizedLight = _Palette(
  brightness: Brightness.light,
  bg: Color(0xFFFDF6E3),
  surface: Color(0xFFEEE8D5),
  surfaceHigh: Color(0xFFE6DFC8),
  fg: Color(0xFF586E75),
  fgDim: Color(0xFF93A1A1),
  primary: Color(0xFF268BD2),
  onPrimary: Color(0xFFFDF6E3),
  red: Color(0xFFDC322F),
  green: Color(0xFF859900),
  yellow: Color(0xFFB58900),
  connection: [
    Color(0xFF268BD2),
    Color(0xFF2AA198),
    Color(0xFF6C71C4),
    Color(0xFF859900),
    Color(0xFFB58900),
    Color(0xFFDC322F),
    Color(0xFFD33682),
    Color(0xFFCB4B16),
  ],
);

const _dracula = _Palette(
  brightness: Brightness.dark,
  bg: Color(0xFF282A36),
  surface: Color(0xFF343746),
  surfaceHigh: Color(0xFF44475A),
  fg: Color(0xFFF8F8F2),
  fgDim: Color(0xFF6272A4),
  primary: Color(0xFFBD93F9),
  onPrimary: Color(0xFF282A36),
  red: Color(0xFFFF5555),
  green: Color(0xFF50FA7B),
  yellow: Color(0xFFF1FA8C),
  connection: [
    Color(0xFFBD93F9),
    Color(0xFF8BE9FD),
    Color(0xFFFF79C6),
    Color(0xFF50FA7B),
    Color(0xFFF1FA8C),
    Color(0xFFFFB86C),
    Color(0xFFFF5555),
    Color(0xFF6272A4),
  ],
);

const _oneDark = _Palette(
  brightness: Brightness.dark,
  bg: Color(0xFF282C34),
  surface: Color(0xFF2C313A),
  surfaceHigh: Color(0xFF3B4048),
  fg: Color(0xFFABB2BF),
  fgDim: Color(0xFF5C6370),
  primary: Color(0xFF61AFEF),
  onPrimary: Color(0xFF282C34),
  red: Color(0xFFE06C75),
  green: Color(0xFF98C379),
  yellow: Color(0xFFE5C07B),
  connection: [
    Color(0xFF61AFEF),
    Color(0xFF56B6C2),
    Color(0xFFC678DD),
    Color(0xFF98C379),
    Color(0xFFE5C07B),
    Color(0xFFE06C75),
    Color(0xFFD19A66),
    Color(0xFFABB2BF),
  ],
);

const _monokai = _Palette(
  brightness: Brightness.dark,
  bg: Color(0xFF272822),
  surface: Color(0xFF3E3D32),
  surfaceHigh: Color(0xFF49483E),
  fg: Color(0xFFF8F8F2),
  fgDim: Color(0xFF75715E),
  primary: Color(0xFF66D9EF),
  onPrimary: Color(0xFF272822),
  red: Color(0xFFF92672),
  green: Color(0xFFA6E22E),
  yellow: Color(0xFFE6DB74),
  connection: [
    Color(0xFF66D9EF),
    Color(0xFFA6E22E),
    Color(0xFFAE81FF),
    Color(0xFFE6DB74),
    Color(0xFFFD971F),
    Color(0xFFF92672),
    Color(0xFFCFCFC2),
    Color(0xFF75715E),
  ],
);

const _solarizedDark = _Palette(
  brightness: Brightness.dark,
  bg: Color(0xFF002B36),
  surface: Color(0xFF073642),
  surfaceHigh: Color(0xFF094553),
  fg: Color(0xFF93A1A1),
  fgDim: Color(0xFF586E75),
  primary: Color(0xFF268BD2),
  onPrimary: Color(0xFFFDF6E3),
  red: Color(0xFFDC322F),
  green: Color(0xFF859900),
  yellow: Color(0xFFB58900),
  connection: [
    Color(0xFF268BD2),
    Color(0xFF2AA198),
    Color(0xFF6C71C4),
    Color(0xFF859900),
    Color(0xFFB58900),
    Color(0xFFDC322F),
    Color(0xFFD33682),
    Color(0xFFCB4B16),
  ],
);

const _rosePine = _Palette(
  brightness: Brightness.dark,
  bg: Color(0xFF191724),
  surface: Color(0xFF1F1D2E),
  surfaceHigh: Color(0xFF26233A),
  fg: Color(0xFFE0DEF4),
  fgDim: Color(0xFF908CAA),
  primary: Color(0xFFC4A7E7),
  onPrimary: Color(0xFF191724),
  red: Color(0xFFEB6F92),
  green: Color(0xFF9CCFD8),
  yellow: Color(0xFFF6C177),
  connection: [
    Color(0xFFC4A7E7),
    Color(0xFF9CCFD8),
    Color(0xFFEB6F92),
    Color(0xFFF6C177),
    Color(0xFF31748F),
    Color(0xFFEBBCBA),
    Color(0xFF908CAA),
    Color(0xFF6E6A86),
  ],
);

const _everforestDark = _Palette(
  brightness: Brightness.dark,
  bg: Color(0xFF2D353B),
  surface: Color(0xFF343F44),
  surfaceHigh: Color(0xFF3D484D),
  fg: Color(0xFFD3C6AA),
  fgDim: Color(0xFF859289),
  primary: Color(0xFF7FBBB3),
  onPrimary: Color(0xFF2D353B),
  red: Color(0xFFE67E80),
  green: Color(0xFFA7C080),
  yellow: Color(0xFFDBBC7F),
  connection: [
    Color(0xFF7FBBB3),
    Color(0xFF83C092),
    Color(0xFFA7C080),
    Color(0xFFDBBC7F),
    Color(0xFFE69875),
    Color(0xFFE67E80),
    Color(0xFFD699B6),
    Color(0xFF859289),
  ],
);

const _catppuccinLatte = _Palette(
  brightness: Brightness.light,
  bg: Color(0xFFEFF1F5),
  surface: Color(0xFFE6E9EF),
  surfaceHigh: Color(0xFFCCD0DA),
  fg: Color(0xFF4C4F69),
  fgDim: Color(0xFF8C8FA1),
  primary: Color(0xFF1E66F5),
  onPrimary: Color(0xFFEFF1F5),
  red: Color(0xFFD20F39),
  green: Color(0xFF40A02B),
  yellow: Color(0xFFDF8E1D),
  connection: [
    Color(0xFF1E66F5),
    Color(0xFF179299),
    Color(0xFF8839EF),
    Color(0xFF40A02B),
    Color(0xFFDF8E1D),
    Color(0xFFD20F39),
    Color(0xFFFE640B),
    Color(0xFFEA76CB),
  ],
);

// ---------------------------------------------------------------------------
// Light twins — added so every family is a dark/light pair. Five carry the
// canonical upstream light palette (verified against the project's own spec);
// Nord/Monokai/Iron have no official light, so they're hand-tuned twins.
// ---------------------------------------------------------------------------

/// Tokyo Night Day — the official light variant of Tokyo Night
/// (folke/tokyonight.nvim, `tokyonight-day`).
const _tokyoNightDay = _Palette(
  brightness: Brightness.light,
  bg: Color(0xFFE1E2E7),
  surface: Color(0xFFD0D5E3),
  surfaceHigh: Color(0xFFC4C8DA),
  fg: Color(0xFF3760BF),
  fgDim: Color(0xFF848CB5),
  primary: Color(0xFF2E7DE9),
  onPrimary: Color(0xFFE1E2E7),
  red: Color(0xFFC64343),
  green: Color(0xFF587539),
  yellow: Color(0xFFB15C00),
  connection: [
    Color(0xFF2E7DE9),
    Color(0xFF007197),
    Color(0xFF9854F1),
    Color(0xFF587539),
    Color(0xFFB15C00),
    Color(0xFFF52A65),
    Color(0xFF118C74),
    Color(0xFFD20065),
  ],
);

/// Alucard — the official Dracula *light* theme (draculatheme.com spec).
const _alucard = _Palette(
  brightness: Brightness.light,
  bg: Color(0xFFFFFBEB),
  surface: Color(0xFFF5F0DC),
  surfaceHigh: Color(0xFFE2DECA),
  fg: Color(0xFF1F1F1F),
  fgDim: Color(0xFF6C664B),
  primary: Color(0xFF644AC9),
  onPrimary: Color(0xFFFFFBEB),
  red: Color(0xFFCB3A2A),
  green: Color(0xFF14710A),
  yellow: Color(0xFF846E15),
  connection: [
    Color(0xFF644AC9),
    Color(0xFF036A96),
    Color(0xFFA3144D),
    Color(0xFF14710A),
    Color(0xFF846E15),
    Color(0xFFA34D14),
    Color(0xFFCB3A2A),
    Color(0xFF6C664B),
  ],
);

/// One Light — Atom's official light syntax theme (atom/one-light-syntax),
/// the light counterpart to One Dark.
const _oneLight = _Palette(
  brightness: Brightness.light,
  bg: Color(0xFFFAFAFA),
  surface: Color(0xFFF0F0F1),
  surfaceHigh: Color(0xFFE5E5E6),
  fg: Color(0xFF383A42),
  fgDim: Color(0xFF696C77),
  primary: Color(0xFF4078F2),
  onPrimary: Color(0xFFFAFAFA),
  red: Color(0xFFE45649),
  green: Color(0xFF50A14F),
  yellow: Color(0xFFC18401),
  connection: [
    Color(0xFF4078F2),
    Color(0xFF0184BC),
    Color(0xFFA626A4),
    Color(0xFF50A14F),
    Color(0xFFC18401),
    Color(0xFFE45649),
    Color(0xFF986801),
    Color(0xFF696C77),
  ],
);

/// Rosé Pine Dawn — the official light variant of Rosé Pine (rose-pine
/// palette, Dawn roles).
const _rosePineDawn = _Palette(
  brightness: Brightness.light,
  bg: Color(0xFFFAF4ED),
  surface: Color(0xFFFFFAF3),
  surfaceHigh: Color(0xFFF2E9E1),
  fg: Color(0xFF464261),
  fgDim: Color(0xFF797593),
  primary: Color(0xFF907AA9),
  onPrimary: Color(0xFFFAF4ED),
  red: Color(0xFFB4637A),
  green: Color(0xFF286983),
  yellow: Color(0xFFEA9D34),
  connection: [
    Color(0xFF907AA9),
    Color(0xFF56949F),
    Color(0xFFB4637A),
    Color(0xFFEA9D34),
    Color(0xFF286983),
    Color(0xFFD7827E),
    Color(0xFF9893A5),
    Color(0xFF797593),
  ],
);

/// Everforest Light (medium contrast) — the official light variant of
/// Everforest (sainnhe/everforest).
const _everforestLight = _Palette(
  brightness: Brightness.light,
  bg: Color(0xFFFDF6E3),
  surface: Color(0xFFF4F0D9),
  surfaceHigh: Color(0xFFEFEBD4),
  fg: Color(0xFF5C6A72),
  fgDim: Color(0xFF939F91),
  primary: Color(0xFF8DA101),
  onPrimary: Color(0xFFFDF6E3),
  red: Color(0xFFF85552),
  green: Color(0xFF8DA101),
  yellow: Color(0xFFDFA000),
  connection: [
    Color(0xFF8DA101),
    Color(0xFF35A77C),
    Color(0xFF3A94C5),
    Color(0xFFDFA000),
    Color(0xFFF57D26),
    Color(0xFFF85552),
    Color(0xFFDF69BA),
    Color(0xFF939F91),
  ],
);

/// Nord has no official light; a hand-built Snow Storm light — Polar Night
/// text on Snow Storm surfaces, Frost blue accent, Aurora semantics deepened
/// where a pale Aurora hue wouldn't read on the light bg.
const _nordLight = _Palette(
  brightness: Brightness.light,
  bg: Color(0xFFECEFF4), // nord6
  surface: Color(0xFFE5E9F0), // nord5
  surfaceHigh: Color(0xFFD8DEE9), // nord4
  fg: Color(0xFF2E3440), // nord0
  fgDim: Color(0xFF4C566A), // nord3
  primary: Color(0xFF5E81AC), // nord10 (the darkest Frost — reads on light)
  onPrimary: Color(0xFFECEFF4),
  red: Color(0xFFBF616A), // nord11
  green: Color(0xFF6A8957), // nord14 deepened
  yellow: Color(0xFFB07D0E), // nord13 deepened (warn)
  connection: [
    Color(0xFF5E81AC),
    Color(0xFF4E8E8C),
    Color(0xFF6E8FB5),
    Color(0xFF6A8957),
    Color(0xFFB07D0E),
    Color(0xFFBF616A),
    Color(0xFF9C6F95),
    Color(0xFFC26E50),
  ],
);

/// Monokai has no canonical light; a hand-built Monokai-on-light — the
/// signature magenta accent with the Monokai hue family deepened to read on a
/// warm off-white.
const _monokaiLight = _Palette(
  brightness: Brightness.light,
  bg: Color(0xFFFAFAF6),
  surface: Color(0xFFF0F0E8),
  surfaceHigh: Color(0xFFE4E3D8),
  fg: Color(0xFF2C2B26),
  fgDim: Color(0xFF6E6B5E),
  primary: Color(0xFFCC1B6B),
  onPrimary: Color(0xFFFAFAF6),
  red: Color(0xFFD12D4B),
  green: Color(0xFF5A9B00),
  yellow: Color(0xFFA8870E),
  connection: [
    Color(0xFFCC1B6B),
    Color(0xFF5A9B00),
    Color(0xFF0089A6),
    Color(0xFFC4790E),
    Color(0xFF7A4FD6),
    Color(0xFFA8870E),
    Color(0xFFC0392B),
    Color(0xFF75715E),
  ],
);

/// The Iron look on light — the iron-link blue (darkened for contrast) on a
/// clean near-white, mirroring the dark Iron's roles.
const _ironLight = _Palette(
  brightness: Brightness.light,
  bg: Color(0xFFEDF0F4),
  surface: Color(0xFFFFFFFF),
  surfaceHigh: Color(0xFFF1F4F8),
  fg: Color(0xFF171B1F),
  fgDim: Color(0xFF586069),
  primary: Color(0xFF2D6CDF),
  onPrimary: Color(0xFFFFFFFF),
  red: Color(0xFFD24B48),
  green: Color(0xFF1F9D5F),
  yellow: Color(0xFFB5760E),
  connection: [
    Color(0xFF2D6CDF),
    Color(0xFF0E8C9A),
    Color(0xFF7A45C0),
    Color(0xFF1F9D5F),
    Color(0xFFB5760E),
    Color(0xFFD24B48),
    Color(0xFF5160B8),
    Color(0xFFC23F86),
  ],
);
