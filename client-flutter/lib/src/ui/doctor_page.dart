import 'dart:io' show Platform;

import 'package:flutter/material.dart';

import '../ipc/client.dart';
import '../wire/wire.dart';
import 'app.dart';
import 'theme/app_colors.dart';
import 'theme/app_typography.dart';
import 'widgets/iron_widgets.dart';

/// The "Doctor" tab: a battery of read-only connection checks, one row each.
/// Running the doctor probes the network (a few seconds) and lists every
/// check's verdict; a check that found a fixable setting offers an Apply
/// action. Nothing changes until the user accepts a remedy. The first check is
/// connectivity + address family (IPv4 / IPv6 reachability, the OS resolver,
/// and whether the DNS strategy matches what's actually reachable).
class DoctorPage extends StatefulWidget {
  const DoctorPage({super.key, required this.client});

  final DaemonClient client;

  @override
  State<DoctorPage> createState() => _DoctorPageState();
}

class _DoctorPageState extends State<DoctorPage> {
  List<DoctorCheck>? _checks; // null until the first run completes
  List<DoctorCheck>? _nodeChecks; // null until "probe all nodes" is pressed
  List<DoctorCheck>? _netReport; // null until "network report" is pressed
  List<DoctorCheck>? _dockerChecks; // null until the docker "check" is pressed
  bool _running = false;
  bool _runningNodes = false;
  bool _runningNet = false;
  bool _runningDocker = false;
  String? _error;

  @override
  void initState() {
    super.initState();
    _run();
  }

  Future<void> _run() async {
    setState(() {
      _running = true;
      _error = null;
    });
    try {
      final checks = await widget.client.doctor();
      if (!mounted) return;
      setState(() => _checks = checks);
    } on ClientException catch (e) {
      if (!mounted) return;
      setState(() => _error = e.message);
    } finally {
      if (mounted) setState(() => _running = false);
    }
  }

  /// Probes every node's camouflage (the heavier sweep behind its own button).
  Future<void> _runAllNodes() async {
    setState(() => _runningNodes = true);
    final checks = await guard(context, () => widget.client.doctorNodes());
    if (!mounted) return;
    setState(() {
      if (checks != null) _nodeChecks = checks;
      _runningNodes = false;
    });
  }

  /// Runs the heavier "Network report" — provider reachability, DNS integrity,
  /// transports. Read-only; characterizes the SHAPE of blocking on this network.
  Future<void> _runNetworkReport() async {
    setState(() => _runningNet = true);
    final checks = await guard(context, () => widget.client.networkReport());
    if (!mounted) return;
    setState(() {
      if (checks != null) _netReport = checks;
      _runningNet = false;
    });
  }

  /// Runs the Linux-only docker / LAN-forwarding ↔ host-firewall check (its own
  /// section). A local read on the daemon; returns one row, or empty off Linux.
  Future<void> _runDocker() async {
    setState(() => _runningDocker = true);
    final checks = await guard(context, () => widget.client.forwardingCheck());
    if (!mounted) return;
    setState(() {
      if (checks != null) _dockerChecks = checks;
      _runningDocker = false;
    });
  }

  /// Applies a check's remedy by editing the effective settings document in
  /// place (whichever DNS levers the remedy carries) and sending it back, then
  /// re-runs so the row reflects the fix.
  Future<void> _apply(DoctorRemedy remedy) async {
    final ok = await guardOk(context, () async {
      final settings = await widget.client.getSettings();
      if (remedy.dnsStrategy != null) settings.dns.strategy = remedy.dnsStrategy!;
      if (remedy.dnsServers != null) settings.dns.servers = remedy.dnsServers!;
      if (remedy.dnsViaTunnel != null) {
        settings.dns.viaTunnel = remedy.dnsViaTunnel!;
      }
      final reply = await widget.client.setSettings(settings);
      if (reply.needsReactivation && mounted) {
        showSnack(context,
            'Saved — reconnect to apply it to the running session.');
      }
    });
    if (ok && mounted) await _run();
  }

  @override
  Widget build(BuildContext context) {
    final t = context.iron;
    return Scaffold(
      backgroundColor: t.panel,
      body: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          // Screen-title row: title + one-line subtitle + the General refresh.
          Padding(
            padding: const EdgeInsets.fromLTRB(24, 16, 24, 8),
            child: Row(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Expanded(
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text('Doctor', style: context.screenTitle),
                      Padding(
                        padding: const EdgeInsets.only(top: 2),
                        child: Text('Connection health checks.',
                            style: Theme.of(context)
                                .textTheme
                                .bodyMedium
                                ?.copyWith(color: t.dim)),
                      ),
                    ],
                  ),
                ),
                const SizedBox(width: 12),
                IronIconButton(
                  icon: Icons.refresh,
                  tooltip: 'Run the checks again',
                  onPressed: _running ? null : _run,
                ),
              ],
            ),
          ),
          if (_running)
            LinearProgressIndicator(
                minHeight: 2,
                backgroundColor: t.border,
                color: t.accent),
          Expanded(child: _body()),
        ],
      ),
    );
  }

  Widget _body() {
    if (_error != null) {
      return _Centered(
        icon: Icons.error_outline,
        color: context.iron.danger,
        text: _error!,
      );
    }
    final checks = _checks;
    if (checks == null) {
      return const Center(child: CircularProgressIndicator());
    }

    final children = <Widget>[
      // ---- General: the auto-run battery + the "probe all nodes" action ----
      _sectionHeader('General',
          action: _actionButton(
            label: 'Run checks',
            icon: Icons.refresh,
            running: _running,
            onPressed: _running ? null : _run,
          )),
      _summary(checks),
      for (final c in checks) _CheckCard(check: c, onApply: _apply),
      Align(
        alignment: Alignment.centerLeft,
        child: _actionButton(
          label: 'Probe all nodes',
          icon: Icons.lan_outlined,
          running: _runningNodes,
          onPressed: _runningNodes ? null : _runAllNodes,
        ),
      ),
    ];
    final nodeChecks = _nodeChecks;
    if (nodeChecks != null) {
      if (nodeChecks.isEmpty) {
        children.add(_hint('No TLS-bearing nodes to probe.'));
      } else {
        for (final c in nodeChecks) {
          children.add(_CheckCard(check: c, onApply: _apply));
        }
      }
    }

    // ---- Network report: provider reachability / DNS integrity / transports.
    children.add(_sectionHeader('Network report',
        action: _actionButton(
          label: 'Run',
          icon: Icons.travel_explore,
          running: _runningNet,
          onPressed: _runningNet ? null : _runNetworkReport,
        )));
    final netReport = _netReport;
    if (netReport == null) {
      children.add(_hint('Characterizes how this network blocks — provider '
          'reachability, DNS integrity, transports.'));
    } else {
      for (final c in netReport) {
        children.add(_CheckCard(check: c, onApply: _apply));
      }
    }

    // ---- Docker / LAN forwarding (Linux only — auto_redirect is Linux-only).
    if (Platform.isLinux) {
      children.add(_sectionHeader('Docker / LAN forwarding',
          action: _actionButton(
            label: 'Check',
            icon: Icons.dns_outlined,
            running: _runningDocker,
            onPressed: _runningDocker ? null : _runDocker,
          )));
      final dockerChecks = _dockerChecks;
      if (dockerChecks == null) {
        children.add(_hint('Checks whether the host firewall drops docker / '
            'LAN builds while the VPN is up.'));
      } else if (dockerChecks.isEmpty) {
        children.add(_hint('Not applicable on this system.'));
      } else {
        for (final c in dockerChecks) {
          children.add(_CheckCard(check: c, onApply: _apply));
        }
      }
    }

    return Center(
      child: ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: 760),
        child: ListView.separated(
          padding: const EdgeInsets.fromLTRB(24, 8, 24, 88),
          itemCount: children.length,
          separatorBuilder: (_, _) => const SizedBox(height: 10),
          itemBuilder: (context, i) => children[i],
        ),
      ),
    );
  }

  /// A section divider: the uppercase faint title with an optional trailing
  /// action (the section's run/refresh button).
  Widget _sectionHeader(String title, {Widget? action}) {
    final t = context.iron;
    return Padding(
      padding: const EdgeInsets.fromLTRB(4, 16, 4, 0),
      child: Row(
        children: [
          Text(
            title.toUpperCase(),
            style: TextStyle(
              color: t.faint,
              fontSize: 11,
              fontWeight: FontWeight.w700,
              letterSpacing: 0.8,
            ),
          ),
          const Spacer(),
          ?action,
        ],
      ),
    );
  }

  /// A section's run button: an icon (or a spinner while running) + label,
  /// styled as the accent-outline [IronButton].
  Widget _actionButton({
    required String label,
    required IconData icon,
    required bool running,
    required VoidCallback? onPressed,
  }) {
    // While a section is running we swap the leading icon for a spinner; the
    // IronButton itself carries the outline + label.
    if (!running) {
      return IronButton(
        label: label,
        icon: icon,
        accent: true,
        onPressed: onPressed,
      );
    }
    final t = context.iron;
    return Opacity(
      opacity: 0.6,
      child: Container(
        padding: const EdgeInsets.symmetric(horizontal: 13, vertical: 9),
        decoration: BoxDecoration(
          borderRadius: BorderRadius.circular(11),
          border: Border.all(color: t.accent.withValues(alpha: 0.5)),
        ),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            SizedBox(
              width: 16,
              height: 16,
              child: CircularProgressIndicator(strokeWidth: 2, color: t.accent),
            ),
            const SizedBox(width: 7),
            Text(label,
                style: TextStyle(
                    fontSize: 13,
                    fontWeight: FontWeight.w600,
                    color: t.accentStrong)),
          ],
        ),
      ),
    );
  }

  /// A muted one-line note shown before a section's button has been pressed.
  Widget _hint(String text) {
    final t = context.iron;
    return Padding(
      padding: const EdgeInsets.only(left: 4, top: 2, bottom: 2),
      child: Text(text,
          style: Theme.of(context)
              .textTheme
              .bodySmall
              ?.copyWith(color: t.dim)),
    );
  }

  /// The at-a-glance verdict above the per-check cards: green when everything
  /// passed, otherwise the count of failures (which dominate) and/or warnings.
  Widget _summary(List<DoctorCheck> checks) {
    final fails = checks.where((c) => c.status == 'fail').length;
    final warns = checks.where((c) => c.status == 'warn').length;
    final app = context.appColors;
    final t = context.iron;
    final (IconData icon, Color color, String text) = fails > 0
        ? (
            Icons.error,
            t.danger,
            '$fails ${fails == 1 ? 'issue needs' : 'issues need'} attention'
                '${warns > 0 ? ' · $warns warning${warns == 1 ? '' : 's'}' : ''}'
          )
        : warns > 0
            ? (
                Icons.warning_amber_rounded,
                app.warning,
                '$warns warning${warns == 1 ? '' : 's'}'
              )
            : (Icons.check_circle, app.statusRunning, 'All checks passed');
    return IronCard(
      tinted: color,
      child: Row(
        children: [
          IronIconTile(icon, color: color),
          const SizedBox(width: 12),
          Expanded(
            child: Text(text,
                style: Theme.of(context).textTheme.titleMedium?.copyWith(
                    color: color, fontWeight: FontWeight.w600)),
          ),
        ],
      ),
    );
  }
}

/// One check rendered as a card: a status icon tile + title + verdict, the
/// supporting detail lines, and (for a warn/fail with a remedy) an Apply row.
class _CheckCard extends StatelessWidget {
  const _CheckCard({required this.check, required this.onApply});

  final DoctorCheck check;
  final Future<void> Function(DoctorRemedy) onApply;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final t = context.iron;
    final (icon, color, tagLabel) = _statusVisual(context, check.status);
    return IronCard(
      // Tint problem cards so they stand out; passing checks stay neutral.
      tinted: check.status == 'ok' ? null : color,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              IronIconTile(icon, color: color),
              const SizedBox(width: 12),
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(check.title, style: theme.textTheme.titleMedium),
                    if (check.summary.isNotEmpty)
                      Padding(
                        padding: const EdgeInsets.only(top: 2),
                        child: Text(check.summary,
                            style: theme.textTheme.bodyMedium
                                ?.copyWith(color: t.dim)),
                      ),
                  ],
                ),
              ),
              const SizedBox(width: 12),
              IronTag(tagLabel, color: color, uppercase: true),
            ],
          ),
          if (check.details.isNotEmpty)
            Padding(
              padding: const EdgeInsets.only(top: 12, left: 50),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  for (final d in check.details)
                    Padding(
                      padding: const EdgeInsets.only(bottom: 3),
                      // Detail lines read as values — monospace, dim.
                      child: Text(d,
                          style: context.monoBodySmall?.copyWith(color: t.dim)),
                    ),
                ],
              ),
            ),
          if (check.remedy != null)
            Padding(
              padding: const EdgeInsets.only(top: 12, left: 50),
              child: Row(
                children: [
                  Expanded(
                    child: Text(check.remedy!.summary,
                        style: theme.textTheme.bodyMedium),
                  ),
                  const SizedBox(width: 12),
                  IronButton(
                    label: 'Apply',
                    accent: true,
                    onPressed: () => onApply(check.remedy!),
                  ),
                ],
              ),
            ),
        ],
      ),
    );
  }

  /// The status icon + colour + tag label: ok = green check / "OK",
  /// warn = amber triangle / "NOTICE", fail = error / "FAIL".
  (IconData, Color, String) _statusVisual(BuildContext context, String status) {
    final t = context.iron;
    final app = context.appColors;
    return switch (status) {
      'ok' => (Icons.check_circle, app.statusRunning, 'OK'),
      'warn' => (Icons.warning_amber_rounded, app.warning, 'NOTICE'),
      'fail' => (Icons.error, t.danger, 'FAIL'),
      _ => (Icons.help_outline, t.dim, '—'),
    };
  }
}

/// A centered icon + message for the empty / error / loading-failed states.
class _Centered extends StatelessWidget {
  const _Centered({required this.icon, required this.text, this.color});

  final IconData icon;
  final String text;
  final Color? color;

  @override
  Widget build(BuildContext context) {
    final c = color ?? context.iron.dim;
    return Center(
      child: Padding(
        padding: const EdgeInsets.all(32),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            Icon(icon, size: 48, color: c),
            const SizedBox(height: 12),
            Text(text, textAlign: TextAlign.center, style: TextStyle(color: c)),
          ],
        ),
      ),
    );
  }
}
