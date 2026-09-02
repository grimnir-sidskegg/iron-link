/// The typed IPC client every screen talks through. One connection per
/// request/response (mirroring the daemon's model); `subscribe()` opens a
/// dedicated connection that becomes a one-way event stream.
library;

import 'dart:async';
import 'dart:io';

import '../wire/wire.dart';
import 'codec.dart';
import 'endpoint.dart';
import 'pipe_windows.dart';
import 'transport.dart';

/// Error taxonomy: [DaemonUnreachable]
/// (cannot connect — carries the endpoint for the "is the daemon
/// running?" hint), [DaemonError] (the daemon replied `error`),
/// [ProtocolError] (the reply did not match the verb, or the stream broke
/// mid-frame).
sealed class ClientException implements Exception {
  const ClientException(this.message);

  final String message;

  @override
  String toString() => message;
}

final class DaemonUnreachable extends ClientException {
  const DaemonUnreachable(this.endpoint, Object cause)
      : super('cannot reach the daemon at $endpoint: $cause');

  final String endpoint;
}

final class DaemonError extends ClientException {
  const DaemonError(super.message);
}

final class ProtocolError extends ClientException {
  const ProtocolError(super.message);
}

/// How many single-node latency probes [DaemonClient.testLatencyEach] keeps in
/// flight at once. Mirrors the daemon's `latencyProbeConcurrency` (8): that cap
/// is applied PER `test_latency` request, so a per-node fan-out must re-impose
/// it client-side or a large profile would spin unbounded ephemeral cores.
const int latencyProbeFanout = 8;

/// The typed daemon client. Stateless between calls — safe to share.
class DaemonClient {
  DaemonClient({String? endpoint})
      : endpoint = endpoint ?? Endpoint().socketPath();

  /// The unix-socket path (or pipe name) this client dials.
  final String endpoint;

  static const _connectTimeout = Duration(seconds: 5);

  Future<IpcConnection> _connect() async {
    try {
      if (Platform.isWindows) {
        // dart:io has no named-pipe client; pipe_windows.dart fills the
        // gap over FFI. A connect failure (PipeError) maps to
        // DaemonUnreachable exactly like a SocketException would.
        return await WindowsPipeConnection.connect(endpoint,
            timeout: _connectTimeout);
      }
      return SocketConnection(await Socket.connect(
        InternetAddress(endpoint, type: InternetAddressType.unix),
        0,
        timeout: _connectTimeout,
      ));
    } on Object catch (e) {
      throw DaemonUnreachable(endpoint, e);
    }
  }

  /// Sends one request and returns the single reply. No read timeout by
  /// design: latency probes and subscription fetches legitimately take
  /// tens of seconds; callers wanting one can race the future.
  Future<Response> request(Request req) async {
    final conn = await _connect();
    try {
      conn.add(encodeFrame(req.toJson()));
      await conn.flush();
      final frame = await decodeFrames(conn.incoming)
          .firstWhere((_) => true, orElse: () => throw const ProtocolError(
              'connection closed before a reply arrived'));
      return Response.fromJson(frame);
    } on CodecException catch (e) {
      throw ProtocolError(e.message);
    } finally {
      conn.destroy();
    }
  }

  /// Opens an event-stream connection. The first event is always a
  /// `state` snapshot; the stream ends when the daemon goes away (the
  /// caller owns reconnection policy).
  Stream<Event> subscribe() async* {
    final conn = await _connect();
    try {
      conn.add(encodeFrame(const SubscribeRequest().toJson()));
      await conn.flush();
      await for (final frame in decodeFrames(conn.incoming)) {
        yield Event.fromJson(frame);
      }
    } on CodecException catch (e) {
      throw ProtocolError(e.message);
    } finally {
      conn.destroy();
    }
  }

  // ---- typed verbs: narrow the reply, surface `error` as DaemonError ----

  Future<T> _expect<T extends Response>(Request req) async {
    final reply = await request(req);
    if (reply is T) return reply;
    if (reply is ErrorResponse) throw DaemonError(reply.message);
    throw ProtocolError('unexpected reply ${reply.runtimeType} to '
        '${req.toJson()['command']}');
  }

  /// `status` — [RunningResponse] or [IdleResponse].
  Future<Response> status() async {
    final reply = await request(const StatusRequest());
    return switch (reply) {
      RunningResponse() || IdleResponse() => reply,
      ErrorResponse(:final message) => throw DaemonError(message),
      _ => throw ProtocolError('unexpected reply ${reply.runtimeType} to status'),
    };
  }

  Future<ActivatedResponse> activate(
          {String? profile, String? node, String? routing, bool tun = false}) =>
      _expect<ActivatedResponse>(ActivateRequest(
          profile: profile, node: node, routing: routing, tun: tun));

  Future<void> stop() => _expect<StoppedResponse>(const StopRequest());

  Future<String> switchNode(String node) async =>
      (await _expect<SwitchedResponse>(SwitchNodeRequest(node))).node;

  Future<List<LatencyResult>> testLatency([List<String>? nodes]) async =>
      (await _expect<LatenciesResponse>(TestLatencyRequest(nodes))).latencies;

  /// Fan-out latency probe: one INDEPENDENT single-node `test_latency`
  /// request per node, at most [concurrency] in flight, yielding each
  /// node's result the moment its own probe returns. A slow/hung node only
  /// delays its OWN result — bounded by the daemon's per-request probe
  /// budget — never the others, unlike [testLatency] whose single reply is
  /// gated on the slowest node in the batch.
  ///
  /// [concurrency] mirrors the daemon's own `latencyProbeConcurrency`: each
  /// single-node request spins one ephemeral probe core, and the daemon's
  /// cap is per-request, so N separate requests would fan out unbounded
  /// cores unless the client re-imposes the ceiling here.
  ///
  /// A per-node NON-connectivity failure (e.g. a node vanished between
  /// list and probe) surfaces as that node's null-latency result — the same
  /// "a failed probe is a null latency, not an error" contract [testLatency]
  /// already honors — so one bad node never voids the sweep. A
  /// [DaemonUnreachable] is different in kind (the daemon is down, every
  /// probe is meaningless): it propagates and ends the stream.
  Stream<LatencyResult> testLatencyEach(Iterable<String> nodes,
      {int concurrency = latencyProbeFanout}) {
    final pending = List<String>.of(nodes);
    final controller = StreamController<LatencyResult>();
    var next = 0;
    var active = 0;

    void drain() {
      while (active < concurrency && next < pending.length) {
        final name = pending[next++];
        active++;
        testLatency([name]).then<void>((results) {
          if (controller.isClosed) return;
          controller.add(results.isNotEmpty
              ? results.first
              : LatencyResult(node: name));
        }, onError: (Object e, StackTrace st) {
          if (controller.isClosed) return;
          if (e is DaemonUnreachable) {
            controller.addError(e, st); // daemon down — the whole sweep is void
          } else {
            controller.add(LatencyResult(node: name)); // this node only: "—"
          }
        }).whenComplete(() {
          active--;
          if (next < pending.length) {
            drain();
          } else if (active == 0 && !controller.isClosed) {
            controller.close();
          }
        });
      }
    }

    // Lazy: nothing dials until someone listens, and an empty node set is an
    // immediately-closed (empty) stream.
    controller.onListen = () => pending.isEmpty ? controller.close() : drain();
    return controller.stream;
  }

  Future<DiagnosisInfo> diagnose(String node) async {
    final reply = await _expect<DiagnosisResponse>(DiagnoseRequest(node));
    final diagnosis = reply.diagnosis;
    if (diagnosis == null) {
      throw const ProtocolError('diagnosis reply without a verdict');
    }
    return diagnosis;
  }

  /// `traffic_apps` — the session's cumulative per-app byte counts (empty
  /// when no session runs); the caller derives rates from the deltas.
  Future<List<AppTraffic>> trafficApps() async =>
      (await _expect<AppTrafficResponse>(const TrafficAppsRequest())).apps;

  /// `doctor` — the ordered connection-doctor checks (one row each).
  Future<List<DoctorCheck>> doctor() async =>
      (await _expect<DoctorReportResponse>(const DoctorRequest())).checks;

  /// `doctor_nodes` — a camouflage check for every node in the active profile.
  Future<List<DoctorCheck>> doctorNodes() async =>
      (await _expect<DoctorReportResponse>(const DoctorNodesRequest())).checks;

  /// `network_report` — provider reachability + DNS integrity + transports.
  Future<List<DoctorCheck>> networkReport() async =>
      (await _expect<DoctorReportResponse>(const NetworkReportRequest())).checks;

  /// `forwarding_check` — the Linux-only docker/LAN forwarding ↔ firewall row
  /// (empty list off Linux).
  Future<List<DoctorCheck>> forwardingCheck() async =>
      (await _expect<DoctorReportResponse>(const ForwardingCheckRequest())).checks;

  Future<ProfilesResponse> listProfiles() =>
      _expect<ProfilesResponse>(const ListProfilesRequest());

  Future<void> createProfile(String name) =>
      _expect<OkResponse>(CreateProfileRequest(name));

  Future<void> deleteProfile(String name) =>
      _expect<OkResponse>(DeleteProfileRequest(name));

  Future<void> setActiveProfile(String name) =>
      _expect<OkResponse>(SetActiveProfileRequest(name));

  Future<List<NodeInfo>> listNodes({String? profile}) async =>
      (await _expect<NodesResponse>(ListNodesRequest(profile: profile))).nodes;

  Future<void> selectNode(String id) => _expect<OkResponse>(SelectNodeRequest(id));

  Future<void> removeNode(String id) => _expect<OkResponse>(RemoveNodeRequest(id));

  /// Returns the new node's id.
  Future<String> addNode(String url) async =>
      (await _expect<OkResponse>(AddNodeRequest(url))).node ?? '';

  Future<void> setNodePrefs(String id, {String? coreOverride}) =>
      _expect<OkResponse>(SetNodePrefsRequest(id, coreOverride: coreOverride));

  Future<List<SubscriptionInfo>> listSubscriptions() async =>
      (await _expect<SubscriptionsResponse>(const ListSubscriptionsRequest()))
          .subscriptions;

  Future<List<RefreshInfo>> addSubscription(String url,
          {String? name, bool? allowInvalidCerts, String? format}) async =>
      (await _expect<RefreshedResponse>(AddSubscriptionRequest(url,
              name: name, allowInvalidCerts: allowInvalidCerts, format: format)))
          .refreshed;

  Future<List<RefreshInfo>> refreshSubscriptions({String? sub}) async =>
      (await _expect<RefreshedResponse>(RefreshSubscriptionsRequest(sub: sub)))
          .refreshed;

  Future<void> removeSubscription(String sub) =>
      _expect<OkResponse>(RemoveSubscriptionRequest(sub));

  /// Edits a stored subscription's metadata; null fields are left unchanged.
  Future<void> updateSubscription(String sub,
          {String? name,
          String? url,
          bool? enabled,
          bool? allowInvalidCerts,
          int? updateIntervalSec,
          String? format}) =>
      _expect<OkResponse>(UpdateSubscriptionRequest(sub,
          name: name,
          url: url,
          enabled: enabled,
          allowInvalidCerts: allowInvalidCerts,
          updateIntervalSec: updateIntervalSec,
          format: format));

  Future<List<RoutingInfo>> listRouting() async =>
      (await _expect<RoutingResponse>(const ListRoutingRequest())).routing;

  Future<void> selectRouting(String routing) =>
      _expect<OkResponse>(SelectRoutingRequest(routing));

  Future<void> removeRouting(String routing) =>
      _expect<OkResponse>(RemoveRoutingRequest(routing));

  Future<void> upsertRouting(Map<String, Object?> config) =>
      _expect<OkResponse>(UpsertRoutingRequest(config));

  /// Create or edit a user group (an "Auto" node). [group] is the spec —
  /// {id?, name, members?/all_of_sub?, probe?}; an empty/absent id creates,
  /// an existing user-group id replaces in place. Removal reuses [removeNode].
  Future<void> upsertGroup(Map<String, Object?> group) =>
      _expect<OkResponse>(UpsertGroupRequest(group));

  /// Returns one user group's stored spec ({name, members?/all_of_sub?, probe})
  /// by id or name — the read half of [upsertGroup] that pre-fills the editor
  /// with the true membership mode and probe (the resolved [listNodes] group
  /// row carries neither).
  Future<Map<String, Object?>> getGroup(String group) async =>
      (await _expect<GroupConfigResponse>(GetGroupRequest(group))).groupConfig;

  /// Returns one dialable node's full stored config ({name, protocol,
  /// profile:{…}}) by id or name — the read-only inspector's source, exposing
  /// the endpoint and security/transport fields the [listNodes] row omits.
  Future<Map<String, Object?>> getNode(String node) async =>
      (await _expect<NodeConfigResponse>(GetNodeRequest(node))).nodeConfig;

  /// Returns one config's full JSON document — the read half of
  /// [upsertRouting].
  Future<Map<String, Object?>> getRouting(String routing) async =>
      (await _expect<RoutingConfigResponse>(GetRoutingRequest(routing)))
          .routingConfig;

  Future<RoutingSchema> routingSchema() async =>
      (await _expect<RoutingSchemaResponse>(const RoutingSchemaRequest()))
          .routingSchema;

  Future<Settings> getSettings() async =>
      (await _expect<SettingsResponse>(const GetSettingsRequest())).settings;

  /// Returns the applied document + whether a running session needs
  /// re-activation to pick the change up.
  Future<SettingsResponse> setSettings(Settings settings) =>
      _expect<SettingsResponse>(SetSettingsRequest(settings));

  /// `check_update` — the daemon's cached update verdict; [force] runs a
  /// fresh synchronous check first. An old daemon answers `error`
  /// ("unimplemented verb"), surfaced as [DaemonError] — callers treat that
  /// as "no update support".
  Future<UpdateStatus> checkUpdate({bool force = false}) async =>
      (await _expect<UpdateStatusResponse>(CheckUpdateRequest(force: force)))
          .updateStatus;

  /// `download_update` — start the async installer download (Windows only);
  /// returns immediately with `download_state: downloading`. Progress
  /// arrives as `update_progress` events.
  Future<UpdateStatus> downloadUpdate() async =>
      (await _expect<UpdateStatusResponse>(const DownloadUpdateRequest()))
          .updateStatus;

  /// `apply_update` — the daemon re-verifies the downloaded installer; the
  /// reply's `setupPath` is the verified file THIS client launches.
  Future<UpdateStatus> applyUpdate() async =>
      (await _expect<UpdateStatusResponse>(const ApplyUpdateRequest()))
          .updateStatus;
}
