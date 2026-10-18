/// Widget tests for RoutingPage's save path: `upsert_routing` only persists,
/// so saving the ACTIVE routing while a session runs offers a restart (a
/// bare `activate`); anything else just saves. The fake client returns
/// canned lists; the `DaemonSession` is passive, pre-seeded running or idle.
library;

import 'dart:convert';
import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:iron_link_flutter/src/app/session.dart';
import 'package:iron_link_flutter/src/ipc/client.dart';
import 'package:iron_link_flutter/src/ui/routing_page.dart';
import 'package:iron_link_flutter/src/wire/wire.dart';

Map<String, Object?> _fixture(String relative) =>
    jsonDecode(File('../contract/fixtures/$relative').readAsStringSync())
        as Map<String, Object?>;

class _FakeClient extends DaemonClient {
  _FakeClient(this.routing, {this.running = false})
      : super(endpoint: '/dev/null');

  final List<RoutingInfo> routing;
  final bool running;
  final List<Map<String, Object?>> upserted = [];
  final List<bool> activated = []; // the tun flag of each activate

  @override
  Future<List<RoutingInfo>> listRouting() async => routing;

  @override
  Future<RoutingSchema> routingSchema() async =>
      (Response.fromJson(_fixture('responses/routing_schema.json'))
              as RoutingSchemaResponse)
          .routingSchema;

  @override
  Future<List<NodeInfo>> listNodes({String? profile}) async => const [];

  @override
  Future<Map<String, Object?>> getRouting(String routing) async => {
        ...(_fixture('responses/routing_config.json')['routing_config']
            as Map<String, Object?>),
        'id': routing,
      };

  @override
  Future<void> upsertRouting(Map<String, Object?> config) async {
    upserted.add(config);
  }

  @override
  Future<ActivatedResponse> activate(
      {String? profile, String? node, String? routing, bool tun = false}) async {
    activated.add(tun);
    return const ActivatedResponse();
  }

  @override
  Future<Response> status() async => running
      ? const RunningResponse(entries: [CoreEntry(role: 'proxy', state: 'running')])
      : const IdleResponse();
}

const _active = RoutingInfo(id: 'r1', name: 'basic', active: true);
const _other = RoutingInfo(id: 'r2', name: 'strict');

Future<_FakeClient> _pump(WidgetTester tester, List<RoutingInfo> routing,
    {required bool running}) async {
  final client = _FakeClient(routing, running: running);
  final session = DaemonSession(client); // no start() — passive
  if (running) {
    session.entries = const [CoreEntry(role: 'proxy', state: 'running')];
    session.active = const PersistedEntry(routing: 'basic', tun: false);
  }
  addTearDown(session.dispose);
  await tester.pumpWidget(
      MaterialApp(home: RoutingPage(client: client, session: session)));
  await tester.pumpAndSettle();
  return client;
}

/// Saves the only row through the "Edit as JSON" dialog, unchanged.
Future<void> _saveAsJson(WidgetTester tester) async {
  await tester.tap(find.byTooltip('Edit as JSON'));
  await tester.pumpAndSettle();
  await tester.tap(find.text('Save'));
  await tester.pumpAndSettle();
}

void main() {
  testWidgets('saving the active routing while running offers a restart',
      (tester) async {
    final client = await _pump(tester, [_active], running: true);
    await tester.tap(find.byTooltip('Edit'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Save')); // the form editor's app-bar action
    await tester.pumpAndSettle();

    expect(client.upserted.single['id'], 'r1');
    expect(find.text('Apply routing'), findsOneWidget);
    await tester.tap(find.text('Confirm'));
    await tester.pumpAndSettle();
    expect(client.activated, [false]); // the session's tun flag, kept
    expect(find.text('Routing config saved and applied'), findsOneWidget);
  });

  testWidgets('declining the restart saves only, applying on next connect',
      (tester) async {
    final client = await _pump(tester, [_active], running: true);
    await _saveAsJson(tester);

    expect(find.text('Apply routing'), findsOneWidget);
    await tester.tap(find.text('Cancel'));
    await tester.pumpAndSettle();
    expect(client.upserted, hasLength(1));
    expect(client.activated, isEmpty);
    expect(find.text('Routing config saved; it applies on the next connect'),
        findsOneWidget);
  });

  testWidgets('an idle session saves without a prompt', (tester) async {
    final client = await _pump(tester, [_active], running: false);
    await _saveAsJson(tester);

    expect(find.text('Apply routing'), findsNothing);
    expect(client.upserted, hasLength(1));
    expect(client.activated, isEmpty);
    expect(find.text('Routing config saved'), findsOneWidget);
  });

  testWidgets('saving a non-active routing while running does not prompt',
      (tester) async {
    final client = await _pump(tester, [_other], running: true);
    await _saveAsJson(tester);

    expect(find.text('Apply routing'), findsNothing);
    expect(client.upserted.single['id'], 'r2');
    expect(client.activated, isEmpty);
    expect(find.text('Routing config saved'), findsOneWidget);
  });
}
