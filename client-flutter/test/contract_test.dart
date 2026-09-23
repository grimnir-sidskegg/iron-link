/// The Dart half of the cross-language wire contract, against
/// `contract/fixtures/` — the same discipline as the Go suite:
///
/// - requests (`requests/`): this client EMITS them, so
///   `toJson()` must equal the fixture EXACTLY as a JSON value (explicit
///   nulls and omitted keys both matter);
/// - responses + events: the daemon emits, this client must DECODE every
///   one leniently;
/// - coverage is enforced both ways: every fixture file must have a case
///   and every case a fixture, so neither side drifts silently.
library;

import 'dart:convert';
import 'dart:io';

import 'package:collection/collection.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:iron_link_flutter/src/wire/wire.dart';

const fixturesRoot = '../contract/fixtures';

Map<String, Object?> readFixture(String relative) {
  final body = File('$fixturesRoot/$relative').readAsStringSync();
  return jsonDecode(body) as Map<String, Object?>;
}

/// Request fixtures → the request that must emit them, byte-equal as JSON.
final requestCases = <String, Request>{
  // session verbs (shared fixtures emitted by the client)
  'requests/activate_defaults.json': const ActivateRequest(),
  'requests/activate_full.json': const ActivateRequest(
      profile: 'home', node: 'tokyo', routing: 'split', tun: true),
  'requests/status.json': const StatusRequest(),
  'requests/stop_all.json': const StopRequest(),
  'requests/stop_role.json': const StopRequest(role: CoreRole.tun),
  'requests/subscribe.json': const SubscribeRequest(),
  'requests/switch_node.json': const SwitchNodeRequest('osaka'),
  'requests/test_latency_all.json': const TestLatencyRequest(),
  'requests/test_latency_nodes.json':
      const TestLatencyRequest(['tokyo', 'osaka']),
  // store verbs + diagnose + settings (promoted to shared at P0)
  'requests/add_node.json': const AddNodeRequest(
      'vless://00000000-0000-0000-0000-000000000000@example.com:443'
      '?security=reality&sni=cdn.example.com&pbk=KEY&fp=chrome&type=tcp'
      '#Grimnir'),
  'requests/add_subscription.json': const AddSubscriptionRequest(
      'https://example.com/sub',
      name: 'main',
      allowInvalidCerts: true,
      format: 'xray'),
  'requests/create_profile.json': const CreateProfileRequest('work'),
  'requests/delete_profile.json': const DeleteProfileRequest('old'),
  'requests/diagnose.json': const DiagnoseRequest('tokyo'),
  'requests/traffic_apps.json': const TrafficAppsRequest(),
  'requests/doctor.json': const DoctorRequest(),
  'requests/doctor_nodes.json': const DoctorNodesRequest(),
  'requests/network_report.json': const NetworkReportRequest(),
  'requests/forwarding_check.json': const ForwardingCheckRequest(),
  'requests/list_nodes.json': const ListNodesRequest(profile: 'main'),
  'requests/list_profiles.json': const ListProfilesRequest(),
  'requests/list_routing.json': const ListRoutingRequest(),
  'requests/list_subscriptions.json': const ListSubscriptionsRequest(),
  'requests/refresh_subscriptions_all.json':
      const RefreshSubscriptionsRequest(),
  'requests/refresh_subscriptions_one.json':
      const RefreshSubscriptionsRequest(sub: 'main'),
  'requests/remove_node.json': const RemoveNodeRequest('3f2a'),
  'requests/remove_routing.json': const RemoveRoutingRequest('r1'),
  'requests/remove_subscription.json': const RemoveSubscriptionRequest('main'),
  'requests/update_subscription.json': const UpdateSubscriptionRequest(
    'main',
    name: 'Main (EU)',
    url: 'https://example.com/sub2',
    enabled: false,
    allowInvalidCerts: true,
    updateIntervalSec: 43200,
    format: 'sing-box',
  ),
  'requests/select_node.json': const SelectNodeRequest('3f2a'),
  'requests/select_routing.json': const SelectRoutingRequest('basic'),
  'requests/get_routing.json': const GetRoutingRequest('r1'),
  'requests/get_group.json': const GetGroupRequest('Auto'),
  'requests/get_node.json': const GetNodeRequest('Tokyo'),
  'requests/routing_schema.json': const RoutingSchemaRequest(),
  'requests/set_active_profile.json': const SetActiveProfileRequest('main'),
  'requests/set_node_prefs_clear.json': const SetNodePrefsRequest('3f2a'),
  'requests/set_node_prefs_pin.json':
      const SetNodePrefsRequest('3f2a', coreOverride: CoreType.xray),
  'requests/upsert_routing.json': const UpsertRoutingRequest({
    'id': 'r1',
    'name': 'basic',
    'rule_sets': <Object?>[],
    'rules': <Object?>[],
    'default_target': 'DefaultProxy',
  }),
  'requests/upsert_group.json': const UpsertGroupRequest({
    'name': 'My Auto',
    'members': ['3f2a', '9b1c'],
    'probe': {'interval_sec': 180},
  }),
  'requests/get_settings.json': const GetSettingsRequest(),
  'requests/check_update.json': const CheckUpdateRequest(),
  'requests/check_update_force.json': const CheckUpdateRequest(force: true),
  'requests/download_update.json': const DownloadUpdateRequest(),
  'requests/apply_update.json': const ApplyUpdateRequest(),
  'requests/set_settings.json': SetSettingsRequest(Settings(
    logLevel: 'warn',
    ipVersion: 'v4',
    dns: DnsSettings(
        servers: [DnsServer(type: 'udp', address: '1.1.1.1')],
        strategy: 'ipv4_only',
        viaTunnel: true),
    lanBypass: LanBypassSettings(
        enabled: true, cidrs: ['10.0.0.0/8', '192.168.0.0/16']),
    restoreOnStart: false,
    subscriptionUserAgent: 'clash-verge/1.7',
    socksPort: 1080,
    tun: TunSettings(mtu: 9000, stack: 'system', strictRoute: false),
    latencyProbe: ProbeSettings(
        url: 'https://cp.cloudflare.com/generate_204', budgetSecs: 20),
    autoUpdate: true,
    updateViaTunnel: false,
    updateViaDirect: true,
  )),
};

/// Response fixtures → assertions on the lenient decode.
final responseCases = <String, void Function(Response)>{
  'responses/activated.json': (r) {
    r as ActivatedResponse;
    expect(r.entries.map((e) => e.role), [CoreRole.tun, CoreRole.proxy]);
    expect(r.entries.every((e) => e.isRunning), isTrue);
  },
  'responses/error.json': (r) {
    expect((r as ErrorResponse).message, 'no such node: osaka');
  },
  'responses/idle.json': (r) {
    expect((r as IdleResponse).daemonVersion, 'v0.1.0');
  },
  'responses/latencies.json': (r) {
    r as LatenciesResponse;
    expect(r.latencies, hasLength(2));
    expect(r.latencies[0].latencyMs, 58);
    expect(r.latencies[1].latencyMs, isNull);
  },
  'responses/ok.json': (r) => expect((r as OkResponse).message, 'removed'),
  'responses/ok_add_node.json': (r) {
    r as OkResponse;
    expect(r.message, 'node added');
    expect(r.node, 'node-3f2a');
  },
  'responses/running_full.json': (r) {
    r as RunningResponse;
    expect(r.entries, hasLength(2));
    expect(r.entries[0].uptimeSecs, 42);
    expect(r.active?.profile, 'main');
    expect(r.active?.tun, isFalse);
    expect(r.activeNodeLive, 'Grimnir [VLESS - tcp]');
    expect(r.daemonVersion, 'v0.1.0');
  },
  'responses/running_no_context.json': (r) {
    r as RunningResponse;
    expect(r.entries, hasLength(1));
    expect(r.active, isNull);
    expect(r.activeNodeLive, isNull);
    expect(r.daemonVersion, 'v0.1.0');
  },
  'responses/running_group.json': (r) {
    r as RunningResponse;
    // Active node is the group; the live pick is a different member.
    expect(r.active?.node, 'Auto');
    expect(r.activeNodeLive, 'Frankfurt');
    expect(r.activeNodeLive, isNot(r.active?.node));
    expect(r.daemonVersion, 'v0.1.0');
  },
  'responses/started.json': (r) {
    r as StartedResponse;
    expect(r.role, CoreRole.tun);
  },
  'responses/stopped_bare.json': (r) => expect(r, isA<StoppedResponse>()),
  'responses/switched.json': (r) {
    expect((r as SwitchedResponse).node, 'osaka');
  },
  'responses/diagnosis.json': (r) {
    final d = (r as DiagnosisResponse).diagnosis!;
    expect(d.node, 'tokyo');
    expect(d.ok, isFalse);
    expect(d.failedStage, 'tls');
    expect(d.stages, hasLength(2));
    expect(d.stages[1].error, 'handshake timeout');
  },
  'responses/app_traffic.json': (r) {
    final apps = (r as AppTrafficResponse).apps;
    expect(apps, hasLength(2));
    expect(apps[0].path, '/usr/bin/firefox');
    expect(apps[0].up, 524288);
    expect(apps[0].down, 8388608);
    // firefox is split across proxy + direct; the totals == the sum.
    expect(apps[0].byRoute.keys.toSet(), {'proxy', 'direct'});
    expect(apps[0].byRoute['proxy']!.up, 491520);
    expect(apps[0].byRoute['proxy']!.down, 8126464);
    expect(apps[0].byRoute['direct']!.up, 32768);
    expect(apps[0].byRoute['direct']!.down, 262144);
    expect(apps[1].path, '/usr/lib/telegram/telegram');
    // telegram is proxy-only.
    expect(apps[1].byRoute.keys.toSet(), {'proxy'});
    expect(apps[1].byRoute['proxy']!.up, 16384);
  },
  'responses/doctor_report.json': (r) {
    final checks = (r as DoctorReportResponse).checks;
    expect(checks, hasLength(2));
    final internet = checks[0];
    expect(internet.id, 'internet');
    expect(internet.status, 'warn');
    expect(internet.isWarn, isTrue);
    expect(internet.details, hasLength(4));
    expect(internet.remedy, isNotNull);
    expect(internet.remedy!.dnsStrategy, 'prefer_ipv6');
    final resolvers = checks[1];
    expect(resolvers.id, 'resolvers');
    expect(resolvers.status, 'warn');
    expect(resolvers.remedy, isNotNull);
    // The resolver remedy promotes a reachable upstream — a full server list.
    expect(resolvers.remedy!.dnsStrategy, isNull);
    expect(resolvers.remedy!.dnsServers, isNotNull);
    expect(resolvers.remedy!.dnsServers!.map((s) => s.address),
        ['1.1.1.1', '8.8.8.8']);
    expect(resolvers.remedy!.dnsServers!.first.type, 'tls');
  },
  'responses/nodes.json': (r) {
    final nodes = (r as NodesResponse).nodes;
    expect(nodes, hasLength(3));
    expect(nodes[0].coreOverride, CoreType.xray);
    expect(nodes[0].active, isTrue);
    expect(nodes[1].subId, isNull);
    expect(nodes[1].transport, 'xhttp');
    expect(nodes[0].port, 443);
    expect(nodes[1].port, 8443);
    // Leniency: a group row omits the port.
    expect(nodes[2].port, 0);
    expect(nodes[0].eligibleCores, [CoreType.singBox, CoreType.xray]);
    expect(nodes[1].eligibleCores, [CoreType.xray]);
    // Leniency: a dialable row has no kind/members.
    expect(nodes[0].kind, '');
    expect(nodes[0].isGroup, isFalse);
    expect(nodes[0].members, isEmpty);
    // The group row.
    expect(nodes[2].isGroup, isTrue);
    expect(nodes[2].name, 'Auto');
    expect(nodes[2].subId, 's1');
    expect(nodes[2].members, ['3f2a']);
    expect(nodes[2].protocol, '');
    expect(nodes[2].eligibleCores, isEmpty);
  },
  'responses/routing_config.json': (r) {
    final cfg = (r as RoutingConfigResponse).routingConfig;
    expect(cfg['id'], 'r1');
    expect(cfg['name'], 'basic');
    expect(cfg['default_target'], 'Direct');
    expect(cfg['rules'], hasLength(1));
  },
  'responses/group_config.json': (r) {
    final g = (r as GroupConfigResponse).groupConfig;
    expect(g['name'], 'Auto');
    expect(g['all_of_sub'], 's1');
    expect((g['probe'] as Map)['interval_sec'], 180);
    expect(g.containsKey('members'), isFalse);
  },
  'responses/node_config.json': (r) {
    final c = (r as NodeConfigResponse).nodeConfig;
    expect(c['name'], 'Tokyo');
    expect(c['protocol'], 'vless');
    final profile = c['profile'] as Map<String, Object?>;
    expect(profile['address'], '198.51.100.7');
    expect(profile['port'], 443);
    // The nested security front is preserved for the inspector to flatten.
    final security = profile['security'] as Map<String, Object?>;
    expect(security['kind'], 'reality');
    expect((security['reality'] as Map)['sni'], 'example.com');
  },
  'responses/routing_schema.json': (r) {
    final s = (r as RoutingSchemaResponse).routingSchema;
    expect(s.conditions, hasLength(33));
    final byKey = {for (final c in s.conditions) c.key: c};
    expect(byKey['domain']!.kind, 'strings');
    expect(byKey['port']!.kind, 'ports');
    expect(byKey['ip_is_private']!.kind, 'bool');
    expect(byKey['user_id']!.kind, 'numbers');
    expect(byKey['clash_mode']!.kind, 'string');
    expect(byKey['process_name']!.supported, isTrue);
    expect(byKey['package_name']!.supported, isFalse);
    expect(s.targets, ['DefaultProxy', 'Direct', 'Block', 'Node']);
    expect(s.ruleSetFormats, ['binary', 'source']);
  },
  'responses/profiles.json': (r) {
    r as ProfilesResponse;
    expect(r.profiles, ['main', 'work']);
    expect(r.activeProfile, 'main');
  },
  'responses/refreshed.json': (r) {
    final refreshed = (r as RefreshedResponse).refreshed;
    expect(refreshed, hasLength(3));
    // The row that fetched carries the parse accounting…
    expect(refreshed[0].format, 'xray');
    expect(refreshed[0].entries, 45);
    expect(refreshed[0].duplicates, 2);
    expect(refreshed[0].unrecognized, 1);
    // …skip/error rows have none — the fields default to ""/0.
    expect(refreshed[1].skipped, isTrue);
    expect(refreshed[1].format, isEmpty);
    expect(refreshed[1].entries, 0);
    expect(refreshed[2].error, 'fetch: status 502');
    expect(refreshed[2].format, isEmpty);
  },
  'responses/routing.json': (r) {
    final routing = (r as RoutingResponse).routing;
    expect(routing, hasLength(2));
    expect(routing[0].ruleSets, 2);
    expect(routing[0].active, isTrue);
  },
  'responses/subscriptions.json': (r) {
    final subs = (r as SubscriptionsResponse).subscriptions;
    expect(subs, hasLength(2));
    expect(subs[0].id, 's1');
    expect(subs[0].enabled, isTrue);
    expect(subs[0].lastUpdated, '2026-06-10T12:00:00Z');
    expect(subs[0].nodeCount, 42);
    expect(subs[0].format, 'auto');
    expect(subs[0].updateIntervalSec, 28800);
    // last_error is omitted while clear.
    expect(subs[0].lastError, isNull);
    expect(subs[1].id, 's2');
    expect(subs[1].enabled, isFalse);
    expect(subs[1].allowInvalidCerts, isTrue);
    expect(subs[1].lastUpdated, '2026-06-09T12:00:00Z');
    expect(subs[1].nodeCount, 0);
    expect(subs[1].format, 'links');
    expect(subs[1].updateIntervalSec, 86400);
    expect(subs[1].lastError, 'subscription fetch: HTTP 503');
  },
  'responses/settings.json': (r) {
    final s = (r as SettingsResponse).settings;
    expect(r.needsReactivation, isFalse);
    expect(s.logLevel, 'info');
    expect(s.ipVersion, 'both');
    expect(s.dns.servers.single.type, 'tls');
    expect(s.dns.strategy, 'prefer_ipv4');
    expect(s.dns.viaTunnel, isFalse);
    expect(s.lanBypass.cidrs, hasLength(3)); // link-local / ULA (RFC1918 moved to an immutable route rule)
    expect(s.socksPort, 10808);
    expect(s.tun.strictRoute, isTrue);
    expect(s.latencyProbe.budgetSecs, 45);
    // The update check defaults ON, tunnel-first with a direct fallback.
    expect(s.autoUpdate, isTrue);
    expect(s.updateViaTunnel, isTrue);
    expect(s.updateViaDirect, isTrue);
    // The document must survive an edit-and-resend round trip.
    expect(const DeepCollectionEquality().equals(
            Settings.fromJson(s.toJson()).toJson(), s.toJson()),
        isTrue);
  },
  'responses/settings_needs_reactivation.json': (r) {
    r as SettingsResponse;
    expect(r.needsReactivation, isTrue);
    expect(r.settings.socksPort, 1080);
  },
  'responses/update_status_none.json': (r) {
    final s = (r as UpdateStatusResponse).updateStatus;
    expect(s.checkedAt, '2026-08-27T12:00:00Z');
    expect(s.currentVersion, 'v1.2.3');
    expect(s.available, isFalse);
    expect(s.stale, isFalse);
    expect(s.latestVersion, isEmpty);
    expect(s.artifact, isNull);
    // Leniency: the whole download half is absent when nothing ran.
    expect(s.downloadState, isEmpty);
    expect(s.downloadReceived, 0);
    expect(s.setupPath, isNull);
    expect(s.transport, 'tunnel');
  },
  'responses/update_status_available.json': (r) {
    final s = (r as UpdateStatusResponse).updateStatus;
    expect(s.available, isTrue);
    expect(s.currentVersion, 'v1.0.0');
    expect(s.latestVersion, 'v1.2.3');
    expect(s.notesUrl, 'https://example.com/iron-link/notes/v1.2.3');
    final a = s.artifact!;
    expect(a.os, 'windows');
    expect(a.kind, 'installer');
    expect(a.name, 'iron-link-1.2.3-windows-amd64-setup.exe');
    expect(a.size, 12345678);
    expect(a.sha256, hasLength(64));
    expect(s.downloadState, isEmpty); // nothing downloaded yet
  },
  'responses/update_status_downloading.json': (r) {
    final s = (r as UpdateStatusResponse).updateStatus;
    expect(s.downloadState, 'downloading');
    // The only reply that carries the live byte counters.
    expect(s.downloadReceived, 4194304);
    expect(s.downloadTotal, 12345678);
    expect(s.artifact, isNotNull);
    expect(s.setupPath, isNull);
  },
  'responses/update_status_downloaded.json': (r) {
    final s = (r as UpdateStatusResponse).updateStatus;
    expect(s.downloadState, 'downloaded');
    // The byte counters exist only WHILE downloading — omitted here.
    expect(s.downloadReceived, 0);
    expect(s.downloadTotal, 0);
    expect(s.setupPath, isNull); // only an apply_update reply carries it
  },
  'responses/update_status_verified.json': (r) {
    final s = (r as UpdateStatusResponse).updateStatus;
    expect(s.downloadState, 'verified');
    expect(s.setupPath,
        r'C:\ProgramData\iron-link\updates\iron-link-1.2.3-windows-amd64-setup.exe');
  },
};

/// Event fixtures → assertions on the lenient decode.
final eventCases = <String, void Function(Event)>{
  'events/log.json': (e) {
    e as LogEvent;
    expect(e.level, 'warning');
    expect(e.message, 'outbound timeout');
  },
  'events/state_idle.json': (e) {
    e as StateEvent;
    expect(e.entries, isEmpty);
    expect(e.active, isNull);
    expect(e.isRunning, isFalse);
  },
  'events/state_running.json': (e) {
    e as StateEvent;
    expect(e.isRunning, isTrue);
    expect(e.active?.node, 'Grimnir [VLESS - tcp]');
    // The daemon pushes the live node on the state event (an urltest
    // auto-switch reaches the client without the status poll).
    expect(e.activeNodeLive, 'Grimnir [VLESS - tcp]');
  },
  'events/traffic.json': (e) {
    e as TrafficEvent;
    expect(e.up, 4096);
    expect(e.down, 1048576);
  },
  'events/traffic_zero.json': (e) {
    e as TrafficEvent;
    expect(e.up, 0);
    expect(e.down, 0);
  },
  'events/core_error.json': (e) {
    e as CoreErrorEvent;
    expect(e.role, CoreRole.proxy);
    expect(e.stage, 'start');
  },
  'events/subscription_updated.json': (e) {
    e as SubscriptionUpdatedEvent;
    expect(e.added, 3);
    expect(e.total, 42);
    expect(e.format, 'xray');
  },
  'events/subscription_updated_zero.json': (e) {
    e as SubscriptionUpdatedEvent;
    expect(e.added, 0); // omitted zero counts default
    expect(e.total, 42);
    expect(e.format, isEmpty); // omitted format defaults too
  },
  'events/update_available.json': (e) {
    e as UpdateAvailableEvent;
    expect(e.version, 'v1.2.3');
    expect(e.notesUrl, 'https://example.com/iron-link/notes/v1.2.3');
    expect(e.kind, 'installer');
  },
  'events/update_progress.json': (e) {
    e as UpdateProgressEvent;
    expect(e.state, 'downloading');
    expect(e.received, 4194304);
    expect(e.total, 12345678);
  },
  'events/update_progress_done.json': (e) {
    e as UpdateProgressEvent;
    expect(e.state, 'downloaded');
    expect(e.received, e.total);
    expect(e.total, 12345678);
  },
};

void main() {
  const deepEq = DeepCollectionEquality();

  group('requests emit exactly', () {
    requestCases.forEach((fixture, request) {
      test(fixture, () {
        final expected = readFixture(fixture);
        final actual = request.toJson();
        expect(deepEq.equals(actual, expected), isTrue,
            reason: 'emitted ${jsonEncode(actual)}\n'
                'fixture ${jsonEncode(expected)}');
        // Null-vs-omitted is part of the contract — deep equality alone
        // would let {"role": null} pass for {}; pin the key sets too.
        expect(actual.keys.toSet(), expected.keys.toSet());
      });
    });
  });

  group('responses decode leniently', () {
    responseCases.forEach((fixture, check) {
      test(fixture, () => check(Response.fromJson(readFixture(fixture))));
    });
  });

  group('events decode leniently', () {
    eventCases.forEach((fixture, check) {
      test(fixture, () => check(Event.fromJson(readFixture(fixture))));
    });
  });

  group('forward compatibility', () {
    test('unknown response tag maps to UnknownResponse', () {
      final r = Response.fromJson({'status': 'from_the_future', 'x': 1});
      expect((r as UnknownResponse).status, 'from_the_future');
    });
    test('unknown event tag maps to UnknownEvent', () {
      expect(Event.fromJson({'event': 'from_the_future'}), isA<UnknownEvent>());
    });
    test('unknown fields are ignored', () {
      final r = Response.fromJson(
          {'status': 'switched', 'node': 'osaka', 'novel_field': true});
      expect((r as SwitchedResponse).node, 'osaka');
    });
    test('daemon_version is optional — an old daemon omits it', () {
      final idle = Response.fromJson({'status': 'idle'});
      expect((idle as IdleResponse).daemonVersion, isNull);
      final running = Response.fromJson({'status': 'running', 'entries': []});
      expect((running as RunningResponse).daemonVersion, isNull);
    });
  });

  test('coverage: every fixture has a case and every case a fixture', () {
    // listSync yields the platform separator (backslash on Windows); the
    // case-table keys are POSIX-style, so normalize before comparing.
    final prefix = '$fixturesRoot/';
    final onDisk = Directory(fixturesRoot)
        .listSync(recursive: true)
        .whereType<File>()
        .map((f) => f.path.replaceAll(r'\', '/'))
        .where((path) => path.endsWith('.json'))
        .map((path) => path.substring(prefix.length))
        .toSet();
    final covered = {
      ...requestCases.keys,
      ...responseCases.keys,
      ...eventCases.keys,
    };
    expect(covered.difference(onDisk), isEmpty,
        reason: 'cases without a fixture file');
    expect(onDisk.difference(covered), isEmpty,
        reason: 'fixture files without a Dart case — extend the tables');
  });
}
