/// Pure-Dart extraction of a Windows app's embedded icon from its PE (`.exe`).
///
/// The Traffic tab knows each app only by the owning process's executable path,
/// which on Windows is the full `C:\...\app.exe` (sing-box resolves it via
/// QueryFullProcessImageName and the daemon forwards it verbatim). To show the
/// real icon we read the PE's resource section directly — no `package:win32`,
/// no FFI (owner decision: hand-FFI only where unavoidable; this is avoidable),
/// and no native image step, so it runs and unit-tests on any platform:
///
///   PE headers → `.rsrc` section → resource tree → RT_GROUP_ICON (the icon
///   directory) → pick the largest entry → its RT_ICON image.
///
/// An RT_ICON image is either a PNG blob (Vista+ large icons — returned as-is,
/// crisp up to 256px) or a 32-bpp BI_RGB DIB (older entries — converted to RGBA
/// and PNG-encoded via `dart:ui`). Anything else (palettised/compressed DIBs,
/// unreadable or malformed files) yields null and the caller falls back to the
/// letter-avatar — this is best-effort, never throws into the UI.
///
/// Only bounded regions are read (headers + the `.rsrc` section, typically tens
/// of KB), never the whole binary — game `.exe`s can be hundreds of MB.
library;

import 'dart:async';
import 'dart:io';
import 'dart:ui' as ui;

import 'package:flutter/foundation.dart';

/// Resource type ids (winuser.h): a single icon image and the icon directory.
const int _rtIcon = 3;
const int _rtGroupIcon = 14;

/// The largest icon section we will read into memory — a guard against a
/// corrupt/hostile `SizeOfRawData`; real `.rsrc` sections are far smaller.
const int _maxRsrcBytes = 64 * 1024 * 1024;

/// Extracts [exePath]'s embedded application icon and returns it as PNG bytes,
/// or null when none can be produced (no icon, unreadable file, unsupported
/// format, malformed PE). Never throws — every failure is a null.
Future<Uint8List?> extractWindowsExeIconPng(String exePath) async {
  RandomAccessFile? raf;
  try {
    raf = await File(exePath).open();
    final fileLen = await raf.length();
    final reader = raf;

    Future<ByteData> read(int off, int len) async {
      if (off < 0 || len <= 0 || off + len > fileLen) {
        throw const FormatException('read out of range');
      }
      await reader.setPosition(off);
      final b = await reader.read(len);
      if (b.length != len) throw const FormatException('short read');
      return ByteData.sublistView(b);
    }

    // DOS header: 'MZ' then e_lfanew (PE header file offset) at 0x3C.
    final dos = await read(0, 64);
    if (dos.getUint16(0, Endian.little) != 0x5A4D) return null;
    final peOff = dos.getUint32(0x3C, Endian.little);

    // PE signature 'PE\0\0' + the 20-byte COFF file header.
    final coff = await read(peOff, 24);
    if (coff.getUint32(0, Endian.little) != 0x00004550) return null;
    final numSections = coff.getUint16(6, Endian.little);
    final optSize = coff.getUint16(20, Endian.little);
    if (numSections == 0 || numSections > 96) return null;

    // The section table follows the optional header; find `.rsrc`.
    final secTableOff = peOff + 24 + optSize;
    final sec = await read(secTableOff, numSections * 40);
    int? rsrcVA, rsrcPtr, rsrcSize;
    for (var i = 0; i < numSections; i++) {
      final base = i * 40;
      if (_sectionName(sec, base) != '.rsrc') continue;
      rsrcVA = sec.getUint32(base + 12, Endian.little);
      rsrcSize = sec.getUint32(base + 16, Endian.little); // SizeOfRawData
      rsrcPtr = sec.getUint32(base + 20, Endian.little); // PointerToRawData
      break;
    }
    if (rsrcVA == null || rsrcPtr == null || rsrcSize == null) return null;
    if (rsrcSize == 0 || rsrcSize > _maxRsrcBytes) return null;

    // The whole resource section, parsed in memory. Resource-tree entry offsets
    // are relative to this block; final data RVAs convert via (rva - rsrcVA).
    final blockData = await read(rsrcPtr, rsrcSize);
    final block = blockData.buffer.asUint8List(blockData.offsetInBytes, rsrcSize);

    // RT_GROUP_ICON leaf → the icon directory → the best entry's icon id.
    final grp = _resourceData(blockData, block, rsrcVA, _rtGroupIcon, null);
    if (grp == null) return null;
    final iconId = pickBestGroupIcon(grp);
    if (iconId == null) return null;

    // RT_ICON leaf for that id → the actual image (PNG blob or DIB).
    final img = _resourceData(blockData, block, rsrcVA, _rtIcon, iconId);
    if (img == null) return null;

    if (isPngBytes(img)) return Uint8List.fromList(img);
    final rgba = dibIconToRgba(img);
    if (rgba == null) return null;
    return await _rgbaToPng(rgba.$1, rgba.$2, rgba.$3);
  } catch (_) {
    return null; // best-effort: any malformed/unreadable PE → letter avatar
  } finally {
    try {
      await raf?.close();
    } catch (_) {}
  }
}

/// The NUL-terminated 8-byte section name at [base] in the section table.
String _sectionName(ByteData d, int base) {
  final sb = StringBuffer();
  for (var i = 0; i < 8; i++) {
    final c = d.getUint8(base + i);
    if (c == 0) break;
    sb.writeCharCode(c);
  }
  return sb.toString();
}

/// Walks the three-level resource tree (type → name/id → language) and returns
/// the bytes of the leaf's data, or null if any level is missing/out of range.
/// At the type level it matches [typeId]; at the name level it matches [matchId]
/// when given (RT_ICON's id) else takes the first entry (RT_GROUP_ICON); at the
/// language level it takes the first entry.
Uint8List? _resourceData(
    ByteData d, Uint8List block, int rsrcVA, int typeId, int? matchId) {
  final len = block.length;
  final typeDir = _subdirOffset(d, 0, typeId, len);
  if (typeDir == null) return null;
  final nameDir = matchId == null
      ? _firstSubdir(d, typeDir, len)
      : _subdirOffset(d, typeDir, matchId, len);
  if (nameDir == null) return null;
  final leaf = _firstEntryRaw(d, nameDir, len);
  if (leaf == null) return null;
  final dataEntry = leaf & 0x7FFFFFFF; // leaf: high bit clear
  if (dataEntry + 16 > len) return null;

  // IMAGE_RESOURCE_DATA_ENTRY: OffsetToData (an RVA), then Size.
  final dataRva = d.getUint32(dataEntry, Endian.little);
  final size = d.getUint32(dataEntry + 4, Endian.little);
  final start = dataRva - rsrcVA;
  if (start < 0 || size == 0 || start + size > len) return null;
  return Uint8List.sublistView(block, start, start + size);
}

/// Offset (relative to the section) of the subdirectory the id-[id] entry at
/// directory [dirOff] points to, or null if absent / not a subdirectory.
int? _subdirOffset(ByteData d, int dirOff, int id, int len) {
  final raw = _entryByIdRaw(d, dirOff, id, len);
  if (raw == null || raw & 0x80000000 == 0) return null;
  return raw & 0x7FFFFFFF;
}

/// Offset of the subdirectory the FIRST entry of [dirOff] points to.
int? _firstSubdir(ByteData d, int dirOff, int len) {
  final raw = _firstEntryRaw(d, dirOff, len);
  if (raw == null || raw & 0x80000000 == 0) return null;
  return raw & 0x7FFFFFFF;
}

/// The raw OffsetToData word of the id-[id] entry in the directory at [dirOff],
/// or null. IMAGE_RESOURCE_DIRECTORY is 16 bytes (named count at +12, id count
/// at +14) followed by 8-byte entries; named entries (high bit of the name set)
/// are skipped — icons are keyed by integer id.
int? _entryByIdRaw(ByteData d, int dirOff, int id, int len) {
  if (dirOff < 0 || dirOff + 16 > len) return null;
  final named = d.getUint16(dirOff + 12, Endian.little);
  final ids = d.getUint16(dirOff + 14, Endian.little);
  final entries = dirOff + 16;
  for (var i = 0; i < named + ids; i++) {
    final e = entries + i * 8;
    if (e + 8 > len) return null;
    final nameOrId = d.getUint32(e, Endian.little);
    if (nameOrId & 0x80000000 != 0) continue; // named entry
    if ((nameOrId & 0xFFFF) == id) return d.getUint32(e + 4, Endian.little);
  }
  return null;
}

/// The raw OffsetToData word of the directory's first entry (any name/id), or
/// null when the directory is empty / out of range.
int? _firstEntryRaw(ByteData d, int dirOff, int len) {
  if (dirOff < 0 || dirOff + 16 > len) return null;
  final count = d.getUint16(dirOff + 12, Endian.little) +
      d.getUint16(dirOff + 14, Endian.little);
  if (count == 0) return null;
  final e = dirOff + 16;
  if (e + 8 > len) return null;
  return d.getUint32(e + 4, Endian.little);
}

/// Picks the best image id from a GRPICONDIR ([grp]): the largest dimension
/// (a 0 width/height byte means 256), tie-broken by colour depth. Returns the
/// chosen entry's RT_ICON id, or null if the directory is empty/too short.
///
/// GRPICONDIR = idReserved(2) idType(2) idCount(2), then idCount GRPICONDIRENTRY
/// of 14 bytes: bWidth(1) bHeight(1) bColorCount(1) bReserved(1) wPlanes(2)
/// wBitCount(2) dwBytesInRes(4) nID(2).
@visibleForTesting
int? pickBestGroupIcon(Uint8List grp) {
  if (grp.length < 6) return null;
  final d = ByteData.sublistView(grp);
  final count = d.getUint16(4, Endian.little);
  int? bestId;
  var bestScore = -1;
  for (var i = 0; i < count; i++) {
    final e = 6 + i * 14;
    if (e + 14 > grp.length) break;
    final w = d.getUint8(e) == 0 ? 256 : d.getUint8(e);
    final bitCount = d.getUint16(e + 6, Endian.little);
    final id = d.getUint16(e + 12, Endian.little);
    final score = w * 1000 + bitCount; // larger first, then deeper colour
    if (score > bestScore) {
      bestScore = score;
      bestId = id;
    }
  }
  return bestId;
}

/// The 8-byte PNG signature — Vista+ icon entries embed a PNG directly.
@visibleForTesting
bool isPngBytes(Uint8List b) =>
    b.length >= 8 &&
    b[0] == 0x89 &&
    b[1] == 0x50 &&
    b[2] == 0x4E &&
    b[3] == 0x47 &&
    b[4] == 0x0D &&
    b[5] == 0x0A &&
    b[6] == 0x1A &&
    b[7] == 0x0A;

/// Converts a 32-bpp BI_RGB icon DIB to top-down RGBA, or null for any other
/// format (palettised/compressed DIBs are not supported — letter-avatar then).
///
/// An RT_ICON DIB is a BITMAPINFOHEADER whose biHeight is DOUBLED: the XOR
/// colour bitmap then a 1-bpp AND mask, both bottom-up. When the colour data
/// carries no alpha (all zero — common for icons authored as XOR+mask), alpha
/// is reconstructed from the AND mask (set bit = transparent).
@visibleForTesting
(Uint8List, int, int)? dibIconToRgba(Uint8List dib) {
  if (dib.length < 40) return null;
  final d = ByteData.sublistView(dib);
  final headerSize = d.getUint32(0, Endian.little);
  if (headerSize < 40) return null;
  final w = d.getInt32(4, Endian.little);
  final hRaw = d.getInt32(8, Endian.little);
  final bitCount = d.getUint16(14, Endian.little);
  final compression = d.getUint32(16, Endian.little);
  if (bitCount != 32 || compression != 0) return null; // 32-bpp BI_RGB only
  final h = hRaw ~/ 2; // XOR bitmap + AND mask
  if (w <= 0 || h <= 0 || w > 1024 || h > 1024) return null;

  final pixels = headerSize; // 32-bpp BI_RGB has no colour table
  final rowBytes = w * 4;
  if (pixels + rowBytes * h > dib.length) return null;

  final rgba = Uint8List(w * h * 4);
  var anyAlpha = false;
  for (var y = 0; y < h; y++) {
    final src = pixels + (h - 1 - y) * rowBytes; // stored bottom-up
    final dst = y * rowBytes;
    for (var x = 0; x < w; x++) {
      final s = src + x * 4;
      final o = dst + x * 4;
      rgba[o] = dib[s + 2]; // R ← BGRA
      rgba[o + 1] = dib[s + 1]; // G
      rgba[o + 2] = dib[s]; // B
      final a = dib[s + 3];
      rgba[o + 3] = a;
      if (a != 0) anyAlpha = true;
    }
  }

  if (!anyAlpha) {
    // Reconstruct alpha from the trailing 1-bpp AND mask (rows padded to a
    // 32-bit boundary, bottom-up). If it isn't there, treat as fully opaque.
    final maskRow = ((w + 31) ~/ 32) * 4;
    final maskOff = pixels + rowBytes * h;
    if (maskOff + maskRow * h <= dib.length) {
      for (var y = 0; y < h; y++) {
        final src = maskOff + (h - 1 - y) * maskRow;
        for (var x = 0; x < w; x++) {
          final bit = (dib[src + (x >> 3)] >> (7 - (x & 7))) & 1;
          rgba[(y * w + x) * 4 + 3] = bit == 1 ? 0 : 255;
        }
      }
    } else {
      for (var i = 3; i < rgba.length; i += 4) {
        rgba[i] = 255;
      }
    }
  }
  return (rgba, w, h);
}

/// Encodes top-down RGBA pixels to PNG via `dart:ui` (runs on the UI isolate —
/// the engine owns the codec). Null if the engine fails to encode.
Future<Uint8List?> _rgbaToPng(Uint8List rgba, int w, int h) async {
  final completer = Completer<ui.Image>();
  ui.decodeImageFromPixels(
      rgba, w, h, ui.PixelFormat.rgba8888, completer.complete);
  final image = await completer.future;
  try {
    final data = await image.toByteData(format: ui.ImageByteFormat.png);
    return data?.buffer.asUint8List();
  } finally {
    image.dispose();
  }
}
