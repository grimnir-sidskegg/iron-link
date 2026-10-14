/// The daemon going away and coming back: a dropped connection must not
/// leave the last status on screen as if the tunnel were still up, and a
/// (re)connect must tell list pages to refetch what they loaded (or failed to
/// load) while the daemon was away.
library;

import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:iron_link_flutter/src/app/session.dart';
import 'package:iron_link_flutter/src/ipc/client.dart';
import 'package:iron_link_flutter/src/ui/home_page.dart';
import 'package:iron_link_flutter/src/wire/wire.dart';

/// A daemon that can be taken down and brought back: while [up] is false
/// every call fails as unreachable; each subscribe hands out a fresh stream
/// the test drives through [events].
class _FlakyClient extends DaemonClient {
  _FlakyClient({required this.up}) : super(endpoint: '/dev/null');

  bool up;
  StreamController<Event>? events;
  int listCalls = 0;

  DaemonUnreachable get _down =>
      const DaemonUnreachable('/dev/null', 'connection refused');

  @override
  Stream<Event> subscribe() {
    if (!up) return Stream.error(_down);
    events = StreamController<Event>();
    return events!.stream;
  }

  @override
  Future<Response> status() async => up ? const IdleResponse() : throw _down;

  @override
  Future<List<NodeInfo>> listNodes({String? profile}) async {
    listCalls++;
    if (!up) throw _down;
    return const [NodeInfo(id: 'n1', name: 'Tokyo')];
  }

  @override
  Future<List<SubscriptionInfo>> listSubscriptions() async =>
      up ? const [] : throw _down;
}

const _runningEvent = StateEvent(
  entries: [CoreEntry(role: 'tun', state: 'running')],
  active: PersistedEntry(profile: 'main', tun: true),
  activeNodeLive: 'n1',
);

void main() {
  test('a dropped daemon clears the running state', () async {
    final client = _FlakyClient(up: true);
    final session = DaemonSession(client)..start();
    addTearDown(session.dispose);

    client.events!.add(_runningEvent);
    await pumpEventQueue();
    expect(session.connected, isTrue);
    expect(session.isRunning, isTrue);

    client.up = false;
    await client.events!.close();
    await pumpEventQueue();
    expect(session.connected, isFalse);
    expect(session.isRunning, isFalse);
    expect(session.active, isNull);
    expect(session.activeNodeLive, isNull);
  });

  test('every (re)connect bumps storeRev', () async {
    final client = _FlakyClient(up: true);
    final session = DaemonSession(client)..start();
    addTearDown(session.dispose);

    final before = session.storeRev;
    client.events!.add(_runningEvent);
    await pumpEventQueue();
    expect(session.storeRev, before + 1);

    // Further events on the same connection are not a reconnect.
    client.events!.add(_runningEvent);
    await pumpEventQueue();
    expect(session.storeRev, before + 1);
  });

  testWidgets('a home page mounted while the daemon is down loads its list '
      'once the daemon comes up', (tester) async {
    tester.view.physicalSize = const Size(1000, 1600);
    tester.view.devicePixelRatio = 1.0;
    addTearDown(tester.view.reset);
    final client = _FlakyClient(up: false);
    final session = DaemonSession(client)..start();

    // The first connect failure notifies before the page exists.
    await tester.pump();
    expect(session.connected, isFalse);
    await tester.pumpWidget(
        MaterialApp(home: HomePage(client: client, session: session)));
    await tester.pump();
    expect(find.text('Tokyo'), findsNothing);

    // The service is started; the 1 s retry reaches it.
    client.up = true;
    await tester.pump(const Duration(seconds: 1));
    client.events!.add(const StateEvent(entries: []));
    await tester.pumpAndSettle();
    expect(session.connected, isTrue);
    expect(find.text('Tokyo'), findsOneWidget);

    // The down banner's service probe (not injectable through HomePage)
    // leaves its 5 s timeout behind; run it out before tearing down.
    await tester.pump(const Duration(seconds: 5));
    await tester.pumpWidget(const SizedBox());
    session.dispose();
  });
}
