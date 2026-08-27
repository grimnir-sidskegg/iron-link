part of 'wire.dart';

/// A daemon → client reply frame, tagged by `status`.
///
/// Decoded leniently: an unknown tag becomes [UnknownResponse] (the natural
/// "I don't speak this" signal of an older client against a newer daemon).
sealed class Response {
  const Response();

  static Response fromJson(Map<String, Object?> json) =>
      switch (json['status']) {
        'ok' => OkResponse.fromJson(json),
        'error' => ErrorResponse.fromJson(json),
        'started' => StartedResponse.fromJson(json),
        'stopped' => const StoppedResponse(),
        'running' => RunningResponse.fromJson(json),
        'activated' => ActivatedResponse.fromJson(json),
        'idle' => IdleResponse.fromJson(json),
        'switched' => SwitchedResponse.fromJson(json),
        'latencies' => LatenciesResponse.fromJson(json),
        'profiles' => ProfilesResponse.fromJson(json),
        'nodes' => NodesResponse.fromJson(json),
        'subscriptions' => SubscriptionsResponse.fromJson(json),
        'refreshed' => RefreshedResponse.fromJson(json),
        'routing' => RoutingResponse.fromJson(json),
        'routing_config' => RoutingConfigResponse.fromJson(json),
        'group_config' => GroupConfigResponse.fromJson(json),
        'node_config' => NodeConfigResponse.fromJson(json),
        'routing_schema' => RoutingSchemaResponse.fromJson(json),
        'diagnosis' => DiagnosisResponse.fromJson(json),
        'app_traffic' => AppTrafficResponse.fromJson(json),
        'doctor_report' => DoctorReportResponse.fromJson(json),
        'settings' => SettingsResponse.fromJson(json),
        _ => UnknownResponse(json),
      };
}

/// `ok` — a store mutation succeeded; `add_node` also carries the new
/// node's id in [node].
final class OkResponse extends Response {
  const OkResponse({this.message = '', this.node});

  OkResponse.fromJson(Map<String, Object?> json)
      : message = _str(json['message']),
        node = _strOpt(json['node']);

  final String message;
  final String? node;
}

/// `error` — the daemon said no.
final class ErrorResponse extends Response {
  const ErrorResponse(this.message);

  ErrorResponse.fromJson(Map<String, Object?> json) : message = _str(json['message']);

  final String message;
}

/// `started` — legacy per-role start acknowledgement; kept decodable for
/// wire compatibility.
final class StartedResponse extends Response {
  const StartedResponse({this.message = '', this.role});

  StartedResponse.fromJson(Map<String, Object?> json)
      : message = _str(json['message']),
        role = _strOpt(json['role']);

  final String message;
  final String? role;
}

/// `stopped`.
final class StoppedResponse extends Response {
  const StoppedResponse();
}

/// `running` — the session snapshot. [activeNodeLive] is the in-process
/// selector readback (a `urltest` auto-switch shows up ONLY here, never as
/// a State event — hence the client's backstop poll).
final class RunningResponse extends Response {
  const RunningResponse(
      {this.entries = const [],
      this.active,
      this.activeNodeLive,
      this.daemonVersion});

  RunningResponse.fromJson(Map<String, Object?> json)
      : entries = _objList(json['entries'])
            .map(CoreEntry.fromJson)
            .toList(growable: false),
        active = json['active'] is Map<String, Object?>
            ? PersistedEntry.fromJson(json['active'] as Map<String, Object?>)
            : null,
        activeNodeLive = _strOpt(json['active_node_live']),
        daemonVersion = _strOpt(json['daemon_version']);

  final List<CoreEntry> entries;
  final PersistedEntry? active;
  final String? activeNodeLive;

  /// The daemon's build version; null from a daemon predating the field.
  final String? daemonVersion;
}

/// `activated` — the reply to a successful `activate`.
final class ActivatedResponse extends Response {
  const ActivatedResponse({this.entries = const []});

  ActivatedResponse.fromJson(Map<String, Object?> json)
      : entries = _objList(json['entries'])
            .map(CoreEntry.fromJson)
            .toList(growable: false);

  final List<CoreEntry> entries;
}

/// `idle` — no session is running.
final class IdleResponse extends Response {
  const IdleResponse({this.daemonVersion});

  IdleResponse.fromJson(Map<String, Object?> json)
      : daemonVersion = _strOpt(json['daemon_version']);

  /// The daemon's build version; null from a daemon predating the field.
  final String? daemonVersion;
}

/// `switched` — the node NAME now selected.
final class SwitchedResponse extends Response {
  const SwitchedResponse(this.node);

  SwitchedResponse.fromJson(Map<String, Object?> json) : node = _str(json['node']);

  final String node;
}

/// `latencies` — per-node probe outcomes; a failed probe is a null
/// latency, never a failed request.
final class LatenciesResponse extends Response {
  const LatenciesResponse({this.latencies = const []});

  LatenciesResponse.fromJson(Map<String, Object?> json)
      : latencies = _objList(json['latencies'])
            .map(LatencyResult.fromJson)
            .toList(growable: false);

  final List<LatencyResult> latencies;
}

/// `profiles`.
final class ProfilesResponse extends Response {
  const ProfilesResponse({this.profiles = const [], this.activeProfile});

  ProfilesResponse.fromJson(Map<String, Object?> json)
      : profiles = _strList(json['profiles']),
        activeProfile = _strOpt(json['active_profile']);

  final List<String> profiles;
  final String? activeProfile;
}

/// `nodes`.
final class NodesResponse extends Response {
  const NodesResponse({this.nodes = const []});

  NodesResponse.fromJson(Map<String, Object?> json)
      : nodes = _objList(json['nodes'])
            .map(NodeInfo.fromJson)
            .toList(growable: false);

  final List<NodeInfo> nodes;
}

/// `subscriptions`.
final class SubscriptionsResponse extends Response {
  const SubscriptionsResponse({this.subscriptions = const []});

  SubscriptionsResponse.fromJson(Map<String, Object?> json)
      : subscriptions = _objList(json['subscriptions'])
            .map(SubscriptionInfo.fromJson)
            .toList(growable: false);

  final List<SubscriptionInfo> subscriptions;
}

/// `refreshed` — per-subscription refresh outcomes (also the
/// `add_subscription` reply: the initial fetch).
final class RefreshedResponse extends Response {
  const RefreshedResponse({this.refreshed = const []});

  RefreshedResponse.fromJson(Map<String, Object?> json)
      : refreshed = _objList(json['refreshed'])
            .map(RefreshInfo.fromJson)
            .toList(growable: false);

  final List<RefreshInfo> refreshed;
}

/// `routing`.
final class RoutingResponse extends Response {
  const RoutingResponse({this.routing = const []});

  RoutingResponse.fromJson(Map<String, Object?> json)
      : routing = _objList(json['routing'])
            .map(RoutingInfo.fromJson)
            .toList(growable: false);

  final List<RoutingInfo> routing;
}

/// `routing_config` — ONE routing config as its full JSON document (the
/// `get_routing` reply); edited as data and sent back via `upsert_routing`.
final class RoutingConfigResponse extends Response {
  const RoutingConfigResponse({this.routingConfig = const {}});

  RoutingConfigResponse.fromJson(Map<String, Object?> json)
      : routingConfig = switch (json['routing_config']) {
          final Map<String, Object?> m => m,
          _ => const {},
        };

  final Map<String, Object?> routingConfig;
}

/// `group_config` — ONE user group's stored spec ({name, members?/all_of_sub?,
/// probe}), the `get_group` reply; pre-fills the group editor and is sent back
/// via `upsert_group`.
final class GroupConfigResponse extends Response {
  const GroupConfigResponse({this.groupConfig = const {}});

  GroupConfigResponse.fromJson(Map<String, Object?> json)
      : groupConfig = switch (json['group_config']) {
          final Map<String, Object?> m => m,
          _ => const {},
        };

  final Map<String, Object?> groupConfig;
}

/// `node_config` — ONE dialable node's full stored config ({name, protocol,
/// profile:{…every configured field…}}), the `get_node` reply behind the
/// read-only node inspector.
final class NodeConfigResponse extends Response {
  const NodeConfigResponse({this.nodeConfig = const {}});

  NodeConfigResponse.fromJson(Map<String, Object?> json)
      : nodeConfig = switch (json['node_config']) {
          final Map<String, Object?> m => m,
          _ => const {},
        };

  final Map<String, Object?> nodeConfig;
}

/// `routing_schema` — the routing-form schema (see [RoutingSchema]).
final class RoutingSchemaResponse extends Response {
  const RoutingSchemaResponse({this.routingSchema = const RoutingSchema()});

  RoutingSchemaResponse.fromJson(Map<String, Object?> json)
      : routingSchema = switch (json['routing_schema']) {
          final Map<String, Object?> m => RoutingSchema.fromJson(m),
          _ => const RoutingSchema(),
        };

  final RoutingSchema routingSchema;
}

/// `diagnosis`.
final class DiagnosisResponse extends Response {
  const DiagnosisResponse({this.diagnosis});

  DiagnosisResponse.fromJson(Map<String, Object?> json)
      : diagnosis = json['diagnosis'] is Map<String, Object?>
            ? DiagnosisInfo.fromJson(json['diagnosis'] as Map<String, Object?>)
            : null;

  final DiagnosisInfo? diagnosis;
}

/// `app_traffic` — the session's cumulative per-app byte counts (the
/// `traffic_apps` reply); empty when no session runs.
final class AppTrafficResponse extends Response {
  const AppTrafficResponse({this.apps = const []});

  AppTrafficResponse.fromJson(Map<String, Object?> json)
      : apps = _objList(json['apps'])
            .map(AppTraffic.fromJson)
            .toList(growable: false);

  final List<AppTraffic> apps;
}

/// `doctor_report` — the ordered connection-doctor checks (the `doctor`
/// reply); each is one row on the Doctor tab.
final class DoctorReportResponse extends Response {
  const DoctorReportResponse({this.checks = const []});

  DoctorReportResponse.fromJson(Map<String, Object?> json)
      : checks = _objList(json['checks'])
            .map(DoctorCheck.fromJson)
            .toList(growable: false);

  final List<DoctorCheck> checks;
}

/// `settings` — the effective document (both verbs reply with it).
/// [needsReactivation] is set by `set_settings` when an engine-relevant
/// field changed while a session runs: the new value applies at the next
/// activation.
final class SettingsResponse extends Response {
  SettingsResponse({Settings? settings, this.needsReactivation = false})
      : settings = settings ?? Settings();

  SettingsResponse.fromJson(Map<String, Object?> json)
      : settings = json['settings'] is Map<String, Object?>
            ? Settings.fromJson(json['settings'] as Map<String, Object?>)
            : Settings(),
        needsReactivation = _bool(json['needs_reactivation']);

  final Settings settings;
  final bool needsReactivation;
}

/// A reply with a `status` tag this client does not know — forward
/// compatibility, not an error.
final class UnknownResponse extends Response {
  const UnknownResponse(this.raw);

  final Map<String, Object?> raw;

  String get status => _str(raw['status'], 'unknown');
}
