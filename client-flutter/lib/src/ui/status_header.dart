import 'package:flutter/material.dart';

import '../app/formats.dart';
import '../app/session.dart';
import '../ipc/client.dart';
import '../platform/daemon_service.dart';
import '../platform/open_url.dart';
import '../wire/wire.dart';
import 'app.dart';
import 'iron_scope.dart';
import 'theme/app_colors.dart';
import 'theme/app_typography.dart';
import 'update_banner.dart';
import 'widgets/iron_widgets.dart';
import 'widgets/motion.dart';

/// The session's honest uptime: the TUN core's (the tunnel itself), or the
/// longest-running core if there is no TUN (proxy-only mode). The cores start
/// together, so this is effectively the session uptime — shown as one line
/// rather than a per-core chip each.
int _sessionUptime(DaemonSession s) {
  var best = 0;
  for (final e in s.entries) {
    if (e.role == CoreRole.tun) return e.uptimeSecs;
    if (e.uptimeSecs > best) best = e.uptimeSecs;
  }
  return best;
}

/// Pulls a leading flag emoji (a regional-indicator pair) off a node name so the
/// pipeline pill can lead with it, like the share-link names do. Returns null
/// when the name doesn't start with one — the caller falls back to a glyph.
String? _leadingFlag(String name) {
  final runes = name.runes.toList();
  if (runes.length < 2) return null;
  bool isRi(int r) => r >= 0x1F1E6 && r <= 0x1F1FF;
  if (isRi(runes[0]) && isRi(runes[1])) {
    return String.fromCharCodes(runes.take(2));
  }
  return null;
}

/// The session hero: the signature pipeline (You → power orb + status word →
/// the active node), then the mode/uptime/rate info row. Everything shown is
/// read back from the daemon. Hosted at the top of HomePage's scroll view —
/// scrolling and padding belong to the host, so the root is a plain Column.
///
/// Tapping the pipeline's server pill calls [onServerTap] (HomePage scrolls to
/// its node list so the user can switch); the daemon-reachable banner renders
/// above the hero when the subscribe connection is down.
class StatusHeader extends StatefulWidget {
  const StatusHeader({
    super.key,
    required this.client,
    required this.session,
    this.onServerTap,
    this.host,
    this.probe = probeDaemonService,
    this.start = startDaemonService,
  });

  final DaemonClient client;
  final DaemonSession session;

  /// Reveal/scroll to the node list — wired from HomePage's server selector.
  final VoidCallback? onServerTap;

  /// The daemon-down banner's host (its Start label), service probe and
  /// elevated Start; injected in tests (the real ones run systemctl /
  /// launchctl / sc.exe).
  final HostOs? host;
  final Future<ServiceReport> Function({Object? cause}) probe;
  final Future<StartResult> Function() start;

  @override
  State<StatusHeader> createState() => _StatusHeaderState();
}

class _StatusHeaderState extends State<StatusHeader> {
  bool _tun = true;
  bool _busy = false;

  Future<void> _activate() async {
    setState(() => _busy = true);
    await guard(context, () => widget.client.activate(tun: _tun));
    await widget.session.refreshStatus();
    if (mounted) setState(() => _busy = false);
  }

  Future<void> _stop() async {
    setState(() => _busy = true);
    await guard(context, widget.client.stop);
    await widget.session.refreshStatus();
    if (mounted) setState(() => _busy = false);
  }

  @override
  Widget build(BuildContext context) {
    // The ListenableBuilder is scoped to the header on purpose: traffic
    // ticks arrive every second and must repaint only the hero, never the
    // host page's node list.
    return ListenableBuilder(
      listenable: widget.session,
      builder: (context, _) {
        final s = widget.session;
        final running = s.isRunning;
        final disabled = _busy || (!running && !s.connected);
        // The offered update, unless dismissed for this version; only while
        // the daemon is reachable (its buttons are daemon calls, and the
        // daemon-down banner already owns that slot).
        final offered = s.offeredUpdate;
        final showUpdate = s.connected &&
            offered != null &&
            s.dismissedUpdateVersion != offered.latestVersion;
        return Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            // While the launched installer restarts the daemon, its brief
            // absence is expected — the "Updating…" card stands in for the
            // daemon-down banner rather than flashing it red.
            if (s.updating)
              const UpdatingBanner()
            else ...[
              if (!s.connected)
                _DaemonDownBanner(
                  session: s,
                  client: widget.client,
                  host: widget.host ?? HostOs.current,
                  probe: widget.probe,
                  start: widget.start,
                ),
              if (showUpdate) UpdateBanner(session: s, status: offered),
            ],
            if (running && s.routingWarnings.isNotEmpty)
              _RoutingWarningsBanner(warnings: s.routingWarnings),
            // 1) You — thread — power orb — thread — the active node.
            _Pipeline(
              session: s,
              running: running,
              orb: _PowerOrb(
                running: running,
                busy: _busy,
                disabled: disabled,
                onTap: disabled ? null : (running ? _stop : _activate),
              ),
              onServerTap: widget.onServerTap,
            ),
            const SizedBox(height: 18),
            // 2) Mode chip + uptime on the left, up/down rate on the right.
            _InfoRow(session: s, tun: _tun, running: running),
            // The mode toggle only matters before powering on; once running the
            // active context owns the mode and the toggle would be a no-op.
            if (!running) ...[
              const SizedBox(height: 14),
              _ModePicker(
                tun: _tun,
                busy: _busy,
                connected: s.connected,
                onChanged: (v) => setState(() => _tun = v),
              ),
            ],
          ],
        );
      },
    );
  }
}

/// The session's primary control: one large circular power toggle. Running →
/// accent ring + outer glow + a breathing accent disc behind a looping
/// [PulseRing]; idle → a calm `border`-ringed disc with a `dim` icon. Activation
/// is gated on the daemon being reachable; Stop is always available while
/// running. Busy shows a spinner in place of the icon. The status word sits
/// below.
class _PowerOrb extends StatelessWidget {
  const _PowerOrb({
    required this.running,
    required this.busy,
    required this.disabled,
    required this.onTap,
  });

  final bool running;
  final bool busy;
  final bool disabled;
  final VoidCallback? onTap;

  @override
  Widget build(BuildContext context) {
    final t = context.iron;
    final animate = running && !IronScope.reduceMotionOf(context);
    final ring = running ? t.accent : t.border;
    final iconColor = busy
        ? context.appColors.statusConnecting
        : running
        ? t.accent
        : t.dim;

    final disc = Container(
      width: 112,
      height: 112,
      decoration: BoxDecoration(
        color: t.raised,
        shape: BoxShape.circle,
        border: Border.all(color: ring, width: 2),
        boxShadow: running
            ? [
                BoxShadow(
                  color: t.accent.withValues(alpha: 0.28),
                  blurRadius: 28,
                  spreadRadius: 2,
                ),
              ]
            : null,
      ),
      alignment: Alignment.center,
      child: busy
          ? SizedBox(
              width: 34,
              height: 34,
              child: CircularProgressIndicator(
                strokeWidth: 3,
                color: iconColor,
              ),
            )
          : Icon(Icons.power_settings_new, size: 46, color: iconColor),
    );

    return Column(
      mainAxisSize: MainAxisSize.min,
      children: [
        MouseRegion(
          cursor: disabled
              ? SystemMouseCursors.basic
              : SystemMouseCursors.click,
          child: GestureDetector(
            onTap: onTap,
            child: SizedBox(
              width: _orbBox,
              height: _orbBox,
              child: PulseRing(
                animate: animate,
                color: t.accent,
                child: BreathingScale(animate: animate, child: disc),
              ),
            ),
          ),
        ),
        const SizedBox(height: 16),
        // Capped (shrinking if ever wider), so the word can never squeeze the
        // pipeline row past the minimum window width.
        ConstrainedBox(
          constraints: const BoxConstraints(maxWidth: 200),
          child: FittedBox(
            fit: BoxFit.scaleDown,
            child: Text(
              running ? 'Connected' : 'Disconnected',
              style: context.displayWord
                  ?.copyWith(color: running ? t.text : t.dim),
            ),
          ),
        ),
      ],
    );
  }
}

/// The info row under the orb: a "TUN · Proxy" mode chip + uptime on the left,
/// the up/down live rate on the right. Accent-tinted while running, neutral
/// when idle (uptime "—", rates 0.0 dim).
class _InfoRow extends StatelessWidget {
  const _InfoRow({
    required this.session,
    required this.tun,
    required this.running,
  });

  final DaemonSession session;
  final bool tun;
  final bool running;

  @override
  Widget build(BuildContext context) {
    final t = context.iron;
    // While running, the daemon's active context owns the real mode; idle, we
    // preview the toggle's current choice.
    final modeTun = running ? (session.active?.tun ?? false) : tun;
    final modeLabel = modeTun ? 'TUN · system' : 'Proxy only';
    final modeColor = running ? t.accent : t.dim;
    final uptime = running ? formatUptime(_sessionUptime(session)) : '—';

    return Row(
      children: [
        IronTag(modeLabel, color: modeColor, icon: Icons.shield_outlined),
        const SizedBox(width: 12),
        Icon(Icons.schedule, size: 15, color: t.faint),
        const SizedBox(width: 5),
        Text(uptime, style: context.mono(13, color: running ? t.dim : t.faint)),
        const Spacer(),
        _RateCell(
          icon: Icons.arrow_upward,
          bytesPerSec: running ? session.rate.up : 0,
          running: running,
        ),
        const SizedBox(width: 16),
        _RateCell(
          icon: Icons.arrow_downward,
          bytesPerSec: running ? session.rate.down : 0,
          running: running,
        ),
      ],
    );
  }
}

/// One ↑/↓ live-rate cell: arrow + mono figure. Dim when idle.
class _RateCell extends StatelessWidget {
  const _RateCell({
    required this.icon,
    required this.bytesPerSec,
    required this.running,
  });

  final IconData icon;
  final int bytesPerSec;
  final bool running;

  @override
  Widget build(BuildContext context) {
    final t = context.iron;
    final color = running ? t.text : t.faint;
    return Row(
      mainAxisSize: MainAxisSize.min,
      children: [
        Icon(icon, size: 15, color: running ? t.accent : t.faint),
        const SizedBox(width: 4),
        Text(formatRate(bytesPerSec), style: context.mono(13, color: color)),
      ],
    );
  }
}

/// The pre-flight mode picker — replaces the old TUN switch row. A small
/// segmented control (system-wide TUN vs proxy-only), disabled while busy.
class _ModePicker extends StatelessWidget {
  const _ModePicker({
    required this.tun,
    required this.busy,
    required this.connected,
    required this.onChanged,
  });

  final bool tun;
  final bool busy;
  final bool connected;
  final ValueChanged<bool> onChanged;

  @override
  Widget build(BuildContext context) {
    final t = context.iron;
    return Row(
      children: [
        IronSegmented<bool>(
          segments: const [(true, 'System (TUN)'), (false, 'Proxy only')],
          selected: tun,
          onChanged: busy ? (_) {} : onChanged,
        ),
        const SizedBox(width: 12),
        Expanded(
          child: Text(
            connected
                ? 'Pick a node below, choose a mode, then power on.'
                : 'Start the daemon to connect.',
            style: Theme.of(
              context,
            ).textTheme.bodySmall?.copyWith(color: t.faint),
          ),
        ),
      ],
    );
  }
}

/// The orb's square footprint (disc + pulse room); the pipeline's side items
/// centre on it, so the threads meet the disc rather than the orb + word.
const double _orbBox = 150;

/// The signature pipeline: You → thread → [orb] → thread → server pill. The
/// threads flow only while running (and reduce-motion off); idle, everything
/// desaturates to neutral tokens. Tapping the server pill calls
/// [onServerTap].
class _Pipeline extends StatelessWidget {
  const _Pipeline({
    required this.session,
    required this.running,
    required this.orb,
    required this.onServerTap,
  });

  final DaemonSession session;
  final bool running;
  final Widget orb;
  final VoidCallback? onServerTap;

  @override
  Widget build(BuildContext context) {
    final t = context.iron;
    final animate = running && !IronScope.reduceMotionOf(context);
    Widget level(Widget child) =>
        SizedBox(height: _orbBox, child: Center(child: child));
    Widget thread() => Expanded(
          child: level(Padding(
            padding: const EdgeInsets.symmetric(horizontal: 6),
            child: PipelineThread(
              animate: animate,
              accent: running ? t.accent : t.faint,
              idle: t.faint,
              height: 16,
            ),
          )),
        );
    return Row(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        level(IronEndNode(icon: Icons.laptop_mac, label: 'You', active: running)),
        thread(),
        orb,
        thread(),
        level(_ServerPill(
          session: session,
          running: running,
          onTap: onServerTap,
        )),
      ],
    );
  }
}

/// The end of the pipeline: the active node, as a tappable pill. Accent-
/// tinted while running, neutral `raised` when idle. Shows a flag (when the
/// node name carries one), the node name, and a protocol/security caption.
/// Tapping it scrolls to the node list below (no chevron — the list is always
/// in view).
class _ServerPill extends StatelessWidget {
  const _ServerPill({
    required this.session,
    required this.running,
    required this.onTap,
  });

  final DaemonSession session;
  final bool running;
  final VoidCallback? onTap;

  @override
  Widget build(BuildContext context) {
    final t = context.iron;
    final active = session.active;
    final name = session.activeNodeLive ?? active?.node ?? '(no node)';
    final flag = _leadingFlag(name);
    // Strip a leading flag from the visible name (it's shown separately).
    final shown = flag == null ? name : name.substring(flag.length).trim();
    final caption = active?.routing != null && active!.routing!.isNotEmpty
        ? active.routing!
        : (running ? 'active' : 'idle');

    final fill = running ? t.accentSoft : t.raised;
    final line = running ? t.accent.withValues(alpha: 0.45) : t.border;
    final nameColor = running ? t.accentStrong : t.text;

    return ConstrainedBox(
      constraints: const BoxConstraints(maxWidth: 180),
      child: Material(
        type: MaterialType.transparency,
        child: InkWell(
          onTap: onTap,
          borderRadius: BorderRadius.circular(14),
          child: Container(
            padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 10),
            decoration: BoxDecoration(
              color: fill,
              borderRadius: BorderRadius.circular(14),
              border: Border.all(color: line),
            ),
            child: Column(
              mainAxisSize: MainAxisSize.min,
              children: [
                Row(
                  mainAxisSize: MainAxisSize.min,
                  children: [
                    if (flag != null) ...[
                      Text(flag, style: const TextStyle(fontSize: 16)),
                      const SizedBox(width: 6),
                    ] else ...[
                      Icon(Icons.dns_outlined, size: 15, color: nameColor),
                      const SizedBox(width: 6),
                    ],
                    Flexible(
                      child: Text(
                        shown.isEmpty ? name : shown,
                        overflow: TextOverflow.ellipsis,
                        style: TextStyle(
                          fontSize: 13.5,
                          fontWeight: FontWeight.w600,
                          color: nameColor,
                        ),
                      ),
                    ),
                  ],
                ),
                const SizedBox(height: 3),
                Text(
                  caption,
                  overflow: TextOverflow.ellipsis,
                  style: context.monoLabelSmall?.copyWith(color: t.faint),
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }
}

/// The daemon-unreachable banner — a danger-tinted card shown above the hero
/// while the subscribe connection is down. Under the headline it says what
/// the OS service manager knows about the daemon service and, when the fix
/// is to start it, offers one elevated Start. The probe runs once, as soon
/// as the session has recorded why its first subscribe attempt ended (that
/// lands a moment after the banner mounts; the Linux running-but-unreachable
/// texts turn on the errno in it), and again after a Start — never on the
/// 1 s reconnect cadence; the reconnect loop hides the banner as soon as the
/// daemon answers.
class _DaemonDownBanner extends StatefulWidget {
  const _DaemonDownBanner({
    required this.session,
    required this.client,
    required this.host,
    required this.probe,
    required this.start,
  });

  final DaemonSession session;
  final DaemonClient client;
  final HostOs host;
  final Future<ServiceReport> Function({Object? cause}) probe;
  final Future<StartResult> Function() start;

  @override
  State<_DaemonDownBanner> createState() => _DaemonDownBannerState();
}

class _DaemonDownBannerState extends State<_DaemonDownBanner> {
  ServiceReport? _report;
  bool _busy = false;
  bool _probed = false;

  @override
  void initState() {
    super.initState();
    _probeOnce();
  }

  @override
  void didUpdateWidget(_DaemonDownBanner oldWidget) {
    super.didUpdateWidget(oldWidget);
    _probeOnce();
  }

  /// Waits for the session to record why the connect failed (the probe tells
  /// a stopped service from a running-but-unreachable one by it), then
  /// probes exactly once.
  void _probeOnce() {
    if (_probed || widget.session.unreachableCause == null) return;
    _probed = true;
    _probe();
  }

  Future<void> _probe() async {
    final report = await widget.probe(cause: widget.session.unreachableCause);
    if (mounted) setState(() => _report = report);
  }

  /// The banner goes away the moment the daemon answers, so a late outcome
  /// may land on an unmounted element; by then it is moot.
  Future<void> _start() async {
    setState(() => _busy = true);
    final (outcome, message) = await widget.start();
    switch (outcome) {
      case StartOutcome.launched:
        // Give the service a moment to bind before re-reading its state.
        await Future<void>.delayed(const Duration(seconds: 2));
        await _probe();
      case StartOutcome.cancelled:
        break;
      case StartOutcome.failed:
        if (mounted) showSnack(context, message, error: true);
    }
    if (mounted) setState(() => _busy = false);
  }

  @override
  Widget build(BuildContext context) {
    final t = context.iron;
    final body = Theme.of(context).textTheme.bodyMedium?.copyWith(color: t.text);
    final report = _report;
    return Padding(
      padding: const EdgeInsets.only(bottom: 16),
      child: IronCard(
        tinted: t.danger,
        child: Row(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Icon(Icons.warning_amber, color: t.danger),
            const SizedBox(width: 12),
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text('Daemon unreachable at ${widget.client.endpoint}.',
                      style: body?.copyWith(fontWeight: FontWeight.w600)),
                  if (report != null) ...[
                    const SizedBox(height: 4),
                    Text(report.detail, style: body),
                    if (report.canStart) ...[
                      const SizedBox(height: 10),
                      IronButton(
                        label: widget.host == HostOs.macos
                            ? 'Start daemon'
                            : 'Start service',
                        icon: Icons.play_arrow,
                        accent: true,
                        onPressed: _busy ? null : _start,
                      ),
                    ],
                  ],
                ],
              ),
            ),
          ],
        ),
      ),
    );
  }
}

/// The running session's routing fallbacks: rules whose node or group is gone
/// or undialable now go through the active node. Shown until the next
/// activation resolves them (the daemon re-sends the list on every state).
class _RoutingWarningsBanner extends StatelessWidget {
  const _RoutingWarningsBanner({required this.warnings});

  final List<String> warnings;

  @override
  Widget build(BuildContext context) {
    final t = context.iron;
    final warning = context.appColors.warning;
    final body = Theme.of(context).textTheme.bodyMedium?.copyWith(color: t.text);
    return Padding(
      padding: const EdgeInsets.only(bottom: 16),
      child: IronCard(
        tinted: warning,
        child: Row(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Icon(Icons.warning_amber, color: warning),
            const SizedBox(width: 12),
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text('Some routing targets are unavailable',
                      style: body?.copyWith(fontWeight: FontWeight.w600)),
                  for (final w in warnings) ...[
                    const SizedBox(height: 4),
                    Text(w, style: body),
                  ],
                ],
              ),
            ),
          ],
        ),
      ),
    );
  }
}
