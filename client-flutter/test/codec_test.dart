import 'dart:typed_data';

import 'package:flutter_test/flutter_test.dart';
import 'package:iron_link_flutter/src/ipc/codec.dart';

void main() {
  test('encode/decode round-trip', () {
    final frame = encodeFrame({'command': 'status'});
    expect(frame.sublist(0, 4), [0, 0, 0, frame.length - 4]);
    final decoded = FrameDecoder().add(frame);
    expect(decoded, [
      {'command': 'status'}
    ]);
  });

  test('frames split across chunks reassemble', () {
    final frame = encodeFrame({'event': 'traffic', 'up': 1, 'down': 2});
    final decoder = FrameDecoder();
    expect(decoder.add(frame.sublist(0, 3)), isEmpty);
    expect(decoder.add(frame.sublist(3, 7)), isEmpty);
    final decoded = decoder.add(frame.sublist(7));
    expect(decoded.single['up'], 1);
  });

  test('multiple frames in one chunk all decode', () {
    final chunk = BytesBuilder()
      ..add(encodeFrame({'event': 'state'}))
      ..add(encodeFrame({'event': 'traffic', 'up': 0, 'down': 0}));
    final decoded = FrameDecoder().add(chunk.toBytes());
    expect(decoded, hasLength(2));
    expect(decoded[1]['event'], 'traffic');
  });

  test('over-cap length prefix is rejected before allocating', () {
    final hostile = Uint8List(4)
      ..buffer.asByteData().setUint32(0, maxFrameBytes + 1);
    expect(() => FrameDecoder().add(hostile), throwsA(isA<CodecException>()));
  });

  test('non-object body is rejected', () {
    final payload = Uint8List.fromList('[1,2,3]'.codeUnits);
    final frame = Uint8List(4 + payload.length);
    ByteData.sublistView(frame).setUint32(0, payload.length);
    frame.setAll(4, payload);
    expect(() => FrameDecoder().add(frame), throwsA(isA<CodecException>()));
  });

  test('utf-8 payload survives the round trip', () {
    final decoded =
        FrameDecoder().add(encodeFrame({'message': 'ノード — Grímnïr ✓'}));
    expect(decoded.single['message'], 'ノード — Grímnïr ✓');
  });
}
