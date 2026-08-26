part of 'wire.dart';

/// A client → daemon command frame, tagged by `command`.
///
/// Null-vs-omitted follows the fixtures exactly: the session verbs
/// (activate / stop / test_latency) carry their optional fields as
/// EXPLICIT nulls — the Rust serializer's shape, pinned by
/// `contract/fixtures/requests/`; the store verbs OMIT absent optionals —
/// the shape pinned by `contract/fixtures/go/requests/`.
sealed class Request {
  const Request();

  Map<String, Object?> toJson();
}

// ---- session verbs -----------------------------------------------------------

/// `activate` — the only verb that starts cores. Nulls mean the store's
/// active profile / node / routing.
final class ActivateRequest extends Request {
  const ActivateRequest({this.profile, this.node, this.routing, this.tun = false});

  final String? profile;
  final String? node;
  final String? routing;
  final bool tun;

  @override
  Map<String, Object?> toJson() => {
        'command': 'activate',
        'profile': profile,
        'node': node,
        'routing': routing,
        'tun': tun,
      };
}

/// `stop` — the embedded cores stop as one unit. [role] exists only for
/// wire compatibility: the daemon rejects any non-null value and the
/// parameter is queued for removal in the next breaking wire batch
/// (ROADMAP "Wire cleanup"); the UI always sends null.
final class StopRequest extends Request {
  const StopRequest({this.role});

  final String? role;

  @override
  Map<String, Object?> toJson() => {'command': 'stop', 'role': role};
}

/// `status` — session snapshot; replied with `running` or `idle`.
final class StatusRequest extends Request {
  const StatusRequest();

  @override
  Map<String, Object?> toJson() => {'command': 'status'};
}

/// `switch_node` — by node NAME; live for selector members, re-activation
/// otherwise.
final class SwitchNodeRequest extends Request {
  const SwitchNodeRequest(this.node);

  final String node;

  @override
  Map<String, Object?> toJson() => {'command': 'switch_node', 'node': node};
}

/// `test_latency` — node NAMES; null = all current selector members.
final class TestLatencyRequest extends Request {
  const TestLatencyRequest([this.nodes]);

  final List<String>? nodes;

  @override
  Map<String, Object?> toJson() => {'command': 'test_latency', 'nodes': nodes};
}

/// `subscribe` — upgrades the connection to a one-way event stream; the
/// first frame is always a `state` snapshot.
final class SubscribeRequest extends Request {
  const SubscribeRequest();

  @override
  Map<String, Object?> toJson() => {'command': 'subscribe'};
}

/// `diagnose` — staged tcp → proxy failure attribution for one node.
final class DiagnoseRequest extends Request {
  const DiagnoseRequest(this.node);

  final String node;

  @override
  Map<String, Object?> toJson() => {'command': 'diagnose', 'node': node};
}

/// `traffic_apps` — poll the session's cumulative per-app byte counts (the
/// "by app" panel). Empty list when no session runs.
final class TrafficAppsRequest extends Request {
  const TrafficAppsRequest();

  @override
  Map<String, Object?> toJson() => {'command': 'traffic_apps'};
}

/// `doctor` — run the connection doctor (the "Doctor" tab): a battery of
/// read-only network checks, one row each. Needs no running session.
final class DoctorRequest extends Request {
  const DoctorRequest();

  @override
  Map<String, Object?> toJson() => {'command': 'doctor'};
}

/// `doctor_nodes` — probe the camouflage of EVERY node in the active profile
/// (the tab's "probe all nodes" button); one camouflage check per node.
final class DoctorNodesRequest extends Request {
  const DoctorNodesRequest();

  @override
  Map<String, Object?> toJson() => {'command': 'doctor_nodes'};
}

/// `network_report` — the heavier "Network report" characterization (hosting-
/// provider reachability + DNS integrity + transport reachability).
final class NetworkReportRequest extends Request {
  const NetworkReportRequest();

  @override
  Map<String, Object?> toJson() => {'command': 'network_report'};
}

/// `forwarding_check` — the Linux-only host-firewall ↔ forwarded-traffic check
/// (docker0 / br-* bridges vs a default-deny INPUT firewall); its own button.
final class ForwardingCheckRequest extends Request {
  const ForwardingCheckRequest();

  @override
  Map<String, Object?> toJson() => {'command': 'forwarding_check'};
}

// ---- store verbs (the daemon owns the store; clients never touch files) ------

/// `list_profiles`.
final class ListProfilesRequest extends Request {
  const ListProfilesRequest();

  @override
  Map<String, Object?> toJson() => {'command': 'list_profiles'};
}

/// `create_profile`.
final class CreateProfileRequest extends Request {
  const CreateProfileRequest(this.profile);

  final String profile;

  @override
  Map<String, Object?> toJson() => {'command': 'create_profile', 'profile': profile};
}

/// `delete_profile`.
final class DeleteProfileRequest extends Request {
  const DeleteProfileRequest(this.profile);

  final String profile;

  @override
  Map<String, Object?> toJson() => {'command': 'delete_profile', 'profile': profile};
}

/// `set_active_profile`.
final class SetActiveProfileRequest extends Request {
  const SetActiveProfileRequest(this.profile);

  final String profile;

  @override
  Map<String, Object?> toJson() =>
      {'command': 'set_active_profile', 'profile': profile};
}

/// `list_nodes` — null profile = the store's active profile.
final class ListNodesRequest extends Request {
  const ListNodesRequest({this.profile});

  final String? profile;

  @override
  Map<String, Object?> toJson() => {
        'command': 'list_nodes',
        if (profile != null) 'profile': profile,
      };
}

/// `select_node` — by node id; marks the store-active node.
final class SelectNodeRequest extends Request {
  const SelectNodeRequest(this.node);

  final String node;

  @override
  Map<String, Object?> toJson() => {'command': 'select_node', 'node': node};
}

/// `remove_node` — by node id.
final class RemoveNodeRequest extends Request {
  const RemoveNodeRequest(this.node);

  final String node;

  @override
  Map<String, Object?> toJson() => {'command': 'remove_node', 'node': node};
}

/// `add_node` — the daemon parses the share link; the reply carries the
/// new node's id.
final class AddNodeRequest extends Request {
  const AddNodeRequest(this.url);

  final String url;

  @override
  Map<String, Object?> toJson() => {'command': 'add_node', 'url': url};
}

/// `set_node_prefs` — pins a node to a core; an ABSENT [coreOverride]
/// clears the pin.
final class SetNodePrefsRequest extends Request {
  const SetNodePrefsRequest(this.node, {this.coreOverride});

  final String node;

  /// A [CoreType] value, or null to clear.
  final String? coreOverride;

  @override
  Map<String, Object?> toJson() => {
        'command': 'set_node_prefs',
        'node': node,
        if (coreOverride != null) 'core_override': coreOverride,
      };
}

/// `list_subscriptions`.
final class ListSubscriptionsRequest extends Request {
  const ListSubscriptionsRequest();

  @override
  Map<String, Object?> toJson() => {'command': 'list_subscriptions'};
}

/// `add_subscription` — the reply is `refreshed` (the initial fetch
/// outcome). Null name = derived from the URL host; null format = "auto"
/// (detect on every fetch).
final class AddSubscriptionRequest extends Request {
  const AddSubscriptionRequest(this.url,
      {this.name, this.allowInvalidCerts, this.format});

  final String url;
  final String? name;
  final bool? allowInvalidCerts;
  final String? format;

  @override
  Map<String, Object?> toJson() => {
        'command': 'add_subscription',
        'url': url,
        if (name != null) 'name': name,
        if (allowInvalidCerts != null) 'allow_invalid_certs': allowInvalidCerts,
        if (format != null) 'format': format,
      };
}

/// `refresh_subscriptions` — null [sub] = all enabled; an explicit sub
/// (id or name) beats the enabled flag.
final class RefreshSubscriptionsRequest extends Request {
  const RefreshSubscriptionsRequest({this.sub});

  final String? sub;

  @override
  Map<String, Object?> toJson() => {
        'command': 'refresh_subscriptions',
        if (sub != null) 'sub': sub,
      };
}

/// `remove_subscription` — by id or name; removes the subscription's nodes
/// with it.
final class RemoveSubscriptionRequest extends Request {
  const RemoveSubscriptionRequest(this.sub);

  final String sub;

  @override
  Map<String, Object?> toJson() => {'command': 'remove_subscription', 'sub': sub};
}

/// `update_subscription` — edit a stored subscription's metadata. [sub] names
/// it (id or name); every other field is optional and a null one is left
/// unchanged by the daemon.
final class UpdateSubscriptionRequest extends Request {
  const UpdateSubscriptionRequest(
    this.sub, {
    this.name,
    this.url,
    this.enabled,
    this.allowInvalidCerts,
    this.updateIntervalSec,
    this.format,
  });

  final String sub;
  final String? name;
  final String? url;
  final bool? enabled;
  final bool? allowInvalidCerts;
  final int? updateIntervalSec;
  final String? format;

  @override
  Map<String, Object?> toJson() => {
        'command': 'update_subscription',
        'sub': sub,
        if (name != null) 'name': name,
        if (url != null) 'url': url,
        if (enabled != null) 'enabled': enabled,
        if (allowInvalidCerts != null) 'allow_invalid_certs': allowInvalidCerts,
        if (updateIntervalSec != null)
          'update_interval_sec': updateIntervalSec,
        if (format != null) 'format': format,
      };
}

/// `list_routing`.
final class ListRoutingRequest extends Request {
  const ListRoutingRequest();

  @override
  Map<String, Object?> toJson() => {'command': 'list_routing'};
}

/// `select_routing` — by id or name.
final class SelectRoutingRequest extends Request {
  const SelectRoutingRequest(this.routing);

  final String routing;

  @override
  Map<String, Object?> toJson() => {'command': 'select_routing', 'routing': routing};
}

/// `get_routing` — ONE config as its full JSON document (by id or name),
/// the read half of `upsert_routing`.
final class GetRoutingRequest extends Request {
  const GetRoutingRequest(this.routing);

  final String routing;

  @override
  Map<String, Object?> toJson() => {'command': 'get_routing', 'routing': routing};
}

/// `routing_schema` — the condition-field schema for routing forms,
/// computed from the daemon's pinned sing-box and ITS platform.
final class RoutingSchemaRequest extends Request {
  const RoutingSchemaRequest();

  @override
  Map<String, Object?> toJson() => const {'command': 'routing_schema'};
}

/// `remove_routing` — by id or name.
final class RemoveRoutingRequest extends Request {
  const RemoveRoutingRequest(this.routing);

  final String routing;

  @override
  Map<String, Object?> toJson() => {'command': 'remove_routing', 'routing': routing};
}

/// `get_settings` — the effective daemon-global settings document
/// (defaults when no file exists).
final class GetSettingsRequest extends Request {
  const GetSettingsRequest();

  @override
  Map<String, Object?> toJson() => {'command': 'get_settings'};
}

/// `set_settings` — the FULL document (edit what `get_settings` returned
/// and send it back; no patch semantics).
final class SetSettingsRequest extends Request {
  const SetSettingsRequest(this.settings);

  final Settings settings;

  @override
  Map<String, Object?> toJson() =>
      {'command': 'set_settings', 'settings': settings.toJson()};
}

/// `upsert_routing` — the full routing config JSON (the store shape),
/// passed through verbatim; a missing id gets a fresh one.
final class UpsertRoutingRequest extends Request {
  const UpsertRoutingRequest(this.routingConfig);

  final Map<String, Object?> routingConfig;

  @override
  Map<String, Object?> toJson() =>
      {'command': 'upsert_routing', 'routing_config': routingConfig};
}

/// `get_group` — ONE user group's stored spec (by id or name), the read half
/// of `upsert_group`: it carries the true membership mode and probe, which the
/// resolved `list_nodes` group row does not.
final class GetGroupRequest extends Request {
  const GetGroupRequest(this.group);

  final String group;

  @override
  Map<String, Object?> toJson() => {'command': 'get_group', 'node': group};
}

/// `upsert_group` — a user group's spec JSON ({id?, name, members?/all_of_sub?,
/// probe?}); a missing id creates a new user group, an existing user-group id
/// replaces it in place.
final class UpsertGroupRequest extends Request {
  const UpsertGroupRequest(this.group);

  final Map<String, Object?> group;

  @override
  Map<String, Object?> toJson() => {'command': 'upsert_group', 'group': group};
}
