/// The update system's visible half: the accent banner above the hero
/// ([UpdateBanner], hosted by StatusHeader), the "installer running" card that
/// stands in for it ([UpdatingBanner]), and the Download / progress / Install
/// / notes affordance the banner shares with the Settings "Updates" section
/// ([UpdateActions]). One state machine, read straight off the daemon's
/// `update_status` — the daemon is the truth, nothing here models progress.
library;

import 'dart:async';

import 'package:flutter/material.dart';

import '../app/formats.dart';
import '../app/session.dart';
import '../platform/open_url.dart';
import '../wire/wire.dart';
import 'app.dart';
import 'dialogs.dart';
import 'theme/app_colors.dart';
import 'theme/app_typography.dart';
import 'widgets/iron_widgets.dart';

/// Runs a session update action and reports its failure as a snackbar — the
/// header never renders the error inline (Settings does). The messenger is
/// captured BEFORE the await: the header swaps the banner out for
/// [UpdatingBanner] the moment the installer is launched, so by the time a
/// launch failure lands the calling element may already be unmounted.
Future<void> runUpdateAction(BuildContext context, DaemonSession session,
    Future<void> Function() action) async {
  final messenger = ScaffoldMessenger.maybeOf(context);
  final errorColor = Theme.of(context).colorScheme.error;
  await action();
  final err = session.lastUpdateCheckError;
  if (err != null) {
    messenger?.showSnackBar(
        SnackBar(content: Text(err), backgroundColor: errorColor));
  }
}

/// The Install flow: the consent dialog (the one place the user is told the
/// tunnel drops for ~30 s and UAC asks once), then the apply + launch. Shared
/// by the banner/Settings button and the tray entry ([UpdateInstallRequests]).
Future<void> installUpdate(
    BuildContext context, DaemonSession session, UpdateStatus status) async {
  final ok = await confirmInstallUpdate(context, status.latestVersion);
  if (!ok || !context.mounted) return;
  await runUpdateAction(context, session, session.applyUpdate);
}

/// Turns a [DaemonSession.requestInstall] (the tray's "Update to vX…" click)
/// into [installUpdate] under this widget's context. Mounted once, in the
/// shell, so the request is served whichever page is showing.
class UpdateInstallRequests extends StatefulWidget {
  const UpdateInstallRequests({
    super.key,
    required this.session,
    required this.child,
  });

  final DaemonSession session;
  final Widget child;

  @override
  State<UpdateInstallRequests> createState() => _UpdateInstallRequestsState();
}

class _UpdateInstallRequestsState extends State<UpdateInstallRequests> {
  @override
  void initState() {
    super.initState();
    widget.session.addListener(_onSession);
  }

  @override
  void didUpdateWidget(UpdateInstallRequests oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (oldWidget.session != widget.session) {
      oldWidget.session.removeListener(_onSession);
      widget.session.addListener(_onSession);
    }
  }

  @override
  void dispose() {
    widget.session.removeListener(_onSession);
    super.dispose();
  }

  void _onSession() {
    final s = widget.session;
    if (!s.takeInstallRequest()) return;
    // A stale request (nothing staged any more, or an install already in
    // flight) is dropped: the window is up and the banner explains the state.
    if (!s.installStaged || s.updateBusy || s.updating) return;
    final st = s.offeredUpdate!;
    // Off the notification's stack, so a dialog is never pushed mid-build.
    scheduleMicrotask(() {
      if (!mounted) return;
      unawaited(installUpdate(context, s, st));
    });
  }

  @override
  Widget build(BuildContext context) => widget.child;
}

/// "Update available: vX (current vY)" + [UpdateActions] + a dismiss, on an
/// accent-tinted card. The host decides WHEN it shows (offered, not
/// dismissed, daemon reachable); [status] is the offered update.
class UpdateBanner extends StatelessWidget {
  const UpdateBanner({
    super.key,
    required this.session,
    required this.status,
    this.host,
  });

  final DaemonSession session;
  final UpdateStatus status;

  /// The OS the affordance is for; defaults to the running one.
  final HostOs? host;

  @override
  Widget build(BuildContext context) {
    final t = context.iron;
    // The status poll's daemon_version already carries the `v`; the check
    // reply's current_version is the same string from the same build.
    final current = session.daemonVersion ?? status.currentVersion;
    final headline = current.isEmpty
        ? 'Update available: ${status.latestVersion}'
        : 'Update available: ${status.latestVersion} (current $current)';
    return Padding(
      padding: const EdgeInsets.only(bottom: 16),
      child: IronCard(
        tinted: t.accent,
        child: Row(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Icon(Icons.system_update_alt, color: t.accent),
            const SizedBox(width: 12),
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(
                    headline,
                    style: Theme.of(context)
                        .textTheme
                        .bodyMedium
                        ?.copyWith(color: t.text),
                  ),
                  const SizedBox(height: 10),
                  UpdateActions(session: session, status: status, host: host),
                ],
              ),
            ),
            const SizedBox(width: 8),
            IconButton(
              tooltip: 'Dismiss',
              visualDensity: VisualDensity.compact,
              padding: EdgeInsets.zero,
              constraints: const BoxConstraints(minWidth: 28, minHeight: 28),
              icon: Icon(Icons.close, size: 18, color: t.dim),
              onPressed: session.dismissUpdate,
            ),
          ],
        ),
      ),
    );
  }
}

/// Shown instead of the update AND the daemon-down banner while the launched
/// installer stops the service, swaps the files and restarts everything.
class UpdatingBanner extends StatelessWidget {
  const UpdatingBanner({super.key});

  @override
  Widget build(BuildContext context) {
    final t = context.iron;
    return Padding(
      padding: const EdgeInsets.only(bottom: 16),
      child: IronCard(
        tinted: t.accent,
        child: Row(
          children: [
            SizedBox(
              width: 18,
              height: 18,
              child: CircularProgressIndicator(strokeWidth: 2, color: t.accent),
            ),
            const SizedBox(width: 14),
            Expanded(
              child: Text(
                'Updating… the app restarts when the installer finishes.',
                style: Theme.of(context)
                    .textTheme
                    .bodyMedium
                    ?.copyWith(color: t.text),
              ),
            ),
          ],
        ),
      ),
    );
  }
}

/// The per-state affordance for an offered update. Windows with an installer
/// artifact walks the download state machine (Download → progress → Install);
/// every other platform/channel is notify-only: a Release notes link and, on
/// Linux, the pacman-side hint (the package is not self-updating by design).
class UpdateActions extends StatelessWidget {
  const UpdateActions({
    super.key,
    required this.session,
    required this.status,
    this.host,
  });

  final DaemonSession session;
  final UpdateStatus status;
  final HostOs? host;

  Future<void> _openNotes(BuildContext context, HostOs os) async {
    final ok = await openUrl(status.notesUrl, host: os);
    if (!ok && context.mounted) {
      showSnack(context, 'Could not open ${status.notesUrl}', error: true);
    }
  }

  @override
  Widget build(BuildContext context) {
    final t = context.iron;
    final os = host ?? HostOs.current;
    // Also held while a launched installer runs (Settings keeps rendering this
    // widget then, the header does not): one setup at a time.
    final busy = session.updateBusy || session.updating;
    final art = status.artifact;
    final installer =
        os == HostOs.windows && art != null && art.kind == 'installer';

    final notes = status.notesUrl.isEmpty
        ? null
        : IronButton(
            label: 'Release notes',
            icon: Icons.open_in_new,
            onPressed: () => _openNotes(context, os),
          );

    if (!installer) {
      return Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          if (os == HostOs.linux)
            Padding(
              padding: EdgeInsets.only(bottom: notes == null ? 0 : 8),
              child: Text(
                'Update with makepkg -si from the repository',
                style: Theme.of(context)
                    .textTheme
                    .bodySmall
                    ?.copyWith(color: t.dim),
              ),
            ),
          ?notes,
        ],
      );
    }

    switch (status.downloadState) {
      case 'downloading':
        final received = status.downloadReceived;
        final total = status.downloadTotal;
        final figure = total > 0
            ? '${formatBytes(received)} / ${formatBytes(total)}'
            : formatBytes(received);
        return Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            ClipRRect(
              borderRadius: BorderRadius.circular(999),
              child: LinearProgressIndicator(
                value: total > 0 ? (received / total).clamp(0.0, 1.0) : null,
                minHeight: 6,
                color: t.accent,
                backgroundColor: t.border,
              ),
            ),
            const SizedBox(height: 6),
            Text('Downloading… $figure', style: context.mono(12, color: t.dim)),
          ],
        );
      case 'downloaded' || 'verified':
        return Wrap(
          spacing: 8,
          runSpacing: 8,
          crossAxisAlignment: WrapCrossAlignment.center,
          children: [
            IronButton(
              label: 'Install',
              icon: Icons.install_desktop,
              accent: true,
              onPressed:
                  busy ? null : () => installUpdate(context, session, status),
            ),
            ?notes,
          ],
        );
      default: // "" (none) / "failed"
        return Wrap(
          spacing: 8,
          runSpacing: 8,
          crossAxisAlignment: WrapCrossAlignment.center,
          children: [
            IronButton(
              label: 'Download',
              icon: Icons.download,
              accent: true,
              onPressed: busy
                  ? null
                  : () => runUpdateAction(
                      context, session, session.startUpdateDownload),
            ),
            if (status.downloadState == 'failed')
              Text('Last download failed',
                  style: Theme.of(context)
                      .textTheme
                      .bodySmall
                      ?.copyWith(color: t.danger)),
            ?notes,
          ],
        );
    }
  }
}
