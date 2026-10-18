/// Widget tests for RoutingPage's save path: `upsert_routing` only persists,
/// so saving the ACTIVE routing while a session runs offers a restart (a
/// bare `activate`); anything else just saves. The fake client returns
/// canned lists; the `DaemonSession` is passive, pre-seeded running or idle.
/// Plus the active-routing diagram above the list.
library;

import 'dart:convert';
import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:iron_link_flutter/src/app/session.dart';
import 'package:iron_link_flutter/src/ipc/client.dart';
import 'package:iron_link_flutter/src/ui/routing_diagram.dart';
import 'package:iron_link_flutter/src/ui/routing_page.dart';
import 'package:iron_link_flutter/src/ui/routing_widgets.dart';
import 'package:iron_link_flutter/src/ui/widgets/iron_widgets.dart';
import 'package:iron_link_flutter/src/wire/wire.dart';

Map<String, Object?> _fixture(String relative) =>
    jsonDecode(File('../contract/fixtures/$relative').readAsStringSync())
        as Map<String, Object?>;

class _FakeClient extends DaemonClient {
  _FakeClient(this.routing,
      {this.running = false, this.doc, this.nodes = const []})
      : super(endpoint: '/dev/null');

  final List<RoutingInfo> routing;
  final bool running;

  /// The document get_routing serves (null = the contract fixture).
  final Map<String, Object?>? doc;
  final List<NodeInfo> nodes;
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
  Future<List<NodeInfo>> listNodes({String? profile}) async => nodes;

  @override
  Future<Map<String, Object?>> getRouting(String routing) async => {
        ...(doc ??
            _fixture('responses/routing_config.json')['routing_config']
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

/// Steps fixed frames: a running session's diagram threads animate forever,
/// so pumpAndSettle would hang.
Future<void> _settle(WidgetTester tester) async {
  for (var i = 0; i < 20; i++) {
    await tester.pump(const Duration(milliseconds: 50));
  }
}

Future<_FakeClient> _pump(WidgetTester tester, List<RoutingInfo> routing,
    {required bool running,
    Map<String, Object?>? doc,
    List<NodeInfo> nodes = const [],
    String? liveNode}) async {
  final client = _FakeClient(routing, running: running, doc: doc, nodes: nodes);
  final session = DaemonSession(client); // no start() — passive
  if (running) {
    session.entries = const [CoreEntry(role: 'proxy', state: 'running')];
    session.active = const PersistedEntry(routing: 'basic', tun: false);
    session.activeNodeLive = liveNode;
  }
  addTearDown(session.dispose);
  await tester.pumpWidget(
      MaterialApp(home: RoutingPage(client: client, session: session)));
  await _settle(tester);
  return client;
}

/// Saves the only row through the "Edit as JSON" dialog, unchanged.
Future<void> _saveAsJson(WidgetTester tester) async {
  await tester.tap(find.byTooltip('Edit as JSON'));
  await _settle(tester);
  await tester.tap(find.text('Save'));
  await _settle(tester);
}

void main() {
  testWidgets('saving the active routing while running offers a restart',
      (tester) async {
    final client = await _pump(tester, [_active], running: true);
    await tester.tap(find.byTooltip('Edit'));
    await _settle(tester);
    await tester.tap(find.text('Save')); // the form editor's app-bar action
    await _settle(tester);

    expect(client.upserted.single['id'], 'r1');
    expect(find.text('Apply routing'), findsOneWidget);
    await tester.tap(find.text('Confirm'));
    await _settle(tester);
    expect(client.activated, [false]); // the session's tun flag, kept
    expect(find.text('Routing config saved and applied'), findsOneWidget);
  });

  testWidgets('declining the restart saves only, applying on next connect',
      (tester) async {
    final client = await _pump(tester, [_active], running: true);
    await _saveAsJson(tester);

    expect(find.text('Apply routing'), findsOneWidget);
    await tester.tap(find.text('Cancel'));
    await _settle(tester);
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

  group('diagram', () {
    const doc = <String, Object?>{
      'name': 'mixed',
      'rule_sets': [],
      'rules': [
        {'domain_suffix': ['.jp'], 'target': {'Node': 'n1'}},
        {'ip_is_private': true, 'target': 'Direct'},
        {'domain_keyword': ['ads'], 'target': 'Block'},
        {'type': 'logical', 'mode': 'or', 'rules': [], 'target': 'Direct'},
        {'domain_suffix': ['.de'], 'target': {'Node': 'ghost'}},
        {'domain_suffix': ['.fi'], 'target': {'Node': 'g1'}},
        {'port': [22], 'target': {'Node': 'n1'}},
      ],
      'default_target': 'DefaultProxy',
    };
    const nodes = [
      NodeInfo(id: 'n1', name: 'Tokyo'),
      NodeInfo(id: 'g1', name: 'Fastest', kind: 'group', members: ['n1']),
    ];
    final diagram = find.byType(RoutingDiagram);
    Finder inDiagram(Finder f) => find.descendant(of: diagram, matching: f);

    testWidgets('renders one lane per destination with names',
        (tester) async {
      await _pump(tester, [_active], running: false, doc: doc, nodes: nodes);
      expect(find.text('Active routing: mixed'), findsOneWidget);
      for (final label in [
        'DefaultProxy',
        'Tokyo',
        'Direct',
        'Block',
        'ghost',
        'Fastest',
      ]) {
        expect(inDiagram(find.text(label)), findsOneWidget, reason: label);
      }
      expect(inDiagram(find.text('default')), findsOneWidget);
      expect(inDiagram(find.byIcon(Icons.block)), findsOneWidget);
      expect(inDiagram(find.text('auto')), findsOneWidget);
    });

    testWidgets('running, the default lane names the live node',
        (tester) async {
      await _pump(tester, [_active],
          running: true, doc: doc, nodes: nodes, liveNode: 'Osaka');
      expect(inDiagram(find.text('Osaka')), findsOneWidget);
      expect(inDiagram(find.text('DefaultProxy')), findsNothing);
    });

    testWidgets('tapping You lists every rule plus the default',
        (tester) async {
      await _pump(tester, [_active], running: false, doc: doc, nodes: nodes);
      await tester.tap(inDiagram(find.text('You')));
      await _settle(tester);
      expect(find.byType(RuleSummary), findsNWidgets(7));
      for (var n = 1; n <= 7; n++) {
        expect(find.text('$n'), findsOneWidget);
      }
      expect(find.byType(RouteTargetPill), findsNWidgets(7)); // 6 flat + default
    });

    testWidgets('tapping a lane lists only its rules', (tester) async {
      await _pump(tester, [_active], running: false, doc: doc, nodes: nodes);
      await tester.tap(inDiagram(find.text('Tokyo')));
      await _settle(tester);
      expect(find.byType(RuleSummary), findsNWidgets(2));
      expect(find.text('1'), findsOneWidget);
      expect(find.text('7'), findsOneWidget);
      expect(find.text('2'), findsNothing);
    });

    testWidgets('no active routing is one DefaultProxy lane',
        (tester) async {
      await _pump(tester, [_other], running: false);
      expect(inDiagram(find.text('DefaultProxy')), findsOneWidget);
      expect(inDiagram(find.byType(IronTag)), findsOneWidget);
      await tester.tap(inDiagram(find.text('You')));
      await _settle(tester);
      expect(find.byType(RuleSummary), findsNothing);
      expect(find.byType(RouteTargetPill), findsOneWidget);
    });

    testWidgets('fits the minimum window width', (tester) async {
      tester.view.physicalSize = const Size(600, 900);
      tester.view.devicePixelRatio = 1.0;
      addTearDown(tester.view.reset);
      await _pump(tester, [_active], running: false, doc: doc, nodes: [
        ...nodes,
        const NodeInfo(id: 'ghost', name: 'A very long node name indeed'),
      ]);
      expect(tester.takeException(), isNull);
    });
  });
}
