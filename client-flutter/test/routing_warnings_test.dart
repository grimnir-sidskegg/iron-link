/// The daemon's routing fallbacks (`routing_warnings` on the state event and
/// the status reply) reach the session and render as a notice in the header.
library;

import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:iron_link_flutter/src/app/session.dart';
import 'package:iron_link_flutter/src/ipc/client.dart';
import 'package:iron_link_flutter/src/ui/status_header.dart';
import 'package:iron_link_flutter/src/wire/wire.dart';

const _warning = 'routing "basic" rule 2: group "Auto US" (6666) has no '
    'dialable members; using the active node instead';

class _Client extends DaemonClient {
  _Client() : super(endpoint: '/dev/null');

  final events = StreamController<Event>();

  @override
  Stream<Event> subscribe() => events.stream;

  @override
  Future<Response> status() async => const RunningResponse(
        entries: [CoreEntry(role: 'proxy', state: 'running')],
        routingWarnings: [_warning],
      );
}

void main() {
  test('a state event carries the warnings; an idle one clears them',
      () async {
    final client = _Client();
    final session = DaemonSession(client);
    addTearDown(session.dispose);
    session.start();
    await Future<void>.value();

    client.events.add(const StateEvent(
      entries: [CoreEntry(role: 'proxy', state: 'running')],
      routingWarnings: [_warning],
    ));
    await Future<void>.value();
    await Future<void>.value();
    expect(session.routingWarnings, [_warning]);

    client.events.add(const StateEvent());
    await Future<void>.value();
    await Future<void>.value();
    expect(session.routingWarnings, isEmpty);
  });

  testWidgets('the header shows the routing warnings while running',
      (tester) async {
    final client = _Client();
    final session = DaemonSession(client)..connected = true;
    await session.refreshStatus();
    await tester.pumpWidget(MaterialApp(
      home: Scaffold(
        body: SingleChildScrollView(
          child: StatusHeader(client: client, session: session),
        ),
      ),
    ));
    expect(find.text('Some routing targets are unavailable'), findsOneWidget);
    expect(find.text(_warning), findsOneWidget);
    // The running orb animates forever: unmount instead of settling.
    await tester.pumpWidget(const SizedBox());
    session.dispose();
  });
}
