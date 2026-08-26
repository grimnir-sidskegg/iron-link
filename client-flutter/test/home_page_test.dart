/// Widget tests for HomePage's node/subscription tree, over an in-process
/// fake: `DaemonClient` does not connect in its constructor and its verbs
/// are overridable, so the fake just returns canned lists. The
/// `DaemonSession` never gets `start()` — passive (no sockets, no timers),
/// which also means idle (`isRunning` false) for the circle-tap test.
library;

import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:iron_link_flutter/src/app/session.dart';
import 'package:iron_link_flutter/src/ipc/client.dart';
import 'package:iron_link_flutter/src/ui/home_page.dart';
import 'package:iron_link_flutter/src/wire/wire.dart';

class _FakeClient extends DaemonClient {
  _FakeClient({this.nodes = const [], this.subs = const [], this.running = false})
      : super(endpoint: '/dev/null');

  final List<NodeInfo> nodes;
  final List<SubscriptionInfo> subs;
  final bool running;
  final List<String> selected = [];
  final List<String> switched = [];
  final List<String> probed = [];
  final List<Map<String, Object?>> upsertedGroups = [];

  @override
  Future<ProfilesResponse> listProfiles() async =>
      const ProfilesResponse(profiles: ['main'], activeProfile: 'main');

  @override
  Future<void> upsertGroup(Map<String, Object?> group) async {
    upsertedGroups.add(group);
  }

  /// The stored spec get_group hands back for an edit (explicit members + a
  /// probe interval — the shape list_nodes cannot express).
  @override
  Future<Map<String, Object?>> getGroup(String group) async => {
        'name': 'Auto',
        'members': ['n1'],
        'probe': {'interval_sec': 90},
      };

  @override
  Future<List<LatencyResult>> testLatency([List<String>? nodes]) async {
    if (nodes != null) probed.addAll(nodes);
    return [for (final n in nodes ?? const <String>[]) LatencyResult(node: n)];
  }

  /// The live node `status()` reports; a `switchNode` moves it (the daemon's
  /// synchronous selector swap, faked).
  String? liveNode;

  @override
  Future<List<NodeInfo>> listNodes({String? profile}) async => nodes;

  @override
  Future<List<SubscriptionInfo>> listSubscriptions() async => subs;

  @override
  Future<void> selectNode(String id) async {
    selected.add(id);
  }

  @override
  Future<String> switchNode(String node) async {
    switched.add(node);
    liveNode = node; // the selector moved live to it
    return node;
  }

  @override
  Future<Response> status() async => running
      ? RunningResponse(
          entries: const [
            CoreEntry(role: 'tun', state: 'running'),
            CoreEntry(role: 'proxy', state: 'running'),
          ],
          activeNodeLive: liveNode)
      : const IdleResponse();
}

const tokyo = NodeInfo(id: 'n1', name: 'Tokyo');
const osaka = NodeInfo(id: 'n2', name: 'Osaka', subId: 's1', active: true);
const autoGroup = NodeInfo(
    id: 'g1',
    name: 'Auto',
    kind: 'group',
    members: ['n1', 'n2'],
    active: true);
const mySub = SubscriptionInfo(
    id: 's1', name: 'MySub', url: 'https://example.com/a', nodeCount: 1);
const emptySub = SubscriptionInfo(
    id: 's2', name: 'EmptySub', url: 'https://example.com/b', nodeCount: 0);

/// Pumps HomePage over [client] in a viewport tall enough that the rows
/// below the ~350 px status header actually get built by the lazy sliver.
Future<void> _pumpHome(WidgetTester tester, DaemonClient client) async {
  tester.view.physicalSize = const Size(1000, 1600);
  tester.view.devicePixelRatio = 1.0;
  addTearDown(tester.view.reset);
  final session = DaemonSession(client); // no start() — passive
  addTearDown(session.dispose);
  await tester.pumpWidget(
      MaterialApp(home: HomePage(client: client, session: session)));
  await tester.pumpAndSettle(); // lets _load()'s canned futures land
}

void main() {
  testWidgets('standalone nodes render above subscription groups',
      (tester) async {
    // Daemon order interleaves them: the grouped node comes first.
    final client = _FakeClient(nodes: [osaka, tokyo], subs: [mySub]);
    await _pumpHome(tester, client);

    final tokyoY = tester.getTopLeft(find.text('Tokyo')).dy;
    final headerY = tester.getTopLeft(find.text('MySub')).dy;
    final osakaY = tester.getTopLeft(find.text('Osaka')).dy;
    expect(tokyoY, lessThan(headerY));
    expect(headerY, lessThan(osakaY));
  });

  testWidgets('tapping a subscription header collapses and re-expands it',
      (tester) async {
    final client = _FakeClient(nodes: [tokyo, osaka], subs: [mySub]);
    await _pumpHome(tester, client);
    expect(find.text('Osaka'), findsOneWidget);

    await tester.tap(find.text('MySub'));
    await tester.pump();
    expect(find.text('Osaka'), findsNothing);
    expect(find.text('MySub'), findsOneWidget); // the header stays

    await tester.tap(find.text('MySub'));
    await tester.pump();
    expect(find.text('Osaka'), findsOneWidget);
  });

  testWidgets('a group renders auto chips and a trimmed menu', (tester) async {
    final client = _FakeClient(nodes: [autoGroup]);
    await _pumpHome(tester, client);

    expect(find.text('Auto'), findsOneWidget);
    expect(find.text('auto'), findsOneWidget); // the group badge
    expect(find.text('2 nodes'), findsOneWidget); // the member count

    await tester.tap(find.byIcon(Icons.more_vert));
    await tester.pumpAndSettle();
    // A group keeps switch/select/remove but has no endpoint to probe, diagnose,
    // or pin a core to.
    expect(find.text('Switch traffic here'), findsOneWidget);
    expect(find.text('Set as profile default'), findsOneWidget);
    expect(find.text('Remove'), findsOneWidget);
    expect(find.text('Test latency'), findsNothing);
    expect(find.text('Diagnose'), findsNothing);
    expect(find.text('Pin to sing-box'), findsNothing);
  });

  testWidgets('"Test all" skips groups (a group has no endpoint to probe)',
      (tester) async {
    final client = _FakeClient(nodes: [tokyo, osaka, autoGroup]);
    await _pumpHome(tester, client);

    await tester.tap(find.text('Test all'));
    await tester.pumpAndSettle();

    expect(client.probed, containsAll(<String>['Tokyo', 'Osaka']));
    expect(client.probed, isNot(contains('Auto')));
  });

  testWidgets('an active running group shows the live member badge',
      (tester) async {
    final client = _FakeClient(nodes: [autoGroup], running: true)
      ..liveNode = 'Frankfurt';
    final session = DaemonSession(client);
    session.entries = const [
      CoreEntry(role: 'tun', state: 'running'),
      CoreEntry(role: 'proxy', state: 'running'),
    ];
    session.activeNodeLive = 'Frankfurt';
    addTearDown(session.dispose);

    tester.view.physicalSize = const Size(1000, 1600);
    tester.view.devicePixelRatio = 1.0;
    addTearDown(tester.view.reset);
    await tester.pumpWidget(
        MaterialApp(home: HomePage(client: client, session: session)));
    // The running StatusHeader animates forever, so pumpAndSettle would hang —
    // step fixed frames to let _load()'s canned futures land instead.
    for (var i = 0; i < 6; i++) {
      await tester.pump(const Duration(milliseconds: 20));
    }

    // The group is active and the urltest's live pick is a different member.
    expect(find.text('now: Frankfurt'), findsOneWidget);
  });

  testWidgets('the New button offers building an auto group over the nodes',
      (tester) async {
    final client = _FakeClient(nodes: [tokyo, osaka], subs: [mySub]);
    await _pumpHome(tester, client);

    // New → the two-way menu → Auto group opens the group editor.
    await tester.tap(find.byType(FloatingActionButton));
    await tester.pumpAndSettle();
    expect(find.text('Link or subscription'), findsOneWidget);
    await tester.tap(find.text('Auto group'));
    await tester.pumpAndSettle();
    expect(find.text('New auto group'), findsOneWidget);

    // Name it, pick a node (Tokyo appears both in the list behind the dialog
    // and in the picker — scope to the dialog's checkbox tile).
    await tester.enterText(
        find
            .descendant(
                of: find.byType(AlertDialog), matching: find.byType(TextField))
            .first,
        'My Auto');
    await tester.pumpAndSettle();
    await tester.tap(find.widgetWithText(CheckboxListTile, 'Tokyo'));
    await tester.pumpAndSettle();
    await tester.tap(find.widgetWithText(FilledButton, 'Create'));
    await tester.pumpAndSettle();

    expect(client.upsertedGroups, hasLength(1));
    final spec = client.upsertedGroups.single;
    expect(spec['name'], 'My Auto');
    expect(spec['members'], ['n1']); // Tokyo's id
    expect(spec.containsKey('all_of_sub'), isFalse);
  });

  testWidgets('the group editor can target a whole subscription',
      (tester) async {
    // osaka belongs to mySub, so mySub is a groupable all_of_sub target.
    final client = _FakeClient(nodes: [osaka], subs: [mySub]);
    await _pumpHome(tester, client);

    await tester.tap(find.byType(FloatingActionButton));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Auto group'));
    await tester.pumpAndSettle();

    // Both modes are available; switch from Pick nodes to Whole subscription
    // (the dropdown pre-selects the only groupable subscription).
    await tester.tap(find.text('Whole subscription'));
    await tester.pumpAndSettle();
    await tester.enterText(
        find
            .descendant(
                of: find.byType(AlertDialog), matching: find.byType(TextField))
            .first,
        'Sub Auto');
    await tester.pumpAndSettle();
    await tester.tap(find.widgetWithText(FilledButton, 'Create'));
    await tester.pumpAndSettle();

    final spec = client.upsertedGroups.single;
    expect(spec['name'], 'Sub Auto');
    expect(spec['all_of_sub'], 's1');
    expect(spec.containsKey('members'), isFalse);
  });

  testWidgets('editing a user group round-trips its spec via get_group',
      (tester) async {
    // autoGroup is a user group (no subId); its menu offers Edit….
    final client = _FakeClient(nodes: [tokyo, osaka, autoGroup]);
    await _pumpHome(tester, client);

    // The group row is last; open its menu and pick Edit.
    await tester.tap(find.byIcon(Icons.more_vert).last);
    await tester.pumpAndSettle();
    await tester.tap(find.text('Edit…'));
    await tester.pumpAndSettle();

    // The editor pre-fills from get_group's stored spec (name + membership).
    expect(find.text('Edit auto group'), findsOneWidget);
    expect(
        find.descendant(
            of: find.byType(AlertDialog), matching: find.text('Auto')),
        findsWidgets);

    // Rename and Save; the caller stamps the group's id for an in-place replace.
    await tester.enterText(
        find
            .descendant(
                of: find.byType(AlertDialog), matching: find.byType(TextField))
            .first,
        'Renamed');
    await tester.pumpAndSettle();
    await tester.tap(find.widgetWithText(FilledButton, 'Save'));
    await tester.pumpAndSettle();

    final spec = client.upsertedGroups.single;
    expect(spec['id'], 'g1'); // in-place replace
    expect(spec['name'], 'Renamed');
    expect(spec['members'], ['n1']); // the pre-filled membership survived
    expect((spec['probe'] as Map)['interval_sec'], 90); // and the probe
  });

  testWidgets('the group editor rejects an out-of-uint32 re-rank interval',
      (tester) async {
    final client = _FakeClient(nodes: [tokyo], subs: const []);
    await _pumpHome(tester, client);

    await tester.tap(find.byType(FloatingActionButton));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Auto group'));
    await tester.pumpAndSettle();

    final dialogField = find.descendant(
        of: find.byType(AlertDialog), matching: find.byType(TextField));
    await tester.enterText(dialogField.first, 'My Auto'); // name
    await tester.tap(find.widgetWithText(CheckboxListTile, 'Tokyo'));
    await tester.pumpAndSettle();

    // An interval past uint32 disables Create (rather than reaching the daemon
    // as an unmarshalable value) and surfaces the range hint.
    await tester.enterText(dialogField.at(1), '5000000000');
    await tester.pumpAndSettle();
    expect(find.text('must be 1…4294967295'), findsOneWidget);
    expect(
        tester
            .widget<FilledButton>(find.widgetWithText(FilledButton, 'Create'))
            .onPressed,
        isNull);

    // A valid interval re-enables Create and rides through on the spec.
    await tester.enterText(dialogField.at(1), '120');
    await tester.pumpAndSettle();
    await tester.tap(find.widgetWithText(FilledButton, 'Create'));
    await tester.pumpAndSettle();
    expect((client.upsertedGroups.single['probe'] as Map)['interval_sec'], 120);
  });

  testWidgets('a subscription with no nodes shows the placeholder row',
      (tester) async {
    final client = _FakeClient(subs: [emptySub]);
    await _pumpHome(tester, client);

    expect(find.text('EmptySub'), findsOneWidget);
    expect(find.text('(no nodes — refresh?)'), findsOneWidget);
  });

  testWidgets('a circle tap while idle calls select_node — no dialog',
      (tester) async {
    final client = _FakeClient(nodes: [tokyo, osaka], subs: [mySub]);
    await _pumpHome(tester, client);

    // Tokyo is the only non-active node, so the only "set default" circle.
    await tester.tap(find.byTooltip('Set as profile default'));
    await tester.pumpAndSettle();

    expect(client.selected, ['n1']);
    expect(find.byType(AlertDialog), findsNothing);
  });

  testWidgets(
      'a circle tap while running switches AND sets default — no dialog, '
      'the play icon moves to the new node', (tester) async {
    final client =
        _FakeClient(nodes: [tokyo, osaka], subs: [mySub], running: true)
          ..liveNode = 'Osaka'; // the session starts live on Osaka
    final session = DaemonSession(client); // passive, but pre-seeded running
    session.entries = const [
      CoreEntry(role: 'tun', state: 'running'),
      CoreEntry(role: 'proxy', state: 'running'),
    ];
    session.activeNodeLive = 'Osaka';
    addTearDown(session.dispose);

    tester.view.physicalSize = const Size(1000, 1600);
    tester.view.devicePixelRatio = 1.0;
    addTearDown(tester.view.reset);
    await tester.pumpWidget(
        MaterialApp(home: HomePage(client: client, session: session)));
    // The running StatusHeader animates forever, so pumpAndSettle would hang —
    // step fixed frames to let _load()'s canned futures land instead.
    for (var i = 0; i < 6; i++) {
      await tester.pump(const Duration(milliseconds: 20));
    }

    // Osaka is live ("Live node"); Tokyo is the only "Switch traffic here".
    expect(find.byTooltip('Live node'), findsOneWidget);
    await tester.tap(find.byTooltip('Switch traffic here'));
    // Flush the switch → select → refreshStatus await chain (all microtask
    // futures in the fake) and the follow-up rebuild.
    for (var i = 0; i < 8; i++) {
      await tester.pump(const Duration(milliseconds: 20));
    }

    // ONE atomic click: live switch + set default, no confirm dialog.
    expect(find.byType(AlertDialog), findsNothing);
    expect(client.switched, ['Tokyo']);
    expect(client.selected, ['n1']);
    // The status re-read landed the play icon on the new live node.
    expect(session.activeNodeLive, 'Tokyo');
    expect(find.byIcon(Icons.play_circle), findsOneWidget);
  });

  testWidgets("a hung node does not block a fast node's latency row",
      (tester) async {
    // Two standalone nodes; the probe for 'Osaka' never returns (a hung node),
    // 'Tokyo' resolves instantly. Under the old all-at-once batch NOTHING would
    // render until Osaka came back — here Tokyo's row must resolve on its own.
    final client = _LatencyFake(const [
      NodeInfo(id: 'n1', name: 'Tokyo'),
      NodeInfo(id: 'n2', name: 'Osaka'),
    ]);
    await _pumpHome(tester, client);

    await tester.tap(find.text('Test all'));
    await tester.pump(); // launch the per-node fan-out
    await tester.pump(const Duration(milliseconds: 20)); // Tokyo's reply lands

    // Tokyo resolved and rendered while Osaka's probe is still hung.
    expect(find.text('42 ms'), findsOneWidget);

    // Resolve the hung probe so the widget tears down cleanly; its row updates.
    client.osakaHang.complete(const [LatencyResult(node: 'Osaka', latencyMs: 7)]);
    await tester.pumpAndSettle();
    expect(find.text('7 ms'), findsOneWidget);
  });
}

/// Fakes the latency verbs for the incremental-render test: 'Tokyo' answers
/// instantly, 'Osaka' hangs on a [Completer] the test controls.
class _LatencyFake extends DaemonClient {
  _LatencyFake(this._nodes) : super(endpoint: '/dev/null');

  final List<NodeInfo> _nodes;
  final Completer<List<LatencyResult>> osakaHang = Completer();

  @override
  Future<List<NodeInfo>> listNodes({String? profile}) async => _nodes;

  @override
  Future<List<SubscriptionInfo>> listSubscriptions() async => const [];

  @override
  Future<Response> status() async => const IdleResponse();

  @override
  Future<List<LatencyResult>> testLatency([List<String>? nodes]) {
    final name = nodes!.single;
    if (name == 'Osaka') return osakaHang.future; // hung — never resolves
    return Future.value([LatencyResult(node: name, latencyMs: 42)]);
  }
}
