import 'package:flutter/material.dart';

import '../app/formats.dart';
import '../app/session.dart';
import '../ipc/client.dart';
import '../wire/wire.dart';
import 'app.dart';
import 'dialogs.dart';
import 'status_header.dart';
import 'theme/app_colors.dart';
import 'theme/app_typography.dart';
import 'widgets/iron_widgets.dart';

/// The home page: the session hero (StatusHeader — power orb, live rate, the
/// pipeline) above one flat tree of standalone nodes and subscriptions with
/// their nodes. Tapping a node's circle is ONE atomic click: while a session
/// runs it live-switches traffic there AND makes it the profile default; when
/// idle it just sets the profile default — no confirm dialog either way. The
/// node menu keeps the full expert action set (switch / select / probe /
/// diagnose / pin / remove) where switch and select stay SEPARATE; subscription
/// rows keep refresh / remove.
class HomePage extends StatefulWidget {
  const HomePage({super.key, required this.client, required this.session});

  final DaemonClient client;
  final DaemonSession session;

  @override
  State<HomePage> createState() => _HomePageState();
}

/// One row of the flat node/subscription tree. A sealed row model instead
/// of nested ExpansionTiles keeps SliverList.builder lazy on thousands of
/// nodes — latency cells are plain trailing widgets, no nested scrolling.
sealed class _Row {}

final class _NodeRow extends _Row {
  _NodeRow(this.node, {required this.indented});

  final NodeInfo node;
  final bool indented; // true under a subscription header
}

final class _SubHeader extends _Row {
  _SubHeader(this.sub, {required this.expanded});

  final SubscriptionInfo sub;
  final bool expanded;
}

/// An expanded subscription with no nodes — a dim placeholder row.
final class _EmptySub extends _Row {
  _EmptySub(this.sub);

  final SubscriptionInfo sub;
}

class _HomePageState extends State<HomePage> {
  List<NodeInfo>? _nodes;
  List<SubscriptionInfo>? _subs;
  String? _error;
  final Map<String, int?> _latencies = {}; // by node NAME — survives reloads
  final Set<String> _probing = {};
  bool _refreshing = false;

  /// The node NAME mid circle-tap switch flow: spinner in its circle and a
  /// lock against re-entrant taps.
  String? _switching;

  /// Collapsed subscription ids (default expanded); stale ids are dropped
  /// on every load.
  final Set<String> _collapsed = {};

  /// Scroll plumbing so the pipeline's server pill can reveal the node list.
  final ScrollController _scroll = ScrollController();
  final GlobalKey _listAnchor = GlobalKey();

  late int _seenStoreRev = widget.session.storeRev;
  late bool _connected = widget.session.connected;
  late bool _running = widget.session.isRunning;
  late String? _live = widget.session.activeNodeLive;

  @override
  void initState() {
    super.initState();
    widget.session.addListener(_onSession);
    _load();
  }

  @override
  void dispose() {
    widget.session.removeListener(_onSession);
    _scroll.dispose();
    super.dispose();
  }

  /// Manual session listener (the _ShellState pattern): traffic ticks
  /// notify every second and must not touch the list — reload/setState
  /// only when something the list shows actually changed. StatusHeader
  /// has its own ListenableBuilder, so `connected` flips need no setState
  /// here, just the reload that fixes a list gone stale while the daemon
  /// was away.
  void _onSession() {
    final s = widget.session;
    if (s.storeRev != _seenStoreRev) {
      _seenStoreRev = s.storeRev;
      _load();
    }
    if (s.connected != _connected) {
      final cameUp = s.connected;
      _connected = s.connected;
      if (cameUp) _load();
    }
    if (s.isRunning != _running || s.activeNodeLive != _live) {
      setState(() {
        _running = s.isRunning;
        _live = s.activeNodeLive;
      });
    }
  }

  Future<void> _load() async {
    try {
      final results = await Future.wait<Object>([
        widget.client.listNodes(),
        widget.client.listSubscriptions(),
      ]);
      if (!mounted) return;
      setState(() {
        _nodes = results[0] as List<NodeInfo>;
        _subs = results[1] as List<SubscriptionInfo>;
        _error = null;
        _collapsed.retainAll({for (final s in _subs!) s.id});
      });
    } on ClientException catch (e) {
      if (!mounted) return;
      setState(() => _error = e.message);
    }
  }

  /// The pipeline server pill's tap: bring the node list into view so the user
  /// can switch. A no-op before the first frame lays the anchor out.
  void _revealNodes() {
    final ctx = _listAnchor.currentContext;
    if (ctx == null) return;
    Scrollable.ensureVisible(
      ctx,
      duration: const Duration(milliseconds: 350),
      curve: Curves.easeOut,
      alignment: 0.05,
    );
  }

  // ---- node actions (from the former NodesPage) ----

  /// Probe each node INDEPENDENTLY: one row resolves the instant its own
  /// probe returns, so fast nodes show immediately and a slow/hung node only
  /// spins its OWN row (until the daemon's per-probe budget) — it never gates
  /// the others. Nodes already mid-probe are skipped (idempotent re-taps, and
  /// no two overlapping probes of the same node racing the daemon's stub
  /// socket). See [DaemonClient.testLatencyEach].
  Future<void> _probe(List<String> names) async {
    final pending = names.where((n) => !_probing.contains(n)).toList();
    if (pending.isEmpty) return;
    setState(() => _probing.addAll(pending));
    try {
      await for (final r in widget.client.testLatencyEach(pending)) {
        if (!mounted) return;
        setState(() {
          _probing.remove(r.node);
          _latencies[r.node] = r.latencyMs;
        });
      }
    } on ClientException catch (e) {
      if (mounted) showSnack(context, e.message, error: true);
    } finally {
      // Clear any rows still marked probing (a mid-sweep daemon-unreachable
      // leaves the not-yet-resolved nodes spinning); resolved ones were
      // already removed one-by-one, so this is a no-op for them.
      if (mounted) setState(() => _probing.removeAll(pending));
    }
  }

  /// The "New" button's menu: add a link/subscription, or build an auto group.
  /// A group is a client-side construct over nodes that already exist, so its
  /// entry sits beside Add rather than replacing it.
  Future<void> _showNewMenu() async {
    final choice = await showModalBottomSheet<String>(
      context: context,
      builder: (context) => SafeArea(
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            ListTile(
              leading: const Icon(Icons.link),
              title: const Text('Link or subscription'),
              subtitle: const Text('Add a share link or a subscription URL'),
              onTap: () => Navigator.pop(context, 'add'),
            ),
            ListTile(
              leading: const Icon(Icons.bolt),
              title: const Text('Auto group'),
              subtitle:
                  const Text('A group that auto-picks the fastest node'),
              onTap: () => Navigator.pop(context, 'group'),
            ),
          ],
        ),
      ),
    );
    if (!mounted) return;
    switch (choice) {
      case 'add':
        await _add();
      case 'group':
        await _newGroup();
    }
  }

  /// Build a new user "Auto" group over the nodes/subscriptions that exist. A
  /// group has nothing to reference on a profile with neither, so bail early
  /// with a hint; otherwise ensure a profile, prompt for the spec, and upsert.
  /// Only subscriptions that actually resolve to a dialable member are offered
  /// as an "all of subscription" target — an all_of_sub over a still-empty
  /// subscription would build a dead urltest over zero nodes.
  Future<void> _newGroup() async {
    final nodes = _nodes, subs = _subs;
    if (nodes == null || subs == null) return;
    final dialable = nodes.where((n) => !n.isGroup).toList();
    final groupableSubIds = {
      for (final n in dialable)
        if (n.subId != null) n.subId,
    };
    final groupableSubs =
        subs.where((s) => groupableSubIds.contains(s.id)).toList();
    if (dialable.isEmpty && groupableSubs.isEmpty) {
      showSnack(context, 'Add nodes or a subscription first', error: true);
      return;
    }
    if (!await _ensureActiveProfile() || !mounted) return;
    final spec =
        await promptNewGroup(context, dialable: dialable, subs: groupableSubs);
    if (spec == null || !mounted) return;
    if (await guardOk(context, () => widget.client.upsertGroup(spec))) {
      if (mounted) showSnack(context, 'Group created');
      _load();
    }
  }

  /// One Add for both kinds: http(s):// URLs are fetched as a subscription,
  /// anything else is parsed as a single node. (This is how the old Rust CLI
  /// took a link — no node-vs-sub picker.)
  Future<void> _add() async {
    final input = await promptAdd(context);
    if (input == null || input.isEmpty || !mounted) return;
    if (!await _ensureActiveProfile() || !mounted) return;
    final lower = input.toLowerCase();
    if (lower.startsWith('http://') || lower.startsWith('https://')) {
      setState(() => _refreshing = true);
      final results = await guard(
        context,
        () => widget.client.addSubscription(input),
      );
      if (!mounted) return;
      setState(() => _refreshing = false);
      if (results != null) {
        _reportRefresh(results);
        _load();
      }
    } else {
      final id = await guard(context, () => widget.client.addNode(input));
      if (id != null && mounted) {
        showSnack(context, 'Node added');
        _load();
      }
    }
  }

  /// Adding the first node/subscription on a fresh install has nowhere to land
  /// until a profile exists and is selected. Rather than make the user create
  /// one first, ensure an active profile: adopt the only/first one if some
  /// exist but none is active, else create a "default" and select it. Returns
  /// false (after the guard's snackbar) if the daemon couldn't oblige.
  Future<bool> _ensureActiveProfile() async {
    final profiles = await guard(context, widget.client.listProfiles);
    if (profiles == null) return false; // daemon down — already reported
    if (profiles.activeProfile != null && profiles.activeProfile!.isNotEmpty) {
      return true;
    }
    if (!mounted) return false;
    final target = profiles.profiles.isNotEmpty
        ? profiles.profiles.first
        : 'default';
    if (profiles.profiles.isEmpty) {
      if (!await guardOk(
        context,
        () => widget.client.createProfile('default'),
      )) {
        return false;
      }
      if (!mounted) return false;
    }
    final ok = await guardOk(
      context,
      () => widget.client.setActiveProfile(target),
    );
    if (ok) widget.session.noteStoreChanged();
    return ok;
  }

  Future<void> _diagnose(NodeInfo node) async {
    final verdict = await guard(
      context,
      () => widget.client.diagnose(node.name),
    );
    if (verdict == null || !mounted) return;
    await showDialog<void>(
      context: context,
      builder: (context) => DiagnosisDialog(verdict: verdict),
    );
  }

  Future<void> _onAction(String action, NodeInfo node) async {
    switch (action) {
      case 'switch':
        final name = await guard(
          context,
          () => widget.client.switchNode(node.name),
        );
        if (name != null && mounted) {
          showSnack(context, 'Switched to $name');
          await widget.session.refreshStatus();
        }
      case 'select':
        if (await guardOk(context, () => widget.client.selectNode(node.id))) {
          _load();
        }
      case 'test':
        await _probe([node.name]);
      case 'diagnose':
        await _diagnose(node);
      case 'pin_sing':
        await _setPin(node, CoreType.singBox);
      case 'pin_xray':
        await _setPin(node, CoreType.xray);
      case 'pin_clear':
        await _setPin(node, null);
      case 'remove':
        if (!mounted) return;
        if (await confirm(
          context,
          'Remove node',
          'Remove "${node.name}" from the profile?',
        )) {
          if (!mounted) return;
          if (await guardOk(context, () => widget.client.removeNode(node.id))) {
            _load();
          }
        }
    }
  }

  Future<void> _setPin(NodeInfo node, String? core) async {
    if (await guardOk(
      context,
      () => widget.client.setNodePrefs(node.id, coreOverride: core),
    )) {
      _load();
    }
  }

  /// The circle tap — ONE atomic click, no confirm dialog:
  ///   * idle    → `select_node` (set the profile default; no live effect);
  ///   * running → `switch_node` (live-switch traffic there), then
  ///               `select_node` (persist the default), then a status re-read
  ///               so the play icon lands on the node the selector actually
  ///               moved to.
  /// switch_node returns only after the in-process selector has synchronously
  /// moved (sing-box atomic swap), so the follow-up refreshStatus reads the new
  /// live node authoritatively — and the session drops any stale poll that was
  /// in flight, so the readback can't be clobbered back onto the old node (the
  /// former two-step race). switch and select stay separate daemon verbs (the
  /// expert menu keeps them apart); only this convenience tap composes them.
  /// A switch that fails (daemon stopped) leaves the default untouched; a
  /// select that fails after a good switch shows play on the live node with the
  /// radio on the old default — a second tap repairs it. An xray-only node
  /// reactivates inside `switch_node` (seconds) — covered by the [_switching]
  /// spinner and the tap lock, so a second tap can't double-fire.
  Future<void> _onCircleTap(NodeInfo node) async {
    if (_switching != null) return;
    if (!widget.session.isRunning) {
      // idle → just set the profile default, no live switch
      if (await guardOk(context, () => widget.client.selectNode(node.id))) {
        _load();
      }
      return;
    }
    setState(() => _switching = node.name);
    final name = await guard(
      context,
      () => widget.client.switchNode(node.name),
    );
    if (name != null && mounted) {
      // live switch ok → persist the default too (one click = switch + default)
      await guardOk(context, () => widget.client.selectNode(node.id));
      if (mounted) showSnack(context, 'Switched to $name');
    }
    // Re-read the authoritative live node last so the play icon reflects where
    // the selector actually landed (reconciles even when the switch failed).
    await widget.session.refreshStatus();
    if (mounted) setState(() => _switching = null);
    _load();
  }

  // ---- subscription actions (from the former SubscriptionsPage) ----

  void _reportRefresh(List<RefreshInfo> results) {
    final lines = results.map((r) {
      if (r.error != null) return '${r.name}: ${r.error}';
      if (r.skipped) return '${r.name}: skipped (disabled)';
      // An empty format is an older daemon without the parse accounting.
      if (r.format.isEmpty) {
        return '${r.name}: ${r.count} nodes (+${r.added} −${r.removed})';
      }
      final line = '${r.name}: ${r.format} · ${r.entries} entries '
          '→ ${r.count} nodes (+${r.added} −${r.removed})';
      return r.unrecognized > 0
          ? '$line · ${r.unrecognized} unrecognized'
          : line;
    });
    showSnack(
      context,
      lines.join('\n'),
      error: results.any((r) => r.error != null),
    );
  }

  Future<void> _refresh({String? sub}) async {
    setState(() => _refreshing = true);
    final results = await guard(
      context,
      () => widget.client.refreshSubscriptions(sub: sub),
    );
    if (!mounted) return;
    setState(() => _refreshing = false);
    if (results != null) {
      _reportRefresh(results);
      _load();
    }
  }

  // ---- row assembly ----

  /// Standalone nodes first, in daemon order, then each subscription's
  /// header with its nodes (when expanded). A node whose subId matches no
  /// listed subscription counts as standalone — a mid-refresh desync must
  /// not make nodes vanish.
  List<_Row> _rows(List<NodeInfo> nodes, List<SubscriptionInfo> subs) {
    final subIds = {for (final s in subs) s.id};
    final bySub = <String, List<NodeInfo>>{};
    final rows = <_Row>[];
    for (final node in nodes) {
      final subId = node.subId;
      if (subId != null && subIds.contains(subId)) {
        (bySub[subId] ??= []).add(node);
      } else {
        rows.add(_NodeRow(node, indented: false));
      }
    }
    for (final sub in subs) {
      final expanded = !_collapsed.contains(sub.id);
      rows.add(_SubHeader(sub, expanded: expanded));
      if (!expanded) continue;
      final group = bySub[sub.id] ?? const <NodeInfo>[];
      if (group.isEmpty) {
        rows.add(_EmptySub(sub));
      } else {
        for (final node in group) {
          rows.add(_NodeRow(node, indented: true));
        }
      }
    }
    return rows;
  }

  /// Whether [r] is part of a subscription group (a child node or the empty
  /// placeholder) — used to tighten the spacing above it.
  static bool _grouped(_Row r) => switch (r) {
    _NodeRow(:final indented) => indented,
    _EmptySub() => true,
    _ => false,
  };

  /// A privacy-masked rendering of a subscription URL for inline display: the
  /// host stays, the path/token tail is hidden behind bullets. The full link is
  /// only ever shown in the Edit… dialog. Used only as a fallback label when a
  /// subscription has no name.
  static String _maskedUrl(String url) {
    final uri = Uri.tryParse(url.trim());
    if (uri == null || uri.host.isEmpty) {
      final u = url.trim();
      return u.length <= 24 ? u : '${u.substring(0, 24)}…';
    }
    final hasTail =
        uri.path.replaceAll('/', '').isNotEmpty || uri.query.isNotEmpty;
    return hasTail ? '${uri.host}/••••' : uri.host;
  }

  /// Indents a subscription's child row so its nodes read as nested under the
  /// header (the indent + the tighter group spacing carry the grouping — no
  /// rail line).
  Widget _nested(Widget child) =>
      Padding(padding: const EdgeInsets.only(left: 26), child: child);

  @override
  Widget build(BuildContext context) {
    final t = context.iron;
    final nodes = _nodes;
    final subs = _subs;
    final loaded = nodes != null && subs != null;
    final rows = loaded ? _rows(nodes, subs) : const <_Row>[];
    // One CustomScrollView so the hero scrolls away over a long node list
    // (Column + Expanded(ListView) would cost the height for good).
    return Scaffold(
      backgroundColor: t.panel,
      floatingActionButton: FloatingActionButton.extended(
        onPressed: _showNewMenu,
        icon: const Icon(Icons.add),
        label: const Text('New'),
      ),
      body: CustomScrollView(
        controller: _scroll,
        slivers: [
          SliverPadding(
            padding: const EdgeInsets.fromLTRB(24, 24, 24, 0),
            sliver: SliverToBoxAdapter(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  Padding(
                    padding: const EdgeInsets.only(bottom: 20),
                    child: Text('Home', style: context.screenTitle),
                  ),
                  StatusHeader(
                    client: widget.client,
                    session: widget.session,
                    onServerTap: _revealNodes,
                  ),
                ],
              ),
            ),
          ),
          SliverToBoxAdapter(child: _toolbar(nodes, subs)),
          if (_error != null)
            SliverToBoxAdapter(
              child: Padding(
                padding: const EdgeInsets.symmetric(horizontal: 24),
                child: Text(_error!, style: TextStyle(color: t.danger)),
              ),
            ),
          if (!loaded)
            const SliverToBoxAdapter(
              child: Padding(
                padding: EdgeInsets.all(48),
                child: Center(child: CircularProgressIndicator()),
              ),
            )
          else if (rows.isEmpty)
            SliverToBoxAdapter(
              child: Padding(
                padding: const EdgeInsets.all(48),
                child: Center(
                  child: Text(
                    'No nodes — add a share link or a subscription.',
                    style: Theme.of(
                      context,
                    ).textTheme.bodyMedium?.copyWith(color: t.dim),
                  ),
                ),
              ),
            )
          else
            SliverPadding(
              padding: const EdgeInsets.symmetric(horizontal: 24),
              sliver: SliverList.builder(
                itemCount: rows.length,
                itemBuilder: (context, i) {
                  // Tighten the gap before a row that belongs to a subscription
                  // group (its child nodes / empty placeholder), so a header and
                  // its nodes read as one block, distinct from the looser
                  // spacing between standalone cards.
                  final nextGrouped =
                      i + 1 < rows.length && _grouped(rows[i + 1]);
                  return Padding(
                    padding: EdgeInsets.only(bottom: nextGrouped ? 4 : 8),
                    child: switch (rows[i]) {
                      _NodeRow(:final node, :final indented) => _nodeRow(
                        node,
                        indented: indented,
                      ),
                      _SubHeader(:final sub, :final expanded) => _subHeader(
                        sub,
                        expanded: expanded,
                      ),
                      _EmptySub(:final sub) => _emptySubRow(sub),
                    },
                  );
                },
              ),
            ),
          // Clearance so the FAB never covers the last row's menu button.
          const SliverToBoxAdapter(child: SizedBox(height: 88)),
        ],
      ),
    );
  }

  Widget _toolbar(List<NodeInfo>? nodes, List<SubscriptionInfo>? subs) {
    return Padding(
      // The anchor lives here so the pipeline pill scrolls the section title
      // (and the rows just below it) into view.
      key: _listAnchor,
      padding: const EdgeInsets.fromLTRB(24, 8, 24, 12),
      child: Row(
        children: [
          Text('Nodes', style: Theme.of(context).textTheme.titleLarge),
          const Spacer(),
          IronButton(
            label: 'Test all',
            icon: Icons.speed,
            // A group has no endpoint to probe; only dialable nodes are tested.
            onPressed: nodes == null || !nodes.any((n) => !n.isGroup)
                ? null
                : () => _probe(nodes
                    .where((n) => !n.isGroup)
                    .map((n) => n.name)
                    .toList()),
          ),
          const SizedBox(width: 8),
          IronButton(
            label: 'Refresh subs',
            icon: _refreshing ? null : Icons.sync,
            onPressed: _refreshing || subs == null || subs.isEmpty
                ? null
                : () => _refresh(),
          ),
        ],
      ),
    );
  }

  Widget _nodeRow(NodeInfo node, {required bool indented}) {
    final t = context.iron;
    final live = widget.session.activeNodeLive;
    final isLive = live != null && node.name == live;
    final tooltip = isLive
        ? 'Live node'
        : widget.session.isRunning
        ? 'Switch traffic here'
        : node.active
        ? 'Profile default'
        : 'Set as profile default';

    // The leading circle: a live play, the active radio, or an idle ring.
    final circleColor = isLive
        ? context.appColors.statusRunning
        : node.active
        ? t.accent
        : t.dim;
    final circle = _switching == node.name
        ? SizedBox(
            width: 20,
            height: 20,
            child: CircularProgressIndicator(strokeWidth: 2, color: t.accent),
          )
        : Icon(
            isLive
                ? Icons.play_circle
                : node.active
                ? Icons.radio_button_checked
                : Icons.radio_button_off,
            color: circleColor,
          );

    final card = IronCard(
      padding: const EdgeInsets.fromLTRB(12, 10, 8, 10),
      child: Row(
        children: [
          IconButton(
            tooltip: tooltip,
            onPressed: () => _onCircleTap(node),
            icon: circle,
          ),
          const SizedBox(width: 4),
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              mainAxisSize: MainAxisSize.min,
              children: [
                Text(
                  node.name,
                  overflow: TextOverflow.ellipsis,
                  style: Theme.of(context).textTheme.bodyLarge,
                ),
                const SizedBox(height: 5),
                Wrap(
                  spacing: 6,
                  runSpacing: 4,
                  children: [
                    if (node.isGroup) ...[
                      // A group ("Auto"): the auto badge, its member count, and —
                      // when it is the active node and the live pick differs — the
                      // member currently carrying traffic ("now: X").
                      IronKindChip('auto', accent: true),
                      IronKindChip('${node.members.length} nodes'),
                      if (node.active &&
                          widget.session.isRunning &&
                          live != null &&
                          live != node.name)
                        IronKindChip('now: $live', accent: true),
                    ] else ...[
                      // The proxy protocol (vless/shadowsocks/vmess/trojan/
                      // hysteria2/…), then the transport/security kinds when the
                      // protocol has them (QUIC protocols like hysteria2/tuic
                      // carry neither).
                      if (node.protocol.isNotEmpty) IronKindChip(node.protocol),
                      if (node.transport.isNotEmpty) IronKindChip(node.transport),
                      if (node.security.isNotEmpty) IronKindChip(node.security),
                      if (node.coreOverride != null)
                        IronKindChip('pin: ${node.coreOverride}', accent: true),
                    ],
                  ],
                ),
              ],
            ),
          ),
          const SizedBox(width: 8),
          SizedBox(
            width: 64,
            // A group has no latency of its own (its members are probed
            // individually) — no spinner, no reading.
            child: node.isGroup
                ? null
                : _probing.contains(node.name)
                ? Center(
                    child: SizedBox(
                      width: 16,
                      height: 16,
                      child: CircularProgressIndicator(
                        strokeWidth: 2,
                        color: t.accent,
                      ),
                    ),
                  )
                : Text(
                    _latencies.containsKey(node.name)
                        ? formatLatency(_latencies[node.name])
                        : '',
                    textAlign: TextAlign.end,
                    style: context.mono(12.5, color: t.dim),
                  ),
          ),
          PopupMenuButton<String>(
            icon: Icon(Icons.more_vert, color: t.dim),
            onSelected: (action) => _onAction(action, node),
            itemBuilder: (context) => [
              const PopupMenuItem(
                value: 'switch',
                child: Text('Switch traffic here'),
              ),
              const PopupMenuItem(
                value: 'select',
                child: Text('Set as profile default'),
              ),
              // A group has no endpoint of its own: latency, diagnosis, and a
              // core pin apply to its members, not to it.
              if (!node.isGroup) ...[
                const PopupMenuDivider(),
                const PopupMenuItem(value: 'test', child: Text('Test latency')),
                const PopupMenuItem(value: 'diagnose', child: Text('Diagnose')),
                const PopupMenuDivider(),
                PopupMenuItem(
                  value: 'pin_sing',
                  child: Text(
                    node.coreOverride == CoreType.singBox
                        ? '✓ Pin to sing-box'
                        : 'Pin to sing-box',
                  ),
                ),
                PopupMenuItem(
                  value: 'pin_xray',
                  child: Text(
                    node.coreOverride == CoreType.xray
                        ? '✓ Pin to xray'
                        : 'Pin to xray',
                  ),
                ),
                const PopupMenuItem(
                  value: 'pin_clear',
                  child: Text('Clear pin'),
                ),
              ],
              const PopupMenuDivider(),
              const PopupMenuItem(value: 'remove', child: Text('Remove')),
            ],
          ),
        ],
      ),
    );
    // Standalone nodes are full-width cards; a subscription's nodes are
    // indented so they read as nested under their header.
    return indented ? _nested(card) : card;
  }

  Widget _subHeader(SubscriptionInfo sub, {required bool expanded}) {
    final t = context.iron;
    final enabled = sub.enabled;
    // Name first; only fall back to a privacy-masked URL when there's no name.
    // The full URL is never shown inline — it lives in the Edit… dialog.
    final hasName = sub.name.trim().isNotEmpty;
    final title = hasName ? sub.name : _maskedUrl(sub.url);
    final titleColor = enabled ? t.text : t.faint;
    return IronCard(
      onTap: () => setState(() {
        expanded ? _collapsed.add(sub.id) : _collapsed.remove(sub.id);
      }),
      padding: const EdgeInsets.fromLTRB(10, 10, 8, 10),
      child: Row(
        children: [
          // The accent tile + heavier (Space Grotesk) title set the
          // subscription apart as a group header, vs the plain node cards.
          IronIconTile(
            Icons.layers_outlined,
            color: enabled ? t.accent : t.faint,
          ),
          const SizedBox(width: 12),
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              mainAxisSize: MainAxisSize.min,
              children: [
                Row(
                  children: [
                    Flexible(
                      child: Text(
                        title,
                        overflow: TextOverflow.ellipsis,
                        style: Theme.of(
                          context,
                        ).textTheme.titleMedium?.copyWith(color: titleColor),
                      ),
                    ),
                    if (!enabled) ...[
                      const SizedBox(width: 8),
                      Icon(
                        Icons.pause_circle_outline,
                        size: 16,
                        color: t.faint,
                      ),
                    ],
                    if (sub.allowInvalidCerts) ...[
                      const SizedBox(width: 8),
                      Tooltip(
                        message: 'TLS verification disabled for this source',
                        child: Icon(
                          Icons.gpp_bad,
                          size: 16,
                          color: context.appColors.warning,
                        ),
                      ),
                    ],
                  ],
                ),
                const SizedBox(height: 2),
                Text(
                  '${sub.nodeCount} ${sub.nodeCount == 1 ? 'node' : 'nodes'} · '
                  'updated ${formatTimestamp(sub.lastUpdated)}',
                  overflow: TextOverflow.ellipsis,
                  style: Theme.of(
                    context,
                  ).textTheme.bodySmall?.copyWith(color: t.faint),
                ),
              ],
            ),
          ),
          Icon(
            expanded ? Icons.keyboard_arrow_up : Icons.keyboard_arrow_down,
            color: t.dim,
          ),
          const SizedBox(width: 4),
          IronIconButton(
            icon: Icons.sync,
            tooltip: 'Refresh',
            onPressed: _refreshing ? null : () => _refresh(sub: sub.id),
          ),
          PopupMenuButton<String>(
            icon: Icon(Icons.more_vert, color: t.dim),
            onSelected: (action) => _onSubAction(action, sub),
            itemBuilder: (context) => const [
              PopupMenuItem(value: 'edit', child: Text('Edit…')),
              PopupMenuItem(
                value: 'remove',
                child: Text('Remove (deletes its nodes)'),
              ),
            ],
          ),
        ],
      ),
    );
  }

  Future<void> _onSubAction(String action, SubscriptionInfo sub) async {
    switch (action) {
      case 'edit':
        await _editSubscription(sub);
      case 'remove':
        if (!mounted) return;
        if (await confirm(
          context,
          'Remove subscription',
          'Remove "${sub.name}" and ALL its nodes?',
        )) {
          if (!mounted) return;
          if (await guardOk(
            context,
            () => widget.client.removeSubscription(sub.id),
          )) {
            _load();
          }
        }
    }
  }

  Future<void> _editSubscription(SubscriptionInfo sub) async {
    final edit = await promptEditSubscription(context, sub);
    if (edit == null || !mounted) return;
    final ok = await guardOk(
      context,
      () => widget.client.updateSubscription(
        sub.id,
        name: edit.name,
        url: edit.url,
        enabled: edit.enabled,
        allowInvalidCerts: edit.allowInvalidCerts,
        format: edit.format,
      ),
    );
    if (ok && mounted) {
      showSnack(context, 'Subscription updated');
      _load();
    }
  }

  Widget _emptySubRow(SubscriptionInfo sub) {
    final t = context.iron;
    return _nested(
      IronCard(
        key: ValueKey('empty-${sub.id}'),
        padding: const EdgeInsets.fromLTRB(12, 12, 16, 12),
        child: Text(
          '(no nodes — refresh?)',
          style: Theme.of(
            context,
          ).textTheme.bodySmall?.copyWith(color: t.faint),
        ),
      ),
    );
  }
}
