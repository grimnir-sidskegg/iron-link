/// Live daemon session state for the UI — the Dart counterpart of the former
/// egui GUI's worker: a dedicated subscribe connection with 1 s-backoff
/// reconnect, PLUS a slow 5 s `status` backstop poll. The poll is not
/// redundancy: a `urltest` auto-switch does not emit a State event, so
/// `active_node_live` is only observable by re-reading status (ROADMAP
/// wire-cleanup item).
library;

import 'dart:async';
import 'dart:collection';

import 'package:flutter/foundation.dart';

import '../ipc/client.dart';
import '../platform/update_launcher.dart';
import '../wire/wire.dart';

/// One line in the session feed (logs + core errors + subscription
/// updates, merged).
class FeedLine {
  FeedLine(this.at, this.level, this.message);

  final DateTime at;
  final String level;
  final String message;
}

/// Matches ANSI CSI escape sequences (`ESC [ … final-byte`) — the SGR colour
/// codes sing-box paints onto a log line.
final _ansiEscape = RegExp(r'\x1B\[[0-?]*[ -/]*[@-~]');

/// Normalizes a feed message for display. sing-box's platform formatter bakes
/// two noisy prefixes onto every line — the level and the daemon uptime in
/// seconds: `INFO[15905] [<connId> <age>] msg`. The level is rendered in its
/// own column and the uptime is meaningless here, so drop both; the
/// connection-id bracket (`[<id> <age>]`, a space inside) is kept. The level
/// word is dropped ONLY when it matches THIS line's level, so real prose like
/// "Error parsing …" on an info line survives.
///
/// sing-box ≥ v1.13 colourises the platform-writer line unconditionally (the
/// upstream wiring that honoured the daemon's DisableColors() was dropped), so
/// the message now arrives wrapped in ANSI SGR codes — `\x1B[37mINFO\x1B[0m…`.
/// We strip them FIRST: the daemon terminal keeps its own colours, and the feed
/// gets recoloured from our palette ([logs_page] tints the level column and the
/// connection id). Left in, the codes would defeat the prefix-strip regexes
/// below (which anchor on a letter / `[`) and render as raw escapes.
@visibleForTesting
String normalizeFeedMessage(String level, String message) {
  var m = message.replaceAll(_ansiEscape, '');
  final lvl = RegExp(r'^([A-Za-z]+)').firstMatch(m);
  if (lvl != null) {
    final token = lvl.group(1)!.toLowerCase();
    final want = level.toLowerCase();
    if (token == want || want.startsWith(token)) {
      m = m.substring(lvl.end);
    }
  }
  // The uptime bracket is a single number; the connection-id bracket has a
  // space inside, so this won't touch it.
  m = m.replaceFirst(RegExp(r'^\[\d+\]\s*'), '');
  return m.trimLeft();
}

/// One second of traffic (deltas, as the daemon emits them).
class TrafficPoint {
  TrafficPoint(this.up, this.down);

  final int up;
  final int down;
}

/// Observable daemon state. Everything the UI shows is read back from the
/// daemon (events + status polls) — never held as a parallel model.
class DaemonSession extends ChangeNotifier {
  DaemonSession(this.client, {this.launchInstaller = launchUpdateInstaller});

  final DaemonClient client;

  /// The Windows one-click apply's installer launch; injected in tests.
  final Future<void> Function(String setupPath) launchInstaller;

  static const _reconnectDelay = Duration(seconds: 1);
  static const _pollInterval = Duration(seconds: 5);
  static const _trafficWindow = 180; // seconds of chart history
  static const _feedCap = 500;

  /// The safety net for [updating]: cleared even when the new daemon never
  /// shows a different version (e.g. the user cancelled the UAC consent,
  /// which the client cannot observe).
  static const _updatingTimeout = Duration(minutes: 3);

  bool _disposed = false;
  StreamSubscription<Event>? _events;
  Timer? _retry;
  Timer? _poll;
  Timer? _updatingTimer;

  /// `daemonVersion` at the moment the installer was launched — [updating]
  /// ends when a status poll reports a different one.
  String? _updatingFrom;

  /// Monotonic id for in-flight `status` polls. The client opens one
  /// connection per request with no read timeout, so a slow poll issued
  /// BEFORE a live switch can land AFTER the switch's [refreshStatus] and
  /// overwrite `active_node_live` with the pre-switch node — the play icon
  /// snaps back to the old node until the next poll. Only the LATEST-issued
  /// poll may apply its result; older responses that arrive late are dropped.
  int _statusGen = 0;

  /// Bumped on every `update_progress` event. An update verb's reply is a
  /// snapshot taken on the request connection; the download goroutine's
  /// terminal `failed`/`downloaded` event travels on the subscribe
  /// connection and can land BEFORE that reply (a fast mirror refusal). The
  /// reply must then not regress the download state — see [_storeUpdateReply].
  int _updateGen = 0;

  /// Whether the subscribe connection is up.
  bool connected = false;

  /// The latest session snapshot (state event or status poll).
  List<CoreEntry> entries = const [];
  PersistedEntry? active;

  /// The live selector readback: pushed on every state event (an urltest
  /// auto-switch included) and refreshed by the backstop status poll.
  String? activeNodeLive;

  /// The daemon's build version, read back from the status poll (null until
  /// the first poll answers, or when the daemon predates the field).
  String? daemonVersion;

  /// The daemon's update verdict, refreshed from `check_update` once the
  /// daemon is confirmed up and on every `update_available` nudge; the
  /// `update_progress` events patch its download counters live. Null until
  /// the first reply — or forever against an old daemon without the verb
  /// (its "unimplemented verb" error is swallowed as "no update support").
  UpdateStatus? updateStatus;

  /// The update the UI should offer: available and from a fresh manifest
  /// (a soft-expired one is informational only). Null otherwise.
  UpdateStatus? get offeredUpdate {
    final st = updateStatus;
    return st != null && st.available && !st.stale ? st : null;
  }

  /// Whether [offeredUpdate] carries a downloaded (or re-verified) installer
  /// — the state in which Install is offered (on Windows).
  bool get installStaged {
    final st = offeredUpdate;
    return st != null &&
        st.artifact?.kind == 'installer' &&
        (st.downloadState == 'downloaded' || st.downloadState == 'verified');
  }

  /// The `latest_version` whose banner the user dismissed — in memory only,
  /// so the banner returns on the next launch.
  String? dismissedUpdateVersion;

  /// An Install asked for from outside the widget tree (the tray entry).
  /// The shell consumes it with [takeInstallRequest] and runs the same
  /// consent dialog + [applyUpdate] flow as the banner's button, so the
  /// tray path never skips the warning about the tunnel drop.
  bool installRequested = false;

  /// The last explicit update action's failure ("Update check failed: …",
  /// "Download failed: …", "Install failed: …"); null once one succeeds.
  String? lastUpdateCheckError;

  /// An explicit update action is in flight — the buttons' double-fire guard.
  bool updateBusy = false;

  /// The installer has been launched: the daemon is about to go away and
  /// come back newer. The UI swaps the daemon-down banner for an "Updating…"
  /// state; cleared when a status poll reports a different `daemon_version`,
  /// or by [_updatingTimeout].
  bool updating = false;

  bool get isRunning => entries.any((e) => e.isRunning);

  /// Rolling traffic history (last [_trafficWindow] seconds) + session totals.
  final Queue<TrafficPoint> traffic = Queue();
  int totalUp = 0;
  int totalDown = 0;
  TrafficPoint get rate => traffic.isEmpty ? TrafficPoint(0, 0) : traffic.last;

  /// Merged event feed for the Logs page.
  final Queue<FeedLine> feed = Queue();

  /// Bumped on `subscription_updated` so list pages know to reload.
  int storeRev = 0;

  /// Client-side hint that the store changed behind the daemon's back —
  /// e.g. the active profile was switched from the rail menu, which has no
  /// daemon event but invalidates `list_nodes`.
  void noteStoreChanged() {
    storeRev++;
    notifyListeners();
  }

  /// Starts the subscribe loop and the status backstop poll.
  void start() {
    _connectEvents();
    _poll = Timer.periodic(_pollInterval, (_) => _pollStatus());
    _pollStatus();
  }

  void _connectEvents() {
    if (_disposed) return;
    _events = client.subscribe().listen(
      (event) {
        if (!connected) {
          connected = true;
          // The daemon is confirmed up (this also fires after a reconnect —
          // e.g. an update restart): read its update verdict, non-fatally.
          _refreshUpdateStatus();
        }
        _onEvent(event);
        notifyListeners();
      },
      onError: (Object _) => _scheduleReconnect(),
      onDone: _scheduleReconnect,
      cancelOnError: true,
    );
  }

  void _scheduleReconnect() {
    if (_disposed) return;
    _events?.cancel();
    _events = null;
    if (connected) {
      connected = false;
      notifyListeners();
    }
    _retry?.cancel();
    _retry = Timer(_reconnectDelay, _connectEvents);
  }

  void _onEvent(Event event) {
    switch (event) {
      case StateEvent():
        entries = event.entries;
        active = event.active;
        // A state event is at least as fresh as any poll already in flight, so
        // invalidate that poll's late reply (bump the generation exactly as a
        // switch's refreshStatus does) — otherwise a pre-auto-switch poll
        // landing after this event would clobber the live node back to the old
        // one until the next backstop poll.
        _statusGen++;
        // The daemon pushes the live node on a running transition (an urltest
        // auto-switch included), so the badge follows without the 5s poll. Only
        // overwrite when the event actually carries it: an idle event clears it,
        // and a daemon predating the push omits it on a running event — which
        // must not wipe the value the status poll provided.
        if (!event.isRunning) {
          activeNodeLive = null;
        } else if (event.activeNodeLive != null) {
          activeNodeLive = event.activeNodeLive;
        }
      case TrafficEvent():
        traffic.addLast(TrafficPoint(event.up, event.down));
        while (traffic.length > _trafficWindow) {
          traffic.removeFirst();
        }
        totalUp += event.up;
        totalDown += event.down;
      case LogEvent():
        _pushFeed(event.level, event.message);
      case CoreErrorEvent():
        _pushFeed('error',
            '[${event.role ?? 'core'}/${event.stage}] ${event.message}');
      case SubscriptionUpdatedEvent():
        storeRev++;
        _pushFeed('info',
            'subscription ${event.subId}: +${event.added} -${event.removed} '
            '(${event.total} total)');
      case UpdateAvailableEvent():
        // The event is only a nudge (the hub has no replay) — the cached
        // check_update reply is the truth, so re-read it.
        _refreshUpdateStatus();
      case UpdateProgressEvent():
        _updateGen++;
        final st = updateStatus;
        if (st != null) {
          updateStatus = st.withProgress(
              state: event.state,
              received: event.received,
              total: event.total);
        }
      case UnknownEvent():
        break; // forward compatibility: skip, don't fail
    }
  }

  /// Reads the daemon's update verdict, non-fatally: an old daemon answers
  /// the verb with an error ("no update support") and a transient failure
  /// changes nothing — [updateStatus] just keeps its last value (null on
  /// first run).
  Future<void> _refreshUpdateStatus() async {
    final gen = _updateGen;
    try {
      final st = await client.checkUpdate();
      if (_disposed) return;
      _storeUpdateReply(st, gen);
      notifyListeners();
    } on ClientException {
      // No update support (or the daemon went away mid-request).
    }
  }

  /// Stores an update verb's reply issued at [gen]. When `update_progress`
  /// events arrived while the request was in flight they are fresher than
  /// the reply's download snapshot (the event stream always ends on the
  /// terminal state; the reply is a moment in between), so the reply's
  /// progress triplet yields to what the events already applied.
  void _storeUpdateReply(UpdateStatus st, int gen) {
    final cur = updateStatus;
    if (gen == _updateGen || cur == null) {
      updateStatus = st;
      return;
    }
    updateStatus = st.withProgress(
        state: cur.downloadState,
        received: cur.downloadReceived,
        total: cur.downloadTotal);
  }

  // ---- explicit update actions (Settings "Check now", the banner buttons) --

  /// "Check now": a forced synchronous check; the reply replaces
  /// [updateStatus].
  Future<void> checkForUpdates() =>
      _updateAction('Update check', () => client.checkUpdate(force: true));

  /// "Download": the daemon starts the installer download; progress then
  /// arrives as `update_progress` events.
  Future<void> startUpdateDownload() =>
      _updateAction('Download', client.downloadUpdate);

  /// "Install": the daemon re-verifies the download and hands back the
  /// installer path, which THIS client launches (Windows). The installer will
  /// kill the client itself, so nothing here quits pre-emptively; [updating]
  /// is raised just before the launch and dropped again if the launch fails.
  /// One installer at a time: while [updating] (the launched setup may still
  /// sit at its UAC prompt) a second Install is ignored rather than spawning
  /// a second setup.
  Future<void> applyUpdate() async {
    if (updating) return;
    await _updateAction('Install', () async {
      final st = await client.applyUpdate();
      updateStatus = st;
      final path = st.setupPath;
      if (st.downloadState != 'verified' || path == null || path.isEmpty) {
        throw const UpdateLaunchError(
            'the daemon did not hand back a verified installer');
      }
      _beginUpdating(st);
      try {
        await launchInstaller(path);
      } on Object {
        _endUpdating(); // no installer is coming — drop the state, then report
        rethrow;
      }
      return st;
    });
  }

  /// Asks the shell to run the Install flow (consent dialog + [applyUpdate])
  /// on behalf of a caller with no widget context — the tray entry.
  void requestInstall() {
    installRequested = true;
    notifyListeners();
  }

  /// Claims a pending [requestInstall]; true exactly once per request.
  bool takeInstallRequest() {
    if (!installRequested) return false;
    installRequested = false;
    return true;
  }

  /// Runs one explicit update action under the busy guard: the reply
  /// replaces [updateStatus]; a failure lands in [lastUpdateCheckError] as
  /// `<what> failed: <message>`.
  Future<void> _updateAction(
      String what, Future<UpdateStatus> Function() action) async {
    if (updateBusy) return;
    updateBusy = true;
    lastUpdateCheckError = null;
    notifyListeners();
    final gen = _updateGen;
    try {
      final st = await action();
      if (_disposed) return;
      _storeUpdateReply(st, gen);
    } on ClientException catch (e) {
      lastUpdateCheckError = '$what failed: ${e.message}';
    } on UpdateLaunchError catch (e) {
      lastUpdateCheckError = '$what failed: ${e.message}';
    } finally {
      // Whatever happened, the buttons must come back.
      if (!_disposed) {
        updateBusy = false;
        notifyListeners();
      }
    }
  }

  /// Hides the banner for the version currently offered (until relaunch).
  void dismissUpdate() {
    dismissedUpdateVersion = updateStatus?.latestVersion;
    notifyListeners();
  }

  void _beginUpdating(UpdateStatus applied) {
    updating = true;
    // The status poll may not have answered yet (its first reply is dropped
    // when a state event lands first); the apply reply's current_version is
    // the same build's string, so it is a valid baseline too.
    _updatingFrom = daemonVersion ?? applied.currentVersion;
    _updatingTimer?.cancel();
    _updatingTimer = Timer(_updatingTimeout, () {
      if (_disposed || !updating) return;
      _endUpdating();
      notifyListeners();
    });
    notifyListeners();
  }

  void _endUpdating() {
    updating = false;
    _updatingFrom = null;
    _updatingTimer?.cancel();
    _updatingTimer = null;
  }

  void _pushFeed(String level, String message) {
    feed.addLast(
        FeedLine(DateTime.now(), level, normalizeFeedMessage(level, message)));
    while (feed.length > _feedCap) {
      feed.removeFirst();
    }
  }

  Future<void> _pollStatus() async {
    if (_disposed) return;
    final gen = ++_statusGen;
    try {
      final reply = await client.status();
      // Drop a stale response: a newer poll (a periodic tick or an explicit
      // refreshStatus) was issued while this one was in flight, so its result
      // reflects daemon state at least as fresh as ours — never let this older
      // response clobber it.
      if (_disposed || gen != _statusGen) return;
      switch (reply) {
        case RunningResponse():
          entries = reply.entries;
          active = reply.active ?? active;
          activeNodeLive = reply.activeNodeLive;
          daemonVersion = reply.daemonVersion;
        case IdleResponse():
          entries = const [];
          active = null;
          activeNodeLive = null;
          daemonVersion = reply.daemonVersion;
        default:
          return;
      }
      // The restarted daemon answers with its new build: the update landed.
      // Without a known baseline (or a version in the reply) only the safety
      // timeout ends it — any answered poll must not drop the flag while the
      // installer is still at its UAC prompt.
      final from = _updatingFrom;
      final now = daemonVersion;
      if (updating && from != null && from.isNotEmpty && now != null &&
          now != from) {
        _endUpdating();
      }
      notifyListeners();
    } on ClientException {
      // The subscribe loop owns connectivity reporting; a failed poll on
      // its own is not news.
    }
  }

  /// Forces a status re-read (after activate/stop/switch).
  Future<void> refreshStatus() => _pollStatus();

  @override
  void dispose() {
    _disposed = true;
    _events?.cancel();
    _retry?.cancel();
    _poll?.cancel();
    _updatingTimer?.cancel();
    super.dispose();
  }
}
