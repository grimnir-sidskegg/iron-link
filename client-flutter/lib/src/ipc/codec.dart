/// The iron-link frame codec: a 4-byte big-endian length prefix followed
/// by a JSON body, 16 MiB cap in BOTH directions — identical to
/// `daemon-go/internal/ipc`. (The cap on the read side is the client's
/// job on both sides; a corrupt prefix must not become a 4 GiB
/// allocation.)
library;

import 'dart:convert';
import 'dart:typed_data';

/// Maximum frame body size, mirroring the Go codec's cap.
const int maxFrameBytes = 16 * 1024 * 1024;

/// Thrown when a frame breaks the codec: an over-cap length prefix or a
/// non-object JSON body.
class CodecException implements Exception {
  CodecException(this.message);

  final String message;

  @override
  String toString() => 'CodecException: $message';
}

/// Encodes one frame: length prefix + UTF-8 JSON.
Uint8List encodeFrame(Map<String, Object?> body) {
  final payload = utf8.encode(jsonEncode(body));
  if (payload.length > maxFrameBytes) {
    throw CodecException('frame of ${payload.length} bytes exceeds the '
        '$maxFrameBytes-byte cap');
  }
  final out = Uint8List(4 + payload.length);
  ByteData.sublistView(out).setUint32(0, payload.length);
  out.setAll(4, payload);
  return out;
}

/// Incremental frame decoder: feed raw socket chunks, get decoded JSON
/// bodies. Carries partial frames across chunks.
class FrameDecoder {
  final BytesBuilder _buf = BytesBuilder(copy: true);

  /// Consumes [chunk] and returns every frame completed by it (possibly
  /// none, possibly several).
  List<Map<String, Object?>> add(List<int> chunk) {
    _buf.add(chunk);
    final frames = <Map<String, Object?>>[];
    var bytes = _buf.toBytes();
    var offset = 0;
    while (bytes.length - offset >= 4) {
      final len = ByteData.sublistView(bytes, offset, offset + 4).getUint32(0);
      if (len > maxFrameBytes) {
        throw CodecException('frame length $len exceeds the '
            '$maxFrameBytes-byte cap');
      }
      if (bytes.length - offset - 4 < len) break;
      final body = utf8.decode(bytes.sublist(offset + 4, offset + 4 + len));
      final decoded = jsonDecode(body);
      if (decoded is! Map<String, Object?>) {
        throw CodecException('frame body is not a JSON object');
      }
      frames.add(decoded);
      offset += 4 + len;
    }
    _buf.clear();
    if (offset < bytes.length) {
      _buf.add(bytes.sublist(offset));
    }
    return frames;
  }
}

/// Transforms a raw byte stream (a socket) into a stream of decoded frame
/// bodies. Codec violations error the stream.
Stream<Map<String, Object?>> decodeFrames(Stream<List<int>> source) async* {
  final decoder = FrameDecoder();
  await for (final chunk in source) {
    yield* Stream.fromIterable(decoder.add(chunk));
  }
}
