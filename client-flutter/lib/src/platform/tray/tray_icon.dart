/// Renders the tray icon: the app emblem with a small state-colored status dot
/// in the bottom-right corner (grey = off, amber = connecting, green = up), at
/// a couple of sizes. Produces platform-neutral straight-RGBA bitmaps; each
/// backend converts (Linux → ARGB32 big-endian `a(iiay)`, Windows → BGRA HICON).
library;

import 'dart:typed_data';
import 'dart:ui' as ui;

import 'package:flutter/services.dart' show rootBundle;

/// One rendered icon size: [size]×[size] straight (non-premultiplied) RGBA,
/// [rgba] length == size*size*4, byte order R,G,B,A.
class TrayBitmap {
  const TrayBitmap(this.size, this.rgba);

  final int size;
  final Uint8List rgba;
}

class TrayIconRenderer {
  TrayIconRenderer._(this._emblem);

  final ui.Image _emblem;

  /// Sizes (px) rendered. Panels are typically 16–24 px; 22 covers the common
  /// case crisply, 44 covers HiDPI / taller bars.
  static const _sizes = [22, 44];

  /// Decodes the bundled emblem once. Call after the Flutter binding is up.
  static Future<TrayIconRenderer> load() async {
    final data = await rootBundle.load('assets/logo-emblem.png');
    final codec = await ui.instantiateImageCodec(data.buffer.asUint8List());
    final frame = await codec.getNextFrame();
    return TrayIconRenderer._(frame.image);
  }

  /// Renders the emblem + a [dot]-colored status dot at every [_sizes] entry.
  Future<List<TrayBitmap>> render(ui.Color dot) async {
    final out = <TrayBitmap>[];
    for (final size in _sizes) {
      out.add(await _render(size, dot));
    }
    return out;
  }

  Future<TrayBitmap> _render(int size, ui.Color dot) async {
    final s = size.toDouble();
    final recorder = ui.PictureRecorder();
    final canvas = ui.Canvas(recorder);
    canvas.drawImageRect(
      _emblem,
      ui.Rect.fromLTWH(0, 0, _emblem.width.toDouble(), _emblem.height.toDouble()),
      ui.Rect.fromLTWH(0, 0, s, s),
      ui.Paint()..filterQuality = ui.FilterQuality.medium,
    );
    // Status dot with a dark ring so it reads on light or dark panels.
    final r = s * 0.30;
    final c = ui.Offset(s - r - s * 0.04, s - r - s * 0.04);
    canvas.drawCircle(c, r + s * 0.07, ui.Paint()..color = const ui.Color(0xFF0E1116));
    canvas.drawCircle(c, r, ui.Paint()..color = dot);

    final image = await recorder.endRecording().toImage(size, size);
    final bytes = await image.toByteData(format: ui.ImageByteFormat.rawStraightRgba);
    image.dispose();
    if (bytes == null) {
      throw StateError('tray icon raster (${size}px) returned no bytes');
    }
    return TrayBitmap(size, bytes.buffer.asUint8List());
  }

  void dispose() => _emblem.dispose();
}
