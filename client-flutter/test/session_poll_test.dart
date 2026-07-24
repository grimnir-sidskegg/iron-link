/// Unit test for the status-poll ordering guard. The client opens one
/// connection per request with no read timeout, so a `status` poll issued
/// BEFORE a live switch can land AFTER the switch's refreshStatus and
/// overwrite `active_node_live` with the pre-switch node — the play icon
/// snaps back to the old node. The session tags each poll with a monotonic id
/// and drops any response that a newer poll has superseded.
library;

import 'dart:async';

import 'package:flutter_test/flutter_test.dart';
import 'package:iron_link_flutter/src/app/session.dart';
import 'package:iron_link_flutter/src/ipc/client.dart';
import 'package:iron_link_flutter/src/wire/wire.dart';

/// Hands out a controllable future per `status()` call so a test can resolve
/// the polls OUT OF ORDER.
class _OrderedStatusClient extends DaemonClient {
  _OrderedStatusClient() : super(endpoint: '/dev/null');

  final List<Completer<Response>> pending = [];

  @override
  Future<Response> status() {
    final c = Completer<Response>();
    pending.add(c);
    return c.future;
  }
}

RunningResponse _running(String live) => RunningResponse(
      entries: const [CoreEntry(role: 'proxy', state: 'running')],
      activeNodeLive: live,
    );

void main() {
  test('a late stale status poll cannot overwrite a newer poll', () async {
    final client = _OrderedStatusClient();
    final session = DaemonSession(client);
    addTearDown(session.dispose);

    // Two overlapping polls: gen 1 (the stale pre-switch poll) then gen 2
    // (the fresh post-switch refreshStatus).
    final stale = session.refreshStatus();
    final fresh = session.refreshStatus();
    expect(client.pending.length, 2);

    // The fresh poll lands FIRST with the new live node.
    client.pending[1].complete(_running('new-node'));
    await fresh;
    expect(session.activeNodeLive, 'new-node');

    // The stale poll lands LATE with the old node — it must be dropped, not
    // clobber the fresher value.
    client.pending[0].complete(_running('old-node'));
    await stale;
    expect(session.activeNodeLive, 'new-node');
  });
}
