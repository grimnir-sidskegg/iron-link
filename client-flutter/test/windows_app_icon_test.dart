import 'dart:typed_data';

import 'package:flutter_test/flutter_test.dart';
import 'package:iron_link_flutter/src/platform/windows_app_icon.dart';

/// Unit tests for the platform-agnostic byte transforms behind Windows PE icon
/// extraction. These craft the exact on-disk structures (GRPICONDIR, an RT_ICON
/// DIB) and run on any OS — the end-to-end PE walk is verified on Windows.
void main() {
  group('isPngBytes', () {
    test('detects the PNG signature', () {
      expect(
        isPngBytes(Uint8List.fromList(
            [0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00])),
        isTrue,
      );
    });

    test('rejects non-PNG and too-short input', () {
      expect(isPngBytes(Uint8List.fromList([0x42, 0x4D, 0, 0])), isFalse);
      expect(isPngBytes(Uint8List.fromList([0x89, 0x50])), isFalse);
    });
  });

  group('pickBestGroupIcon', () {
    /// Builds a GRPICONDIR from (width, bitCount, id) triples; width 0 = 256.
    Uint8List groupDir(List<(int, int, int)> entries) {
      final b = BytesBuilder();
      final head = ByteData(6)
        ..setUint16(0, 0, Endian.little) // reserved
        ..setUint16(2, 1, Endian.little) // type: icon
        ..setUint16(4, entries.length, Endian.little);
      b.add(head.buffer.asUint8List());
      for (final (w, bits, id) in entries) {
        final e = ByteData(14)
          ..setUint8(0, w) // bWidth
          ..setUint8(1, w) // bHeight
          ..setUint16(6, bits, Endian.little) // wBitCount
          ..setUint32(8, 100, Endian.little) // dwBytesInRes
          ..setUint16(12, id, Endian.little); // nID
        b.add(e.buffer.asUint8List());
      }
      return b.toBytes();
    }

    test('picks the largest entry (0 width means 256)', () {
      // 32px(id 1), 256px(id 2), 48px(id 3) → the 256px wins.
      expect(pickBestGroupIcon(groupDir([(32, 8, 1), (0, 32, 2), (48, 8, 3)])),
          equals(2));
    });

    test('breaks ties on colour depth', () {
      expect(pickBestGroupIcon(groupDir([(48, 8, 1), (48, 32, 2)])), equals(2));
    });

    test('returns null for an empty/short directory', () {
      expect(pickBestGroupIcon(Uint8List(0)), isNull);
      expect(pickBestGroupIcon(groupDir(const [])), isNull);
    });
  });

  group('dibIconToRgba', () {
    /// A 32-bpp BI_RGB icon DIB (no AND mask needed when alpha is present).
    /// [bgra] is bottom-up rows of w*h BGRA quads.
    Uint8List dib(int w, int h, List<int> bgra) {
      final header = ByteData(40)
        ..setUint32(0, 40, Endian.little) // biSize
        ..setInt32(4, w, Endian.little) // biWidth
        ..setInt32(8, h * 2, Endian.little) // biHeight (doubled: XOR + mask)
        ..setUint16(12, 1, Endian.little) // biPlanes
        ..setUint16(14, 32, Endian.little) // biBitCount
        ..setUint32(16, 0, Endian.little); // biCompression = BI_RGB
      return Uint8List.fromList([...header.buffer.asUint8List(), ...bgra]);
    }

    test('converts BGRA bottom-up to RGBA top-down', () {
      // 1x2 image. Bottom-up storage: file row 0 = bottom (y=1), row 1 = top.
      final bytes = dib(1, 2, [
        10, 20, 30, 255, // y=1 (bottom): B G R A
        12, 22, 32, 255, // y=0 (top)
      ]);
      final out = dibIconToRgba(bytes);
      expect(out, isNotNull);
      final (rgba, w, h) = out!;
      expect((w, h), equals((1, 2)));
      // Top row first, channels swapped to RGBA.
      expect(rgba.sublist(0, 4), equals([32, 22, 12, 255])); // y=0
      expect(rgba.sublist(4, 8), equals([30, 20, 10, 255])); // y=1
    });

    test('reconstructs alpha from the AND mask when colour alpha is all zero',
        () {
      // 1x1, colour alpha 0; append a 1-bpp AND mask row (padded to 4 bytes)
      // with the top bit SET → that pixel is transparent.
      final maskRow = Uint8List(4)..[0] = 0x80;
      final bytes = Uint8List.fromList([
        ...dib(1, 1, [50, 60, 70, 0]),
        ...maskRow,
      ]);
      final out = dibIconToRgba(bytes);
      expect(out, isNotNull);
      expect(out!.$1[3], equals(0)); // mask bit set → alpha 0
    });

    test('rejects unsupported (non-32bpp / compressed) DIBs', () {
      final paletted = ByteData(40)
        ..setUint32(0, 40, Endian.little)
        ..setInt32(4, 16, Endian.little)
        ..setInt32(8, 32, Endian.little)
        ..setUint16(14, 8, Endian.little) // 8-bpp — unsupported
        ..setUint32(16, 0, Endian.little);
      expect(dibIconToRgba(paletted.buffer.asUint8List()), isNull);
      expect(dibIconToRgba(Uint8List(10)), isNull); // too short
    });
  });
}
