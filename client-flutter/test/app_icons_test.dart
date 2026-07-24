import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:iron_link_flutter/src/platform/app_icons.dart';

void main() {
  late Directory root;
  late Directory apps;
  late Directory icons;
  late Directory pixmaps;

  setUp(() {
    // A self-contained fake of the freedesktop layout under one temp root, so
    // the resolver runs identically on Linux and on non-Linux CI runners.
    root = Directory.systemTemp.createTempSync('app_icons_test');
    apps = Directory('${root.path}/applications')..createSync(recursive: true);
    icons = Directory('${root.path}/icons')..createSync(recursive: true);
    pixmaps = Directory('${root.path}/pixmaps')..createSync(recursive: true);
  });

  tearDown(() => root.deleteSync(recursive: true));

  /// Builds a resolver wired to the temp roots with Linux behavior forced on,
  /// so the test exercises the real algorithm everywhere.
  AppIconResolver makeResolver() => AppIconResolver(
        applicationDirs: [apps.path],
        iconThemeDirs: [icons.path],
        pixmapDirs: [pixmaps.path],
        isLinux: true,
      );

  File writeIcon(String relative) {
    final f = File('${icons.path}/$relative');
    f.parent.createSync(recursive: true);
    f.writeAsBytesSync(const [0x89, 0x50, 0x4E, 0x47]); // PNG magic, enough
    return f;
  }

  test('resolves via the .desktop Exec→Icon mapping (name != binary)', () {
    // Telegram: Exec basename `Telegram` but Icon is reverse-DNS — proving the
    // lookup goes through the .desktop file, not the binary name.
    File('${apps.path}/org.telegram.desktop.desktop').writeAsStringSync(
      '[Desktop Entry]\n'
      'Exec=Telegram -- %U\n'
      'Icon=org.telegram.desktop\n'
      'StartupWMClass=TelegramDesktop\n',
    );
    final png = writeIcon('hicolor/128x128/apps/org.telegram.desktop.png');

    final resolver = makeResolver();
    expect(
      resolver.resolve('/usr/bin/Telegram'),
      completion(predicate<File?>((f) => f?.path == png.path)),
    );
  });

  test('picks the largest hicolor size available', () {
    File('${apps.path}/org.telegram.desktop.desktop').writeAsStringSync(
      '[Desktop Entry]\nExec=Telegram\nIcon=org.telegram.desktop\n',
    );
    writeIcon('hicolor/48x48/apps/org.telegram.desktop.png');
    final big = writeIcon('hicolor/256x256/apps/org.telegram.desktop.png');
    writeIcon('hicolor/128x128/apps/org.telegram.desktop.png');

    expect(
      makeResolver().resolve('/usr/bin/Telegram'),
      completion(predicate<File?>((f) => f?.path == big.path)),
    );
  });

  test('falls back to the binary basename as an icon name', () {
    // No .desktop entry for `discord`, but a hicolor icon named after the
    // binary — the second lookup path (basename AS icon name) must find it.
    final png = writeIcon('hicolor/256x256/apps/discord.png');
    expect(
      makeResolver().resolve('/usr/bin/discord'),
      completion(predicate<File?>((f) => f?.path == png.path)),
    );
  });

  test('resolves an absolute-path Icon= value pointing at a PNG', () {
    final png = writeIcon('custom/myapp.png');
    File('${apps.path}/myapp.desktop').writeAsStringSync(
      '[Desktop Entry]\nExec=myapp\nIcon=${png.path}\n',
    );
    expect(
      makeResolver().resolve('/opt/myapp/myapp'),
      completion(predicate<File?>((f) => f?.path == png.path)),
    );
  });

  test('resolves via /usr/share/pixmaps as a last resort', () {
    final png = File('${pixmaps.path}/kitty.png')
      ..writeAsBytesSync(const [0x89, 0x50, 0x4E, 0x47]);
    File('${apps.path}/kitty.desktop').writeAsStringSync(
      '[Desktop Entry]\nExec=kitty\nIcon=kitty\n',
    );
    expect(
      makeResolver().resolve('/usr/bin/kitty'),
      completion(predicate<File?>((f) => f?.path == png.path)),
    );
  });

  test('NoDisplay=true entries are skipped', () {
    writeIcon('hicolor/128x128/apps/ghost.png');
    File('${apps.path}/ghost.desktop').writeAsStringSync(
      '[Desktop Entry]\nExec=ghost\nIcon=ghost\nNoDisplay=true\n',
    );
    // The .desktop is ignored; but the binary-basename fallback still finds the
    // PNG named `ghost`, so resolution succeeds via that second path. Use a
    // binary whose basename differs from the icon to prove the skip.
    File('${apps.path}/hidden.desktop').writeAsStringSync(
      '[Desktop Entry]\nExec=hiddenbin\nIcon=ghost\nNoDisplay=true\n',
    );
    expect(
      makeResolver().resolve('/usr/bin/hiddenbin'),
      completion(isNull),
    );
  });

  test('unknown path resolves to null', () {
    expect(
      makeResolver().resolve('/usr/bin/does-not-exist-anywhere'),
      completion(isNull),
    );
  });

  test('token fallback: a wrapper binary resolves to the base app, not a PWA '
      'or a sibling', () {
    // The process is /opt/google/chrome/chrome (basename `chrome`), but the
    // real .desktop's Exec is the /usr/bin/google-chrome-stable wrapper — so
    // no EXACT key matches `chrome`; only the token fallback can.
    File('${apps.path}/google-chrome.desktop').writeAsStringSync(
      '[Desktop Entry]\n'
      'Exec=/usr/bin/google-chrome-stable %U\n'
      'Icon=google-chrome\n'
      'StartupWMClass=Google-chrome\n',
    );
    final chromePng = writeIcon('hicolor/256x256/apps/google-chrome.png');
    // A per-site PWA whose stem ALSO carries a `chrome` token + its own icon —
    // its longer key must lose to the base app's shorter `google-chrome`.
    File('${apps.path}/chrome-abcdef-Default.desktop').writeAsStringSync(
      '[Desktop Entry]\n'
      'Exec=/opt/google/chrome/google-chrome --app-id=abcdef\n'
      'Icon=chrome-abcdef-Default\n',
    );
    writeIcon('hicolor/256x256/apps/chrome-abcdef-Default.png');
    // Chromium must NOT be matched by `chrome` — `chromium` has no `chrome`
    // token, so the equality-of-token rule excludes it.
    File('${apps.path}/chromium.desktop').writeAsStringSync(
      '[Desktop Entry]\nExec=chromium\nIcon=chromium\n',
    );
    writeIcon('hicolor/256x256/apps/chromium.png');

    expect(
      makeResolver().resolve('/opt/google/chrome/chrome'),
      completion(predicate<File?>((f) => f?.path == chromePng.path)),
    );
  });

  test('the .desktop database is consulted only when the Linux path is on', () {
    // With both platform paths off, the freedesktop index is never read even
    // though a matching icon exists (Windows uses PE extraction, not this).
    final png = writeIcon('hicolor/128x128/apps/org.telegram.desktop.png');
    File('${apps.path}/org.telegram.desktop.desktop').writeAsStringSync(
      '[Desktop Entry]\nExec=Telegram\nIcon=org.telegram.desktop\n',
    );
    expect(png.existsSync(), isTrue);
    final resolver = AppIconResolver(
      applicationDirs: [apps.path],
      iconThemeDirs: [icons.path],
      pixmapDirs: [pixmaps.path],
      isLinux: false,
      isWindows: false,
    );
    expect(resolver.resolve('/usr/bin/Telegram'), completion(isNull));
  });
}
