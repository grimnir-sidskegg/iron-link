part of 'wire.dart';

/// Wire spellings for [CoreEntry.role] / the `stop` role — `daemon-go`'s
/// `CoreRole`. Kept as strings (not an enum) so an unknown future role
/// decodes losslessly.
abstract final class CoreRole {
  static const proxy = 'Proxy';
  static const tun = 'Tun';
  static const dpi = 'Dpi';
}

/// Wire spellings for the per-node core pin — `daemon-go`'s `CoreType`.
abstract final class CoreType {
  static const xray = 'Xray';
  static const singBox = 'SingBox';
}

// ---- lenient decode helpers -------------------------------------------------

String _str(Object? v, [String fallback = '']) => v is String ? v : fallback;

String? _strOpt(Object? v) => v is String ? v : null;

int _int(Object? v, [int fallback = 0]) => v is num ? v.toInt() : fallback;

int? _intOpt(Object? v) => v is num ? v.toInt() : null;

bool _bool(Object? v, [bool fallback = false]) => v is bool ? v : fallback;

List<Map<String, Object?>> _objList(Object? v) => v is List
    ? v.whereType<Map<String, Object?>>().toList(growable: false)
    : const [];

List<String> _strList(Object? v) =>
    v is List ? v.whereType<String>().toList(growable: false) : const [];

// ---- shared models (names mirror daemon-go/internal/api) --------------------

/// One embedded core in the session: logical role + state, no pid (the
/// cores are in-process).
class CoreEntry {
  const CoreEntry({required this.role, required this.state, this.uptimeSecs = 0});

  CoreEntry.fromJson(Map<String, Object?> json)
      : role = _str(json['role']),
        state = _str(json['state']),
        uptimeSecs = _int(json['uptime_secs']);

  final String role;
  final String state;
  final int uptimeSecs;

  bool get isRunning => state == 'running';
}

/// The activation INTENT — names + the tun flag, never paths/argv.
class PersistedEntry {
  const PersistedEntry({this.profile, this.node, this.routing, this.tun = false});

  PersistedEntry.fromJson(Map<String, Object?> json)
      : profile = _strOpt(json['profile']),
        node = _strOpt(json['node']),
        routing = _strOpt(json['routing']),
        tun = _bool(json['tun']);

  final String? profile;
  final String? node;
  final String? routing;
  final bool tun;
}

/// One node's latency-probe outcome; a null [latencyMs] is a timed-out or
/// unreachable probe (the request as a whole still succeeds).
class LatencyResult {
  const LatencyResult({required this.node, this.latencyMs});

  LatencyResult.fromJson(Map<String, Object?> json)
      : node = _str(json['node']),
        latencyMs = _intOpt(json['latency_ms']);

  final String node;
  final int? latencyMs;
}

/// One route outcome's cumulative up/down bytes within an [AppTraffic].
class RouteBytes {
  const RouteBytes({this.up = 0, this.down = 0});

  RouteBytes.fromJson(Map<String, Object?> json)
      : up = _int(json['up']),
        down = _int(json['down']);

  final int up;
  final int down;
}

/// One app's CUMULATIVE routed bytes since session start (`app_traffic`),
/// keyed by the owning process's executable [path] (empty = unattributed).
/// [up]/[down] are the totals; [byRoute] splits them per route outcome
/// ("direct"/"proxy"/"blocked"/"dpi") and carries only outcomes with nonzero
/// bytes. The UI derives per-app rates from the cumulative deltas between polls.
class AppTraffic {
  const AppTraffic(
      {this.path = '', this.up = 0, this.down = 0, this.byRoute = const {}});

  AppTraffic.fromJson(Map<String, Object?> json)
      : path = _str(json['path']),
        up = _int(json['up']),
        down = _int(json['down']),
        byRoute = _routeBytes(json['by_route']);

  final String path;
  final int up;
  final int down;
  final Map<String, RouteBytes> byRoute;
}

/// Decodes the `by_route` map (outcome → up/down); empty for a missing or
/// malformed field.
Map<String, RouteBytes> _routeBytes(Object? v) {
  if (v is! Map) return const {};
  final out = <String, RouteBytes>{};
  v.forEach((key, value) {
    if (key is String && value is Map<String, Object?>) {
      out[key] = RouteBytes.fromJson(value);
    }
  });
  return out;
}

/// One connection-doctor check's outcome — one row on the Doctor tab.
/// [status] is "ok" / "warn" / "fail"; [summary] the one-line verdict;
/// [details] the supporting facts. [remedy], when present, is a settings
/// change the UI offers to apply.
class DoctorCheck {
  const DoctorCheck({
    this.id = '',
    this.title = '',
    this.status = 'ok',
    this.summary = '',
    this.details = const [],
    this.remedy,
  });

  DoctorCheck.fromJson(Map<String, Object?> json)
      : id = _str(json['id']),
        title = _str(json['title']),
        status = _str(json['status'], 'ok'),
        summary = _str(json['summary']),
        details = _strList(json['details']),
        remedy = json['remedy'] is Map<String, Object?>
            ? DoctorRemedy.fromJson(json['remedy'] as Map<String, Object?>)
            : null;

  final String id;
  final String title;
  final String status;
  final String summary;
  final List<String> details;
  final DoctorRemedy? remedy;

  bool get isOk => status == 'ok';
  bool get isWarn => status == 'warn';
  bool get isFail => status == 'fail';
}

/// A suggested settings change for a warn/fail [DoctorCheck]. The client
/// applies whichever fields are non-null via `set_settings`; a given remedy
/// sets ONE lever. [dnsStrategy] → settings.dns.strategy; [dnsServers] REPLACES
/// settings.dns.servers (the resolver "switch upstream" fix, already reordered);
/// [dnsViaTunnel] → settings.dns.viaTunnel (the "route DNS through the tunnel"
/// fix).
class DoctorRemedy {
  const DoctorRemedy(
      {this.summary = '', this.dnsStrategy, this.dnsServers, this.dnsViaTunnel});

  DoctorRemedy.fromJson(Map<String, Object?> json)
      : summary = _str(json['summary']),
        dnsStrategy = _strOpt(json['dns_strategy']),
        dnsServers = json['dns_servers'] is List
            ? _objList(json['dns_servers'])
                .map(DnsServer.fromJson)
                .toList(growable: false)
            : null,
        dnsViaTunnel =
            json['dns_via_tunnel'] is bool ? json['dns_via_tunnel'] as bool : null;

  final String summary;
  final String? dnsStrategy;
  final List<DnsServer>? dnsServers;
  final bool? dnsViaTunnel;
}

/// One stored node, as listed by `list_nodes` — a THIN view (kinds, not
/// configs; no secrets cross the wire).
class NodeInfo {
  const NodeInfo({
    required this.id,
    required this.name,
    this.subId,
    this.coreOverride,
    this.protocol = '',
    this.transport = '',
    this.security = '',
    this.port = 0,
    this.active = false,
    this.eligibleCores = const [],
    this.kind = '',
    this.members = const [],
  });

  NodeInfo.fromJson(Map<String, Object?> json)
      : id = _str(json['id']),
        name = _str(json['name']),
        subId = _strOpt(json['sub_id']),
        coreOverride = _strOpt(json['core_override']),
        protocol = _str(json['protocol']),
        transport = _str(json['transport']),
        security = _str(json['security']),
        port = _int(json['port']),
        active = _bool(json['active']),
        eligibleCores = _strList(json['eligible_cores']),
        kind = _str(json['kind']),
        members = _strList(json['members']);

  final String id;
  final String name;
  final String? subId;

  /// A [CoreType] value, or null when no pin is set.
  final String? coreOverride;

  /// The protocol kind ("vless"/"shadowsocks"/"vmess"/…) — the primary badge.
  final String protocol;

  /// Variant kinds for the badges; either may be "" for a protocol with no
  /// such dimension (e.g. hysteria2 has no stream transport).
  final String transport;
  final String security;

  /// The server port, shown as a badge. Zero for a group row (no endpoint).
  final int port;
  final bool active;

  /// [CoreType] values that can dial this node, in daemon priority order.
  final List<String> eligibleCores;

  /// Row kind: "" (absent) = a dialable node, "group" = a member group (an
  /// "Auto" node). A group leaves [protocol]/[transport]/[security] "" and
  /// [eligibleCores] empty.
  final String kind;

  /// The resolved member node ids of a group row (empty for a dialable node).
  final List<String> members;

  /// Whether this row is a member group ("Auto") rather than a dialable node.
  bool get isGroup => kind == 'group';
}

/// One subscription, as listed by `list_subscriptions`.
class SubscriptionInfo {
  const SubscriptionInfo({
    required this.id,
    required this.name,
    required this.url,
    this.enabled = true,
    this.allowInvalidCerts = false,
    this.lastUpdated,
    this.nodeCount = 0,
    this.format = 'auto',
  });

  SubscriptionInfo.fromJson(Map<String, Object?> json)
      : id = _str(json['id']),
        name = _str(json['name']),
        url = _str(json['url']),
        enabled = _bool(json['enabled'], true),
        allowInvalidCerts = _bool(json['allow_invalid_certs']),
        lastUpdated = _strOpt(json['last_updated']),
        nodeCount = _int(json['node_count']),
        format = _str(json['format'], 'auto');

  final String id;
  final String name;
  final String url;
  final bool enabled;
  final bool allowInvalidCerts;

  /// RFC 3339, as emitted by the daemon.
  final String? lastUpdated;
  final int nodeCount;

  /// The pinned parse format ("links" / "xray" / "sing-box" / "clash" /
  /// "sip008"), or "auto" when unpinned. The daemon always sends it; the
  /// default only covers an older daemon that does not.
  final String format;
}

/// One routing-rule condition field from the daemon's `routing_schema`.
/// [kind] is a closed input-shape enum: "strings" / "ports" / "numbers" /
/// "bool" / "string" / "number"; [supported] = can match on the DAEMON's
/// platform.
class ConditionSpec {
  const ConditionSpec({
    required this.key,
    required this.kind,
    this.hint = '',
    this.supported = true,
  });

  ConditionSpec.fromJson(Map<String, Object?> json)
      : key = _str(json['key']),
        kind = _str(json['kind']),
        hint = _str(json['hint']),
        supported = _bool(json['supported']);

  final String key;
  final String kind;
  final String hint;
  final bool supported;
}

/// The routing-form schema served by the daemon: condition table + valid
/// rule targets + rule-set formats. Forms render from this data — no typed
/// routing model on the client.
class RoutingSchema {
  const RoutingSchema({
    this.conditions = const [],
    this.targets = const [],
    this.ruleSetFormats = const [],
  });

  RoutingSchema.fromJson(Map<String, Object?> json)
      : conditions = _objList(json['conditions'])
            .map(ConditionSpec.fromJson)
            .toList(growable: false),
        targets = _strList(json['targets']),
        ruleSetFormats = _strList(json['rule_set_formats']);

  final List<ConditionSpec> conditions;
  final List<String> targets;
  final List<String> ruleSetFormats;
}

/// One routing config summary, as listed by `list_routing`.
class RoutingInfo {
  const RoutingInfo({
    required this.id,
    required this.name,
    this.rules = 0,
    this.ruleSets = 0,
    this.active = false,
  });

  RoutingInfo.fromJson(Map<String, Object?> json)
      : id = _str(json['id']),
        name = _str(json['name']),
        rules = _int(json['rules']),
        ruleSets = _int(json['rule_sets']),
        active = _bool(json['active']);

  final String id;
  final String name;
  final int rules;
  final int ruleSets;
  final bool active;
}

/// One subscription's refresh outcome.
class RefreshInfo {
  const RefreshInfo({
    required this.name,
    this.count = 0,
    this.added = 0,
    this.removed = 0,
    this.skipped = false,
    this.error,
    this.format = '',
    this.entries = 0,
    this.duplicates = 0,
    this.unrecognized = 0,
  });

  RefreshInfo.fromJson(Map<String, Object?> json)
      : name = _str(json['name']),
        count = _int(json['count']),
        added = _int(json['added']),
        removed = _int(json['removed']),
        skipped = _bool(json['skipped']),
        error = _strOpt(json['error']),
        format = _str(json['format']),
        entries = _int(json['entries']),
        duplicates = _int(json['duplicates']),
        unrecognized = _int(json['unrecognized']);

  final String name;
  final int count;
  final int added;
  final int removed;
  final bool skipped;
  final String? error;

  /// Parse accounting for a fetch that ran: the format that parsed the
  /// payload, raw [entries] seen, [duplicates] / [unrecognized] dropped.
  /// The daemon omits zero values and sends none of these on skip/error
  /// rows (or from an older daemon) — all default to ""/0.
  final String format;
  final int entries;
  final int duplicates;
  final int unrecognized;
}

/// The daemon-global settings document (`get_settings` / `set_settings`).
/// Deliberately MUTABLE: there are no patch semantics on the wire — the UI
/// edits the document `get_settings` returned and sends the whole thing
/// back. Defaults mirror the daemon's `store.DefaultSettings`; the daemon is
/// the source of truth and always emits every field, so the literals here are
/// only fallbacks for an absent key. One default is per-OS on the daemon:
/// `restore_on_start` defaults OFF on Windows (auto-start service) and ON
/// elsewhere — the toggle shows whatever the daemon returned, so it reflects
/// that per-OS default without the client encoding it.
class Settings {
  Settings({
    this.logLevel = 'info',
    this.ipVersion = 'both',
    DnsSettings? dns,
    LanBypassSettings? lanBypass,
    this.restoreOnStart = true,
    this.subscriptionUserAgent = 'v2rayN/6.23',
    this.socksPort = 10808,
    TunSettings? tun,
    ProbeSettings? latencyProbe,
    this.autoUpdate = true,
    this.updateViaTunnel = true,
    this.updateViaDirect = true,
  })  : dns = dns ?? DnsSettings(),
        lanBypass = lanBypass ?? LanBypassSettings(),
        tun = tun ?? TunSettings(),
        latencyProbe = latencyProbe ?? ProbeSettings();

  factory Settings.fromJson(Map<String, Object?> json) => Settings(
        logLevel: _str(json['log_level'], 'info'),
        ipVersion: _str(json['ip_version'], 'both'),
        dns: json['dns'] is Map<String, Object?>
            ? DnsSettings.fromJson(json['dns'] as Map<String, Object?>)
            : null,
        lanBypass: json['lan_bypass'] is Map<String, Object?>
            ? LanBypassSettings.fromJson(json['lan_bypass'] as Map<String, Object?>)
            : null,
        restoreOnStart: _bool(json['restore_on_start'], true),
        subscriptionUserAgent:
            _str(json['subscription_user_agent'], 'v2rayN/6.23'),
        socksPort: _int(json['socks_port'], 10808),
        tun: json['tun'] is Map<String, Object?>
            ? TunSettings.fromJson(json['tun'] as Map<String, Object?>)
            : null,
        latencyProbe: json['latency_probe'] is Map<String, Object?>
            ? ProbeSettings.fromJson(json['latency_probe'] as Map<String, Object?>)
            : null,
        autoUpdate: _bool(json['auto_update'], true),
        updateViaTunnel: _bool(json['update_via_tunnel'], true),
        updateViaDirect: _bool(json['update_via_direct'], true),
      );

  /// "error" / "warn" / "info" / "debug".
  String logLevel;

  /// "v4" / "v6" / "both".
  String ipVersion;
  DnsSettings dns;
  LanBypassSettings lanBypass;
  bool restoreOnStart;
  String subscriptionUserAgent;
  int socksPort;
  TunSettings tun;
  ProbeSettings latencyProbe;

  /// Master switch of the periodic update check (fail-soft; the daemon
  /// never installs anything by itself).
  bool autoUpdate;

  /// Reach the update manifest through the active session (tunnel-first).
  bool updateViaTunnel;

  /// Allow a TUN-exempt direct dial when no session is up (or via-tunnel
  /// is off). Both via-flags off disables the check's transport entirely.
  bool updateViaDirect;

  Map<String, Object?> toJson() => {
        'log_level': logLevel,
        'ip_version': ipVersion,
        'dns': dns.toJson(),
        'lan_bypass': lanBypass.toJson(),
        'restore_on_start': restoreOnStart,
        'subscription_user_agent': subscriptionUserAgent,
        'socks_port': socksPort,
        'tun': tun.toJson(),
        'latency_probe': latencyProbe.toJson(),
        'auto_update': autoUpdate,
        'update_via_tunnel': updateViaTunnel,
        'update_via_direct': updateViaDirect,
      };
}

/// DNS upstreams; the FIRST answers queries today.
class DnsSettings {
  DnsSettings(
      {List<DnsServer>? servers,
      this.strategy = 'prefer_ipv4',
      this.viaTunnel = false})
      : servers = servers ?? [DnsServer(type: 'tls', address: '8.8.8.8')];

  factory DnsSettings.fromJson(Map<String, Object?> json) => DnsSettings(
        servers: _objList(json['servers'])
            .map(DnsServer.fromJson)
            .toList(), // growable: the UI edits this list in place
        // Any empty/legacy/unknown value coerces to the default so it always
        // matches a settings dropdown option (the daemon tolerates "").
        strategy: switch (_str(json['strategy'])) {
          'prefer_ipv6' => 'prefer_ipv6',
          'ipv4_only' => 'ipv4_only',
          'ipv6_only' => 'ipv6_only',
          _ => 'prefer_ipv4',
        },
        viaTunnel: _bool(json['via_tunnel']),
      );

  List<DnsServer> servers;

  /// Record strategy, decoupled from the TUN's IP version: 'prefer_ipv4'
  /// (the default), 'prefer_ipv6', 'ipv4_only', 'ipv6_only'. On a dual-stack
  /// network this stabilizes which family apps reach over.
  String strategy;

  /// Queries detour through the tunnel; node-domain bootstrap stays
  /// direct on the daemon side (structural chicken-and-egg).
  bool viaTunnel;

  Map<String, Object?> toJson() => {
        'servers': [for (final s in servers) s.toJson()],
        'strategy': strategy,
        'via_tunnel': viaTunnel,
      };
}

/// One DNS upstream: type "udp" / "tls" / "https" at a LITERAL IP.
class DnsServer {
  DnsServer({required this.type, required this.address});

  factory DnsServer.fromJson(Map<String, Object?> json) =>
      DnsServer(type: _str(json['type'], 'udp'), address: _str(json['address']));

  String type;
  String address;

  Map<String, Object?> toJson() => {'type': type, 'address': address};
}

/// Local-network bypass for the TUN.
class LanBypassSettings {
  LanBypassSettings({this.enabled = true, List<String>? cidrs})
      : cidrs = cidrs ?? defaultCidrs.toList();

  factory LanBypassSettings.fromJson(Map<String, Object?> json) =>
      LanBypassSettings(
        enabled: _bool(json['enabled'], true),
        cidrs: _strList(json['cidrs']).toList(),
      );

  // Link-local / ULA only — mirrors the daemon default. The RFC1918 IPv4
  // ranges are NOT excluded here: the daemon steers them direct via an
  // immutable route rule so DNS to a LAN resolver is hijacked, not leaked.
  static const defaultCidrs = [
    '169.254.0.0/16', 'fc00::/7', 'fe80::/10',
  ];

  bool enabled;
  List<String> cidrs;

  Map<String, Object?> toJson() => {'enabled': enabled, 'cidrs': cidrs};
}

/// TUN inbound knobs.
class TunSettings {
  TunSettings({this.mtu = 1500, this.stack = 'mixed', this.strictRoute = true});

  factory TunSettings.fromJson(Map<String, Object?> json) => TunSettings(
        mtu: _int(json['mtu'], 1500),
        stack: _str(json['stack'], 'mixed'),
        strictRoute: _bool(json['strict_route'], true),
      );

  int mtu;

  /// "system" / "gvisor" / "mixed".
  String stack;
  bool strictRoute;

  Map<String, Object?> toJson() =>
      {'mtu': mtu, 'stack': stack, 'strict_route': strictRoute};
}

/// Latency-probe endpoint + budget.
class ProbeSettings {
  ProbeSettings({
    this.url = 'https://www.gstatic.com/generate_204',
    this.budgetSecs = 45,
  });

  factory ProbeSettings.fromJson(Map<String, Object?> json) => ProbeSettings(
        url: _str(json['url'], 'https://www.gstatic.com/generate_204'),
        budgetSecs: _int(json['budget_secs'], 45),
      );

  String url;
  int budgetSecs;

  Map<String, Object?> toJson() => {'url': url, 'budget_secs': budgetSecs};
}

/// The downloadable file of an available update — the signed-manifest facts
/// the UI may display (name/size) and the daemon verifies downloads against.
/// Deliberately no URLs: the daemon downloads from its own verified
/// manifest, the wire never carries locations.
class UpdateArtifact {
  const UpdateArtifact({
    this.os = '',
    this.arch = '',
    this.kind = '',
    this.name = '',
    this.size = 0,
    this.sha256 = '',
  });

  UpdateArtifact.fromJson(Map<String, Object?> json)
      : os = _str(json['os']),
        arch = _str(json['arch']),
        kind = _str(json['kind']),
        name = _str(json['name']),
        size = _int(json['size']),
        sha256 = _str(json['sha256']);

  final String os;
  final String arch;

  /// "installer", or "none" for a notify-only channel.
  final String kind;
  final String name;
  final int size;
  final String sha256;
}

/// The daemon's update verdict + download state machine (`update_status` —
/// the reply of `check_update` / `download_update` / `apply_update`).
class UpdateStatus {
  const UpdateStatus({
    this.checkedAt,
    this.currentVersion = '',
    this.available = false,
    this.stale = false,
    this.latestVersion = '',
    this.notesUrl = '',
    this.artifact,
    this.downloadState = '',
    this.downloadReceived = 0,
    this.downloadTotal = 0,
    this.setupPath,
    this.transport = '',
  });

  UpdateStatus.fromJson(Map<String, Object?> json)
      : checkedAt = _strOpt(json['checked_at']),
        currentVersion = _str(json['current_version']),
        available = _bool(json['available']),
        stale = _bool(json['stale']),
        latestVersion = _str(json['latest_version']),
        notesUrl = _str(json['notes_url']),
        artifact = json['artifact'] is Map<String, Object?>
            ? UpdateArtifact.fromJson(json['artifact'] as Map<String, Object?>)
            : null,
        downloadState = _str(json['download_state']),
        downloadReceived = _int(json['download_received']),
        downloadTotal = _int(json['download_total']),
        setupPath = _strOpt(json['setup_path']),
        transport = _str(json['transport']);

  /// RFC 3339 timestamp of the last completed check; null before the first.
  final String? checkedAt;
  final String currentVersion;
  final bool available;

  /// The manifest is past its soft freshness horizon — informational only.
  final bool stale;
  final String latestVersion;
  final String notesUrl;

  /// The downloadable file for the daemon's platform; null when nothing is
  /// available or the channel is notify-only (Linux/macOS).
  final UpdateArtifact? artifact;

  /// "" (none) / "downloading" / "downloaded" / "verified" / "failed".
  final String downloadState;

  /// Live byte counters, nonzero only while downloading.
  final int downloadReceived;
  final int downloadTotal;

  /// The daemon-owned verified installer path — set only on a successful
  /// `apply_update` reply; the client launches it.
  final String? setupPath;

  /// What a check would use right now: "tunnel" / "direct" / "disabled".
  final String transport;

  /// A copy with the download progress patched in — the `update_progress`
  /// event stream moves only these three fields.
  UpdateStatus withProgress(
          {required String state, required int received, required int total}) =>
      UpdateStatus(
        checkedAt: checkedAt,
        currentVersion: currentVersion,
        available: available,
        stale: stale,
        latestVersion: latestVersion,
        notesUrl: notesUrl,
        artifact: artifact,
        downloadState: state,
        downloadReceived: received,
        downloadTotal: total,
        setupPath: setupPath,
        transport: transport,
      );
}

/// One diagnosis stage's outcome.
class StageInfo {
  const StageInfo({required this.stage, required this.ok, this.durationMs = 0, this.error});

  StageInfo.fromJson(Map<String, Object?> json)
      : stage = _str(json['stage']),
        ok = _bool(json['ok']),
        durationMs = _int(json['duration_ms']),
        error = _strOpt(json['error']);

  final String stage;
  final bool ok;
  final int durationMs;
  final String? error;
}

/// The staged-diagnosis verdict for one node.
class DiagnosisInfo {
  const DiagnosisInfo({
    required this.node,
    required this.ok,
    this.failedStage,
    this.stages = const [],
  });

  DiagnosisInfo.fromJson(Map<String, Object?> json)
      : node = _str(json['node']),
        ok = _bool(json['ok']),
        failedStage = _strOpt(json['failed_stage']),
        stages = _objList(json['stages'])
            .map(StageInfo.fromJson)
            .toList(growable: false);

  final String node;
  final bool ok;
  final String? failedStage;
  final List<StageInfo> stages;
}
