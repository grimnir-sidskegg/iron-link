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

/// Drives a controllable event stream AND hands out a completer per status
/// poll, so a test can interleave a pushed state event with a late poll reply.
class _EventClient extends DaemonClient {
  _EventClient() : super(endpoint: '/dev/null');

  final events = StreamController<Event>();
  final pending = <Completer<Response>>[];

  @override
  Stream<Event> subscribe() => events.stream;

  @override
  Future<Response> status() {
    final c = Completer<Response>();
    pending.add(c);
    return c.future;
  }
}

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

  test('a pushed state event moves the live node without a status poll',
      () async {
    final client = _EventClient();
    final session = DaemonSession(client);
    addTearDown(session.dispose);

    session.start(); // opens the subscribe stream; the backstop poll stays pending
    await Future<void>.value();

    client.events.add(const StateEvent(
      entries: [CoreEntry(role: 'proxy', state: 'running')],
      active: PersistedEntry(node: 'Auto'),
      activeNodeLive: 'Frankfurt',
    ));
    await Future<void>.value();
    await Future<void>.value();

    // The urltest's live pick reached the session straight off the event.
    expect(session.activeNodeLive, 'Frankfurt');

    // An idle state event clears it.
    client.events.add(const StateEvent());
    await Future<void>.value();
    await Future<void>.value();
    expect(session.activeNodeLive, isNull);
  });

  test('a state event drops a status poll that was already in flight',
      () async {
    final client = _EventClient();
    final session = DaemonSession(client);
    addTearDown(session.dispose);

    session.start(); // issues the first backstop poll (pending, pre-switch)
    await Future<void>.value();
    expect(client.pending, hasLength(1));

    // The urltest auto-switch arrives as a state event BEFORE the poll reply.
    client.events.add(const StateEvent(
      entries: [CoreEntry(role: 'proxy', state: 'running')],
      activeNodeLive: 'Tokyo',
    ));
    await Future<void>.value();
    await Future<void>.value();
    expect(session.activeNodeLive, 'Tokyo');

    // The stale poll (served while the live node was still Frankfurt) lands
    // late — the event bumped the generation, so it must be dropped, not snap
    // the badge back to the old node.
    client.pending[0].complete(_running('Frankfurt'));
    await Future<void>.value();
    await Future<void>.value();
    expect(session.activeNodeLive, 'Tokyo');
  });

  test('a running state event without a live node keeps the poll value',
      () async {
    // Version skew: a daemon whose status carries active_node_live but whose
    // state events do not (the commit before the push). A running event must
    // not wipe the poll-provided live node.
    final client = _EventClient();
    final session = DaemonSession(client);
    addTearDown(session.dispose);

    session.start();
    await Future<void>.value();
    client.pending[0].complete(_running('Frankfurt')); // poll sets the live node
    await Future<void>.value();
    await Future<void>.value();
    expect(session.activeNodeLive, 'Frankfurt');

    // A running state event with NO active_node_live (older daemon) arrives.
    client.events.add(const StateEvent(
      entries: [CoreEntry(role: 'proxy', state: 'running')],
    ));
    await Future<void>.value();
    await Future<void>.value();
    expect(session.activeNodeLive, 'Frankfurt'); // not wiped to null
  });
}
