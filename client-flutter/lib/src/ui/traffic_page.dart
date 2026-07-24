import 'dart:async';
import 'dart:io';
import 'dart:math' as math;
import 'dart:ui' as ui;

import 'package:flutter/material.dart';

import '../app/formats.dart';
import '../app/session.dart';
import '../ipc/client.dart';
import '../platform/app_icons.dart';
import '../wire/wire.dart';
import 'theme/app_colors.dart';
import 'theme/app_typography.dart';
import 'widgets/iron_widgets.dart';

/// The "Traffic" tab: the running session's throughput (a live up/down readout
/// + a short sparkline of the last ~30s) over a per-application breakdown of the
/// routed bytes (current rate + cumulative total). Polled while this tab is
/// visible; switching away disposes it, so the poll stops.
class TrafficPage extends StatelessWidget {
  const TrafficPage({super.key, required this.client, required this.session});

  final DaemonClient client;
  final DaemonSession session;

  @override
  Widget build(BuildContext context) =>
      _ByApp(client: client, session: session);
}

/// How the app list is ordered. Default is [name] — a STABLE order, so rows
/// don't reshuffle on every poll; [rate] (live, so it jitters) is opt-in.
enum _Sort { name, total, rate }

String _sortLabel(_Sort s) => switch (s) {
      _Sort.name => 'Name',
      _Sort.total => 'Total',
      _Sort.rate => 'Rate',
    };

/// The "by app" list: while a session runs it polls `traffic_apps` every ~1.5s,
/// keeps the previous cumulative sample to derive a per-app rate, and lists
/// apps by rate descending (basename + current rate + cumulative total).
class _ByApp extends StatefulWidget {
  const _ByApp({required this.client, required this.session});

  final DaemonClient client;
  final DaemonSession session;

  @override
  State<_ByApp> createState() => _ByAppState();
}

/// One app's derived view: cumulative totals + the rate since the last poll +
/// the per-route-outcome split (only outcomes with nonzero bytes present).
class _AppView {
  _AppView(this.path, this.up, this.down, this.upRate, this.downRate,
      this.byRoute);

  final String path;
  final int up;
  final int down;
  final int upRate; // bytes/sec since the previous sample
  final int downRate;
  final Map<String, RouteBytes> byRoute;

  int get rate => upRate + downRate;
}

/// The route outcomes in display order, each with its label and the theme role
/// it colours from (proxy = primary, direct = neutral, blocked = error, dpi =
/// amber). Only the outcomes present in an app's `by_route` render.
const _routeOrder = ['proxy', 'direct', 'blocked', 'dpi'];
const _routeLabels = {
  'proxy': 'proxy',
  'direct': 'direct',
  'blocked': 'blocked',
  'dpi': 'dpi',
};

/// How many tail samples of [DaemonSession.traffic] the line chart shows
/// (~the last minute, the subtitle's window).
const _chartPoints = 60;

class _ByAppState extends State<_ByApp> {
  static const _pollInterval = Duration(milliseconds: 1500);

  Timer? _timer;

  /// Resolves an app's executable path → its real icon (best-effort: Linux via
  /// the .desktop database, Windows via the EXE's PE icon; else letter-avatar).
  final AppIconResolver _icons = AppIconResolver();

  /// Per-path resolved icon: absent = not looked up yet, present-but-null =
  /// resolved-to-nothing (use the letter-avatar). Each path is resolved once,
  /// off the build, with a setState when it lands so the list doesn't jank.
  final Map<String, File?> _iconFiles = {};

  /// The previous cumulative sample (by path) and its timestamp — the basis
  /// for the rate derivation.
  Map<String, AppTraffic> _last = const {};
  DateTime? _lastAt;

  List<_AppView> _views = const [];
  late bool _running = widget.session.isRunning;

  /// Current list order. Default [_Sort.name] keeps rows stable across polls.
  _Sort _sort = _Sort.name;

  @override
  void initState() {
    super.initState();
    widget.session.addListener(_onSession);
    if (_running) _start();
  }

  @override
  void dispose() {
    widget.session.removeListener(_onSession);
    _timer?.cancel();
    super.dispose();
  }

  void _onSession() {
    final running = widget.session.isRunning;
    if (running == _running) return;
    setState(() => _running = running);
    if (running) {
      _start();
    } else {
      _stop();
    }
  }

  void _start() {
    _poll(); // one immediate sample so the list fills without a 1.5s wait
    _timer ??= Timer.periodic(_pollInterval, (_) => _poll());
  }

  void _stop() {
    _timer?.cancel();
    _timer = null;
    setState(() {
      _last = const {};
      _lastAt = null;
      _views = const [];
    });
  }

  Future<void> _poll() async {
    final List<AppTraffic> apps;
    try {
      apps = await widget.client.trafficApps();
    } on ClientException {
      return; // daemon hiccup — keep the last frame, try again next tick
    }
    if (!mounted) return;
    final now = DateTime.now();
    final dt = _lastAt == null
        ? 0.0
        : now.difference(_lastAt!).inMilliseconds / 1000.0;
    final views = <_AppView>[];
    for (final a in apps) {
      final prev = _last[a.path];
      // Cumulative deltas → bytes/sec; a fresh app (no prev) or a non-positive
      // dt shows zero rate this tick, real totals immediately.
      final upRate = (prev == null || dt <= 0)
          ? 0
          : ((a.up - prev.up) / dt).round().clamp(0, 1 << 62);
      final downRate = (prev == null || dt <= 0)
          ? 0
          : ((a.down - prev.down) / dt).round().clamp(0, 1 << 62);
      views.add(_AppView(a.path, a.up, a.down, upRate, downRate, a.byRoute));
    }
    setState(() {
      _last = {for (final a in apps) a.path: a};
      _lastAt = now;
      _views = _sorted(views);
    });
  }

  /// Orders [views] by the current [_sort]. A path tiebreak keeps rows with
  /// equal keys (e.g. idle apps at rate 0) from jittering between polls.
  List<_AppView> _sorted(List<_AppView> views) {
    int byPath(_AppView a, _AppView b) => a.path.compareTo(b.path);
    final out = [...views];
    switch (_sort) {
      case _Sort.name:
        out.sort((a, b) {
          final c = _appBasename(a.path)
              .toLowerCase()
              .compareTo(_appBasename(b.path).toLowerCase());
          return c != 0 ? c : byPath(a, b);
        });
      case _Sort.total:
        out.sort((a, b) {
          final c = (b.up + b.down).compareTo(a.up + a.down);
          return c != 0 ? c : byPath(a, b);
        });
      case _Sort.rate:
        out.sort((a, b) {
          final c = b.rate.compareTo(a.rate);
          return c != 0 ? c : byPath(a, b);
        });
    }
    return out;
  }

  @override
  Widget build(BuildContext context) {
    final t = context.iron;
    return ListView(
      padding: const EdgeInsets.fromLTRB(24, 16, 24, 88),
      children: [
        _titleRow(context),
        const SizedBox(height: 4),
        ..._content(context, t),
      ],
    );
  }

  /// The screen title + subtitle, with the sort control parked on the right
  /// (only while a running session has rows to order).
  Widget _titleRow(BuildContext context) {
    final t = context.iron;
    return Row(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Expanded(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text('Traffic', style: context.screenTitle),
              const SizedBox(height: 2),
              Text('Throughput over the last 60 seconds.',
                  style: Theme.of(context)
                      .textTheme
                      .bodyMedium
                      ?.copyWith(color: t.dim)),
            ],
          ),
        ),
        if (_running && _views.isNotEmpty) ...[
          const SizedBox(width: 12),
          Padding(
            padding: const EdgeInsets.only(top: 2),
            child: _sortControl(context),
          ),
        ],
      ],
    );
  }

  /// The body below the title: the not-running notice, or the live session
  /// stats + chart followed by the per-app list (or its empty notice).
  List<Widget> _content(BuildContext context, IronTheme t) {
    if (!_running) {
      return [
        const SizedBox(height: 48),
        _notice(context, 'Connect a session to see per-app traffic.'),
      ];
    }
    return [
      const SizedBox(height: 16),
      // The session-level readout repaints every second off the traffic feed,
      // independent of the slower 1.5s per-app poll.
      AnimatedBuilder(
        animation: widget.session,
        builder: (context, _) => _trafficCard(context, t),
      ),
      const SizedBox(height: 20),
      if (_views.isEmpty)
        _notice(context, 'No per-app traffic yet.')
      else
        ..._appList(),
    ];
  }

  /// The throughput card: the live up/down readouts (rate + cumulative total)
  /// as the chart's legend, above two smooth curves — Upload (accent) and
  /// Download (neutral) — over the last ~minute of [DaemonSession.traffic].
  Widget _trafficCard(BuildContext context, IronTheme t) {
    final rate = widget.session.rate;
    final points = widget.session.traffic;
    final tail = points.length <= _chartPoints
        ? points.toList()
        : points.toList().sublist(points.length - _chartPoints);
    return IronCard(
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Row(
            children: [
              _legend(
                context,
                t,
                color: t.accent,
                label: 'Upload',
                rate: rate.up,
                total: widget.session.totalUp,
              ),
              const SizedBox(width: 28),
              _legend(
                context,
                t,
                color: t.dim,
                label: 'Download',
                rate: rate.down,
                total: widget.session.totalDown,
              ),
            ],
          ),
          const SizedBox(height: 18),
          SizedBox(
            height: 150,
            width: double.infinity,
            child: tail.length < 2
                ? Center(
                    child: Text('Waiting for traffic…',
                        style: Theme.of(context)
                            .textTheme
                            .bodySmall
                            ?.copyWith(color: t.faint)),
                  )
                : CustomPaint(
                    painter: _TrafficChartPainter(
                      tail: tail,
                      up: t.accent,
                      down: t.dim,
                    ),
                  ),
          ),
        ],
      ),
    );
  }

  /// One direction's legend entry — a coloured line swatch (matching its curve),
  /// the uppercase label, the current rate big in mono, and the cumulative
  /// total below. This is the up/down data, now folded onto the chart itself.
  Widget _legend(
    BuildContext context,
    IronTheme t, {
    required Color color,
    required String label,
    required int rate,
    required int total,
  }) {
    return Row(
      mainAxisSize: MainAxisSize.min,
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Container(
          width: 16,
          height: 3,
          margin: const EdgeInsets.only(top: 7),
          decoration: BoxDecoration(
            color: color,
            borderRadius: BorderRadius.circular(2),
          ),
        ),
        const SizedBox(width: 9),
        Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          mainAxisSize: MainAxisSize.min,
          children: [
            Text(label.toUpperCase(),
                style: TextStyle(
                  color: t.faint,
                  fontSize: 11,
                  fontWeight: FontWeight.w700,
                  letterSpacing: 0.8,
                )),
            const SizedBox(height: 4),
            Text(formatRate(rate),
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
                style: context.mono(20, weight: FontWeight.w600, color: t.text)),
            const SizedBox(height: 2),
            Text('total ${formatBytes(total)}',
                style: context.monoBodySmall?.copyWith(color: t.dim)),
          ],
        ),
      ],
    );
  }

  /// The per-app rows. Each is its own [IronCard] so the list reads as a stack
  /// of cards rather than the old divided list.
  List<Widget> _appList() {
    final out = <Widget>[];
    for (var i = 0; i < _views.length; i++) {
      if (i > 0) out.add(const SizedBox(height: 10));
      out.add(_appTile(_views[i]));
    }
    return out;
  }

  /// The header sort selector — Name (stable, default) / Total / Rate (live).
  /// Picking one re-sorts the current list immediately, not just next poll.
  Widget _sortControl(BuildContext context) {
    final t = context.iron;
    return PopupMenuButton<_Sort>(
      initialValue: _sort,
      tooltip: 'Sort the app list',
      onSelected: (s) => setState(() {
        _sort = s;
        _views = _sorted(_views);
      }),
      itemBuilder: (_) => [
        for (final s in _Sort.values)
          CheckedPopupMenuItem(
              value: s, checked: s == _sort, child: Text(_sortLabel(s))),
      ],
      child: Container(
        padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 8),
        decoration: BoxDecoration(
          color: t.raised,
          borderRadius: BorderRadius.circular(11),
          border: Border.all(color: t.border),
        ),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            Icon(Icons.sort, size: 16, color: t.dim),
            const SizedBox(width: 6),
            Text('Sort: ${_sortLabel(_sort)}',
                style: TextStyle(
                    fontSize: 13, fontWeight: FontWeight.w600, color: t.text)),
          ],
        ),
      ),
    );
  }

  /// Kicks off a one-time async icon resolution for [path] the first time we
  /// see it; setState when it lands so the tile swaps the letter-avatar for the
  /// real icon. Subsequent sightings are no-ops (the key is already present).
  void _ensureIcon(String path) {
    if (_iconFiles.containsKey(path)) return;
    _iconFiles[path] = null; // mark in-flight so we don't re-resolve
    _icons.resolve(path).then((file) {
      if (!mounted || file == null) return;
      setState(() => _iconFiles[path] = file);
    });
  }

  /// A calm centred notice — the not-running / empty states.
  Widget _notice(BuildContext context, String message) {
    final t = context.iron;
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 24),
      child: Center(
        child: Text(message,
            textAlign: TextAlign.center,
            style: Theme.of(context)
                .textTheme
                .bodyMedium
                ?.copyWith(color: t.dim)),
      ),
    );
  }

  /// The leading avatar: the resolved app icon (rounded, downscaled) when one
  /// is available, else the letter-avatar. A corrupt/unreadable icon file
  /// (errorBuilder) also falls back to the letter so the row never breaks.
  Widget _appAvatar(IronTheme t, String letter, File? icon) {
    final fallback = Container(
      width: 38,
      height: 38,
      alignment: Alignment.center,
      decoration: BoxDecoration(
        color: t.accentSoft,
        borderRadius: BorderRadius.circular(11),
      ),
      child: Text(letter,
          style: TextStyle(
              color: t.accentStrong,
              fontSize: 16,
              fontWeight: FontWeight.w700)),
    );
    if (icon == null) return fallback;
    return ClipRRect(
      borderRadius: BorderRadius.circular(11),
      child: Image.file(
        icon,
        width: 38,
        height: 38,
        fit: BoxFit.contain,
        filterQuality: FilterQuality.medium,
        errorBuilder: (_, _, _) => fallback,
      ),
    );
  }

  Widget _appTile(_AppView v) {
    final t = context.iron;
    final name = _appBasename(v.path);
    final letter = name.isEmpty ? '?' : name[0].toUpperCase();
    _ensureIcon(v.path); // resolve the real icon once, off the build
    return IronCard(
      padding: const EdgeInsets.all(14),
      child: Row(
        children: [
          _appAvatar(t, letter, _iconFiles[v.path]),
          const SizedBox(width: 14),
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(name,
                    overflow: TextOverflow.ellipsis,
                    style: Theme.of(context)
                        .textTheme
                        .bodyLarge
                        ?.copyWith(color: t.text)),
                const SizedBox(height: 2),
                Text(
                  'total ↑ ${formatBytes(v.up)} · ↓ ${formatBytes(v.down)}',
                  style: context.monoBodySmall?.copyWith(color: t.dim),
                ),
                if (v.byRoute.isNotEmpty)
                  Padding(
                    padding: const EdgeInsets.only(top: 8),
                    child: _routeBreakdown(v),
                  ),
              ],
            ),
          ),
          const SizedBox(width: 8),
          Column(
            crossAxisAlignment: CrossAxisAlignment.end,
            children: [
              Text('↑ ${formatRate(v.upRate)}',
                  style: context.monoBodySmall?.copyWith(color: t.text)),
              const SizedBox(height: 2),
              Text('↓ ${formatRate(v.downRate)}',
                  style: context.monoBodySmall?.copyWith(color: t.dim)),
            ],
          ),
        ],
      ),
    );
  }

  /// A compact, colour-coded row of the app's per-route split: one chip per
  /// outcome present in `by_route` (in [_routeOrder]), each showing its up+down
  /// bytes. The `by_route` map only carries nonzero outcomes, so the absent
  /// ones (e.g. `dpi` until byedpi exists) simply don't render.
  Widget _routeBreakdown(_AppView v) {
    final chips = <Widget>[];
    for (final outcome in _routeOrder) {
      final rb = v.byRoute[outcome];
      if (rb == null) continue;
      chips.add(_routeChip(outcome, rb));
    }
    return Wrap(spacing: 6, runSpacing: 6, children: chips);
  }

  Widget _routeChip(String outcome, RouteBytes rb) {
    final color = _routeColor(outcome, context.appColors);
    final label = _routeLabels[outcome] ?? outcome;
    return IronTag(
      '$label ↑ ${formatBytes(rb.up)} · ↓ ${formatBytes(rb.down)}',
      color: color,
    );
  }

  /// The semantic colour for one route outcome: proxy = the desired path,
  /// direct = neutral, blocked = error, dpi = the DPI-circumvented path.
  Color _routeColor(String outcome, AppColors c) => switch (outcome) {
        'proxy' => c.routeProxy,
        'direct' => c.routeDirect,
        'blocked' => c.routeBlocked,
        'dpi' => c.routeDpi,
        _ => c.routeDirect,
      };
}

/// The basename of a unix/Windows executable path — the last segment after a
/// `/` or `\`. An empty path is the daemon's unattributed bucket.
String _appBasename(String path) {
  if (path.isEmpty) return 'Unattributed';
  final cut = path.lastIndexOf(RegExp(r'[/\\]'));
  final base = cut < 0 ? path : path.substring(cut + 1);
  return base.isEmpty ? path : base;
}

/// Paints the two throughput curves — Upload ([up]) and Download ([down]) —
/// from a window of [TrafficPoint]s. Both share one y-scale (the window's peak
/// across either direction, with a little headroom) so they're comparable; each
/// is a smooth (Catmull-Rom) line over a soft gradient fill, download behind and
/// upload in front.
class _TrafficChartPainter extends CustomPainter {
  _TrafficChartPainter({required this.tail, required this.up, required this.down});

  final List<TrafficPoint> tail;
  final Color up;
  final Color down;

  @override
  void paint(Canvas canvas, Size size) {
    if (tail.length < 2) return;
    // Shared scale: the largest single-direction sample in the window sets the
    // top, +15% headroom so a peak never touches the ceiling. Floor 1 avoids a
    // divide-by-zero on an all-idle window.
    var peak = 1.0;
    for (final p in tail) {
      peak = math.max(peak, math.max(p.up.toDouble(), p.down.toDouble()));
    }
    final scaleMax = peak * 1.15;
    _series(canvas, size, [for (final p in tail) p.down.toDouble()], scaleMax, down);
    _series(canvas, size, [for (final p in tail) p.up.toDouble()], scaleMax, up);
  }

  void _series(
      Canvas canvas, Size size, List<double> values, double scaleMax, Color color) {
    final n = values.length;
    final pts = <Offset>[
      for (var i = 0; i < n; i++)
        Offset(
          n == 1 ? 0 : i / (n - 1) * size.width,
          size.height - (values[i] / scaleMax).clamp(0.0, 1.0) * size.height,
        ),
    ];
    final line = _smooth(pts);
    final fill = Path.from(line)
      ..lineTo(size.width, size.height)
      ..lineTo(0, size.height)
      ..close();
    canvas.drawPath(
      fill,
      Paint()
        ..style = PaintingStyle.fill
        ..shader = ui.Gradient.linear(
          Offset(0, 0),
          Offset(0, size.height),
          [color.withValues(alpha: 0.20), color.withValues(alpha: 0.0)],
        ),
    );
    canvas.drawPath(
      line,
      Paint()
        ..style = PaintingStyle.stroke
        ..strokeWidth = 2.5
        ..strokeCap = StrokeCap.round
        ..strokeJoin = StrokeJoin.round
        ..color = color,
    );
  }

  /// A smooth path through [pts] via Catmull-Rom → cubic Bézier (tension 1/6).
  Path _smooth(List<Offset> pts) {
    final path = Path()..moveTo(pts[0].dx, pts[0].dy);
    for (var i = 0; i < pts.length - 1; i++) {
      final p0 = pts[i == 0 ? 0 : i - 1];
      final p1 = pts[i];
      final p2 = pts[i + 1];
      final p3 = pts[i + 2 >= pts.length ? pts.length - 1 : i + 2];
      path.cubicTo(
        p1.dx + (p2.dx - p0.dx) / 6,
        p1.dy + (p2.dy - p0.dy) / 6,
        p2.dx - (p3.dx - p1.dx) / 6,
        p2.dy - (p3.dy - p1.dy) / 6,
        p2.dx,
        p2.dy,
      );
    }
    return path;
  }

  @override
  bool shouldRepaint(_TrafficChartPainter old) =>
      old.tail != tail || old.up != up || old.down != down;
}
