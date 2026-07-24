/// End-to-end smoke against the REAL Go daemon: spawns
/// `daemon-go/bin/iron-link-daemon` with a scratch config root + endpoint
/// (IRON_LINK_CONFIG_DIR / IRON_LINK_SOCKET), exercises the typed client
/// over the live transport — a unix socket on Linux, a named pipe on
/// Windows — and tears the process down. Store verbs + status + subscribe
/// only — no activation, so no cores start, no TUN, no conflict with a
/// user session.
///
/// Skips itself when the daemon binary is not built.
@TestOn('linux || windows')
@Timeout(Duration(minutes: 2))
library;

import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:iron_link_flutter/src/ipc/client.dart';
import 'package:iron_link_flutter/src/wire/wire.dart';

final daemonBinary =
    '../daemon-go/bin/iron-link-daemon${Platform.isWindows ? '.exe' : ''}';

const shareLink = 'vless://11111111-2222-3333-4444-555555555555@example.com:443'
    '?security=reality&sni=cdn.example.com&pbk=KEY&fp=chrome&sid=0123abcd'
    '&type=tcp#Smoke';

void main() {
  final binary = File(daemonBinary);
  if (!binary.existsSync()) {
    test('live smoke (skipped: $daemonBinary not built)', () {},
        skip: 'build the daemon first: see AGENTS.md');
    return;
  }

  late Directory scratch;
  late Process daemon;
  late DaemonClient client;

  setUpAll(() async {
    scratch = Directory.systemTemp.createTempSync('iron-link-smoke-');
    // Unique per-run pipe name so the smoke never collides with a user
    // daemon on \\.\pipe\iron-link.
    final socket = Platform.isWindows
        ? '\\\\.\\pipe\\iron-link-test-$pid'
        : '${scratch.path}/iron-link.sock';
    daemon = await Process.start(binary.absolute.path, const [], environment: {
      'IRON_LINK_CONFIG_DIR': '${scratch.path}/config',
      'IRON_LINK_SOCKET': socket,
    });
    // Drain output so the daemon never blocks on a full pipe.
    daemon.stdout.drain<void>();
    daemon.stderr.drain<void>();
    client = DaemonClient(endpoint: socket);
    // Wait for the socket to come up.
    for (var i = 0; i < 100; i++) {
      try {
        await client.status();
        return;
      } on DaemonUnreachable {
        await Future<void>.delayed(const Duration(milliseconds: 100));
      }
    }
    fail('daemon socket never came up at $socket');
  });

  tearDownAll(() async {
    daemon.kill();
    await daemon.exitCode;
    // Windows can hold file locks for a beat after the process exits;
    // retry the scratch cleanup instead of failing the whole suite.
    for (var attempt = 0;; attempt++) {
      try {
        scratch.deleteSync(recursive: true);
        break;
      } on FileSystemException {
        if (!Platform.isWindows || attempt >= 10) rethrow;
        await Future<void>.delayed(const Duration(milliseconds: 200));
      }
    }
  });

  test('status: a fresh daemon is idle', () async {
    expect(await client.status(), isA<IdleResponse>());
  });

  test('profile lifecycle over the wire', () async {
    await client.createProfile('smoke');
    await client.setActiveProfile('smoke');
    final profiles = await client.listProfiles();
    expect(profiles.profiles, contains('smoke'));
    expect(profiles.activeProfile, 'smoke');
  });

  test('node lifecycle: add → list → pin → clear → remove', () async {
    final id = await client.addNode(shareLink);
    expect(id, isNotEmpty);

    var nodes = await client.listNodes();
    expect(nodes, hasLength(1));
    expect(nodes.single.name, 'Smoke');
    expect(nodes.single.transport, 'tcp');
    expect(nodes.single.security, 'reality');
    expect(nodes.single.active, isTrue, reason: 'first node is auto-elected');

    await client.setNodePrefs(id, coreOverride: CoreType.xray);
    nodes = await client.listNodes();
    expect(nodes.single.coreOverride, CoreType.xray);

    await client.setNodePrefs(id); // absent override clears the pin
    nodes = await client.listNodes();
    expect(nodes.single.coreOverride, isNull);

    await client.removeNode(id);
    expect(await client.listNodes(), isEmpty);
  });

  test('routing lifecycle: upsert → list → select → remove', () async {
    // NB: the store shape spells targets in the Rust enum's PascalCase
    // ("DefaultProxy"); the `upsert_routing` contract fixture's "proxy"
    // would be rejected by the daemon's routing decoder.
    await client.upsertRouting({
      'name': 'smoke-basic',
      'rule_sets': <Object?>[],
      'rules': <Object?>[],
      'default_target': 'DefaultProxy',
    });
    final routing = await client.listRouting();
    final mine = routing.firstWhere((r) => r.name == 'smoke-basic');
    await client.selectRouting(mine.id);
    expect((await client.listRouting())
        .firstWhere((r) => r.id == mine.id)
        .active, isTrue);
    await client.removeRouting(mine.id);
    expect((await client.listRouting()).where((r) => r.id == mine.id), isEmpty);
  });

  test('routing read-back: upsert → get_routing; routing_schema', () async {
    await client.upsertRouting({
      'name': 'smoke-edit',
      'rule_sets': <Object?>[],
      'rules': <Object?>[
        {
          'target': 'DefaultProxy',
          'domain_keyword': <Object?>['example.com'],
        },
      ],
      'default_target': 'Direct',
    });
    final mine =
        (await client.listRouting()).firstWhere((r) => r.name == 'smoke-edit');

    final doc = await client.getRouting(mine.id);
    expect(doc['id'], mine.id);
    expect(doc['name'], 'smoke-edit');
    expect(doc['default_target'], 'Direct');
    final rules = doc['rules'] as List<Object?>;
    expect(rules, hasLength(1));
    final rule = rules.single as Map<String, Object?>;
    expect(rule['target'], 'DefaultProxy');
    expect(rule['domain_keyword'], ['example.com']);

    final schema = await client.routingSchema();
    expect(schema.targets, contains('DefaultProxy'));
    expect(schema.conditions.map((c) => c.key), contains('domain_keyword'));
    expect(schema.ruleSetFormats, contains('binary'));

    await client.removeRouting(mine.id);
  });

  test('settings lifecycle: defaults → set → readback → reject invalid',
      () async {
    final defaults = await client.getSettings();
    expect(defaults.socksPort, 10808);
    expect(defaults.lanBypass.cidrs, hasLength(3)); // link-local / ULA (RFC1918 moved to an immutable route rule)

    defaults.logLevel = 'warn';
    defaults.dns.viaTunnel = true;
    defaults.lanBypass.cidrs.add('100.64.0.0/10');
    final reply = await client.setSettings(defaults);
    expect(reply.needsReactivation, isFalse,
        reason: 'no session is running in the smoke daemon');

    final readback = await client.getSettings();
    expect(readback.logLevel, 'warn');
    expect(readback.dns.viaTunnel, isTrue);
    expect(readback.lanBypass.cidrs, contains('100.64.0.0/10'));

    readback.socksPort = -5;
    expect(client.setSettings(readback), throwsA(isA<DaemonError>()));
  });

  test('daemon errors surface as DaemonError', () async {
    expect(client.switchNode('no-such-node'), throwsA(isA<DaemonError>()));
  });

  test('subscribe: the first frame is a state snapshot', () async {
    final first = await client.subscribe().first;
    expect(first, isA<StateEvent>());
    expect((first as StateEvent).isRunning, isFalse);
  });
}
