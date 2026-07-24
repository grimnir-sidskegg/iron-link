/// Unit tests for [DaemonClient.testLatencyEach] — the per-node fan-out that
/// makes each node's latency INDEPENDENT (a slow/hung node delays only its own
/// result, never the others). Driven over fakes that override the single-node
/// [DaemonClient.testLatency] with controlled timing/outcomes — no sockets, no
/// daemon.
library;

import 'dart:math';

import 'package:flutter_test/flutter_test.dart';
import 'package:iron_link_flutter/src/ipc/client.dart';
import 'package:iron_link_flutter/src/wire/wire.dart';

/// Overrides the single-node probe with a per-name delay (ms); the reported
/// latency echoes the delay. Tracks the peak number of overlapping probes.
class _DelayClient extends DaemonClient {
  _DelayClient(this.delaysMs) : super(endpoint: '/dev/null');

  final Map<String, int> delaysMs;
  int inFlight = 0;
  int maxInFlight = 0;

  @override
  Future<List<LatencyResult>> testLatency([List<String>? nodes]) async {
    final name = nodes!.single;
    final ms = delaysMs[name] ?? 0;
    inFlight++;
    maxInFlight = max(maxInFlight, inFlight);
    await Future<void>.delayed(Duration(milliseconds: ms));
    inFlight--;
    return [LatencyResult(node: name, latencyMs: ms)];
  }
}

class _ErrClient extends DaemonClient {
  _ErrClient() : super(endpoint: '/dev/null');

  @override
  Future<List<LatencyResult>> testLatency([List<String>? nodes]) async {
    final name = nodes!.single;
    if (name == 'bad') throw const DaemonError('node vanished');
    return [LatencyResult(node: name, latencyMs: 42)];
  }
}

class _DownClient extends DaemonClient {
  _DownClient() : super(endpoint: '/dev/null');

  @override
  Future<List<LatencyResult>> testLatency([List<String>? nodes]) async =>
      throw const DaemonUnreachable('/dev/null', 'connection refused');
}

void main() {
  test('a slow node does not delay a fast node — the fast result emits first',
      () async {
    // Input order lists the SLOW node first; the old all-at-once batch would
    // have surfaced them together (gated on the slow one). The fan-out must
    // emit the fast node's result before the slow node's.
    final client = _DelayClient({'slow': 120, 'fast': 5});
    final order = <String>[];
    await for (final r
        in client.testLatencyEach(['slow', 'fast'], concurrency: 2)) {
      order.add(r.node);
    }
    expect(order.first, 'fast');
    expect(order.toSet(), {'slow', 'fast'});
  });

  test('the fan-out never exceeds `concurrency` probes in flight', () async {
    final names = ['a', 'b', 'c', 'd', 'e'];
    final client = _DelayClient({for (final n in names) n: 10});
    final seen = <String>[];
    await for (final r in client.testLatencyEach(names, concurrency: 2)) {
      seen.add(r.node);
    }
    expect(client.maxInFlight, lessThanOrEqualTo(2));
    expect(seen.toSet(), names.toSet());
  });

  test('a per-node error yields a null latency, not a thrown sweep', () async {
    final client = _ErrClient();
    final byName = {
      for (final r in await client.testLatencyEach(['ok', 'bad']).toList())
        r.node: r.latencyMs,
    };
    expect(byName['ok'], 42);
    expect(byName.containsKey('bad'), isTrue); // the row still resolves...
    expect(byName['bad'], isNull); // ...as unreachable ("—"), sweep intact
  });

  test('a daemon-unreachable error ends the sweep', () async {
    final client = _DownClient();
    await expectLater(
      client.testLatencyEach(['x', 'y']).toList(),
      throwsA(isA<DaemonUnreachable>()),
    );
  });

  test('an empty node set is an immediately-closed, empty stream', () async {
    final client = _DelayClient(const {});
    expect(await client.testLatencyEach(const <String>[]).toList(), isEmpty);
    expect(client.maxInFlight, 0);
  });
}
