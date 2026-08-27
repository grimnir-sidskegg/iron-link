/// End-to-end smoke against the REAL Go daemon: spawns
/// `daemon-go/bin/iron-link-daemon` with a scratch config root + endpoint
/// (IRON_LINK_CONFIG_DIR / IRON_LINK_SOCKET), exercises the typed client
/// over the live transport — a unix socket on Linux, a named pipe on
/// Windows — and tears the process down. Store verbs + status + subscribe
/// + the update verbs only — no activation, so no cores start, no TUN, no
/// conflict with a user session. The one network touch is a loopback HTTP
/// server this test runs itself (the `IRON_LINK_UPDATE_URL` case).
///
/// Skips itself when the daemon binary is not built.
@TestOn('linux || windows')
@Timeout(Duration(minutes: 2))
library;

import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:iron_link_flutter/src/ipc/client.dart';
import 'package:iron_link_flutter/src/wire/wire.dart';

final daemonBinary =
    '../daemon-go/bin/iron-link-daemon${Platform.isWindows ? '.exe' : ''}';

/// The update package's signed manifest fixture (dev current key, v1.2.3
/// seq 7) — served back to the daemon over loopback in the override case.
const updateFixtureDir = '../daemon-go/internal/update/testdata';

/// A CI dispatch build's stamp, `v0.0.0-<short sha>`, as far as semver
/// accepts it: below every fixture and tag, so the fixture is offered. An
/// all-digit sha with a leading zero is not a valid prerelease identifier —
/// the daemon treats such a build as unstamped and never offers anything.
final _dispatchStamp =
    RegExp(r'^v0\.0\.0-(?:0|[1-9][0-9]*|[0-9]*[a-f][0-9a-f]*)$');

const shareLink = 'vless://11111111-2222-3333-4444-555555555555@example.com:443'
    '?security=reality&sni=cdn.example.com&pbk=KEY&fp=chrome&sid=0123abcd'
    '&type=tcp#Smoke';

/// One spawned daemon: its scratch config root, endpoint, process and typed
/// client, plus its stderr (the startup log) for assertions.
class _LiveDaemon {
  _LiveDaemon._(this.scratch, this.process, this.client, this.stderrLines);

  static int _seq = 0;

  final Directory scratch;
  final Process process;
  final DaemonClient client;
  final List<String> stderrLines;

  static Future<_LiveDaemon> start(File binary,
      {Map<String, String> env = const {}}) async {
    final scratch = Directory.systemTemp.createTempSync('iron-link-smoke-');
    // Unique per-run pipe name so the smoke never collides with a user
    // daemon on \\.\pipe\iron-link, nor with a second instance of this run.
    final socket = Platform.isWindows
        ? '\\\\.\\pipe\\iron-link-test-$pid-${_seq++}'
        : '${scratch.path}/iron-link.sock';
    final process =
        await Process.start(binary.absolute.path, const [], environment: {
      'IRON_LINK_CONFIG_DIR': '${scratch.path}/config',
      'IRON_LINK_SOCKET': socket,
      ...env,
    });
    // Drain output so the daemon never blocks on a full pipe; stderr is
    // kept line by line (the startup log lands there).
    process.stdout.drain<void>();
    final stderrLines = <String>[];
    process.stderr
        .transform(utf8.decoder)
        .transform(const LineSplitter())
        .listen(stderrLines.add);
    final client = DaemonClient(endpoint: socket);
    // Wait for the socket to come up.
    for (var i = 0; i < 100; i++) {
      try {
        await client.status();
        return _LiveDaemon._(scratch, process, client, stderrLines);
      } on DaemonUnreachable {
        await Future<void>.delayed(const Duration(milliseconds: 100));
      }
    }
    process.kill();
    fail('daemon socket never came up at $socket');
  }

  /// Polls the daemon's stderr for a line containing [needle].
  Future<bool> sawLog(String needle) async {
    for (var i = 0; i < 50; i++) {
      if (stderrLines.any((l) => l.contains(needle))) return true;
      await Future<void>.delayed(const Duration(milliseconds: 100));
    }
    return false;
  }

  Future<void> stop() async {
    process.kill();
    await process.exitCode;
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
  }
}

void main() {
  final binary = File(daemonBinary);
  if (!binary.existsSync()) {
    test('live smoke (skipped: $daemonBinary not built)', () {},
        skip: 'build the daemon first: see AGENTS.md');
    return;
  }

  late _LiveDaemon daemon;
  late DaemonClient client;

  setUpAll(() async {
    daemon = await _LiveDaemon.start(binary);
    client = daemon.client;
  });

  tearDownAll(() => daemon.stop());

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

  test('check_update: a fresh daemon serves an unchecked verdict', () async {
    // The background loop holds its first attempt back for ≥ 60 s, so a
    // daemon this young has touched no network: nothing checked, nothing
    // offered — and the version stamp travels regardless.
    final st = await client.checkUpdate();
    expect(st.checkedAt, isNull);
    expect(st.available, isFalse);
    expect(st.latestVersion, isEmpty);
    expect(st.currentVersion, isNotEmpty); // "dev" on an unstamped build
    expect(st.downloadState, isEmpty);
    expect(st.transport, 'direct', reason: 'no session, both flags default ON');
  });

  test('check_update force: IRON_LINK_UPDATE_URL reaches a local manifest',
      () async {
    final manifest = File('$updateFixtureDir/update.json').readAsBytesSync();
    final signature =
        File('$updateFixtureDir/update.json.minisig').readAsBytesSync();
    final served = <String>[];
    final server = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
    server.listen((req) {
      served.add(req.uri.path);
      final body = switch (req.uri.path) {
        '/update.json' => manifest,
        '/update.json.minisig' => signature,
        _ => null,
      };
      if (body == null) {
        req.response.statusCode = HttpStatus.notFound;
      } else {
        req.response.add(body);
      }
      req.response.close();
    });
    final url = 'http://127.0.0.1:${server.port}/update.json';

    // A second daemon (own scratch + endpoint) started with the override.
    final second = await _LiveDaemon.start(binary,
        env: {'IRON_LINK_UPDATE_URL': url});
    try {
      expect(await second.sawLog('overridden by IRON_LINK_UPDATE_URL'), isTrue,
          reason: 'startup log: ${second.stderrLines}');
      final st = await second.client.checkUpdate(force: true);
      expect(served, ['/update.json', '/update.json.minisig'],
          reason: 'the override host must serve manifest + signature');
      expect(st.checkedAt, isNotNull, reason: 'signature verified, manifest evaluated');
      expect(st.latestVersion, 'v1.2.3');
      expect(st.notesUrl, 'https://example.com/iron-link/notes/v1.2.3');
      expect(st.transport, 'direct');
      // The verdict hinges on the daemon's own stamp and clock, neither of
      // which this harness can pin: an unstamped ("dev") build never
      // compares versions, an expired manifest (the fixture lapses on
      // 2027-02-23) is cached as stale but never offered, and a tag build
      // may already sort at or above the fixture's v1.2.3. Pin the verdict
      // only where it is determined; the ordering table lives in
      // internal/update.
      final current = st.currentVersion;
      final reason = 'current $current, stale ${st.stale}';
      if (current == 'dev' || st.stale) {
        expect(st.available, isFalse, reason: reason);
      } else if (_dispatchStamp.hasMatch(current)) {
        expect(st.available, isTrue, reason: reason);
      }
      expect(st.downloadState, isEmpty);
    } finally {
      await second.stop();
      await server.close(force: true);
    }
  });

  test('download_update / apply_update: notify-only off Windows', () async {
    // Windows has a downloadable channel; its verbs would run for real
    // against the fixture's example.com artifact, so only Linux pins the
    // platform gate here.
    final notSupported = throwsA(isA<DaemonError>()
        .having((e) => e.message, 'message', contains('not supported')));
    await expectLater(client.downloadUpdate(), notSupported);
    await expectLater(client.applyUpdate(), notSupported);
  }, skip: Platform.isLinux ? null : 'Linux-only: the not-supported gate');
}
