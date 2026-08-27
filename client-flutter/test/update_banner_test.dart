/// Widget tests for the update banner's state machine, driven by a passive
/// `DaemonSession` (no start(): no sockets, no timers) over a fake client
/// whose update verbs return canned replies. The host OS and the installer
/// launch are injected, so a Linux test box walks the Windows path too.
library;

import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:iron_link_flutter/src/app/session.dart';
import 'package:iron_link_flutter/src/ipc/client.dart';
import 'package:iron_link_flutter/src/platform/open_url.dart';
import 'package:iron_link_flutter/src/platform/update_launcher.dart';
import 'package:iron_link_flutter/src/ui/status_header.dart';
import 'package:iron_link_flutter/src/ui/update_banner.dart';
import 'package:iron_link_flutter/src/ui/widgets/iron_widgets.dart';
import 'package:iron_link_flutter/src/wire/wire.dart';

const _installer = UpdateArtifact(
    os: 'windows',
    arch: 'amd64',
    kind: 'installer',
    name: 'iron-link-1.2.4-windows-amd64-setup.exe',
    size: 12345678);

const _setupPath = r'C:\ProgramData\iron-link\updates\setup.exe';

UpdateStatus _offer({
  String state = '',
  int received = 0,
  int total = 0,
  UpdateArtifact? artifact = _installer,
  bool stale = false,
  String version = 'v1.2.4',
}) =>
    UpdateStatus(
      checkedAt: '2026-08-27T12:00:00Z',
      currentVersion: 'v1.2.3',
      available: true,
      stale: stale,
      latestVersion: version,
      notesUrl: 'https://example.com/iron-link/notes/$version',
      artifact: artifact,
      downloadState: state,
      downloadReceived: received,
      downloadTotal: total,
      transport: 'tunnel',
    );

class _FakeClient extends DaemonClient {
  _FakeClient() : super(endpoint: '/dev/null');

  int downloads = 0;
  int applies = 0;

  @override
  Future<UpdateStatus> downloadUpdate() async {
    downloads++;
    return _offer(state: 'downloading', received: 1 << 20, total: 4 << 20);
  }

  @override
  Future<UpdateStatus> applyUpdate() async {
    applies++;
    return const UpdateStatus(
        available: true,
        latestVersion: 'v1.2.4',
        artifact: _installer,
        downloadState: 'verified',
        setupPath: _setupPath);
  }
}

/// Pumps the banner over [session] for [host], rebuilding off the session so
/// a reply / progress event re-renders it the way StatusHeader does — the
/// "Updating…" swap included, so the banner's element really goes away once
/// the installer is launched.
Future<void> _pumpBanner(
    WidgetTester tester, DaemonSession session, HostOs host) async {
  await tester.pumpWidget(MaterialApp(
    home: Scaffold(
      body: ListenableBuilder(
        listenable: session,
        builder: (context, _) {
          if (session.updating) return const UpdatingBanner();
          final st = session.offeredUpdate;
          return st == null
              ? const SizedBox()
              : UpdateBanner(session: session, status: st, host: host);
        },
      ),
    ),
  ));
}

/// Pumps the shell-level install-request consumer over [session]; the child
/// is a bare placeholder (the request opens a dialog above it).
Future<void> _pumpRequests(WidgetTester tester, DaemonSession session) async {
  await tester.pumpWidget(MaterialApp(
    home: Scaffold(
      body: UpdateInstallRequests(session: session, child: const SizedBox()),
    ),
  ));
}

/// Pumps the real StatusHeader (idle, so nothing animates) over [session].
Future<void> _pumpHeader(
    WidgetTester tester, _FakeClient client, DaemonSession session) async {
  await tester.pumpWidget(MaterialApp(
    home: Scaffold(
      body: SingleChildScrollView(
        child: StatusHeader(client: client, session: session),
      ),
    ),
  ));
}

void main() {
  group('StatusHeader slot', () {
    testWidgets('no banner without an offered update', (tester) async {
      final client = _FakeClient();
      final session = DaemonSession(client)
        ..connected = true
        ..updateStatus = const UpdateStatus(
            currentVersion: 'v1.2.3', available: false, transport: 'tunnel');
      addTearDown(session.dispose);
      await _pumpHeader(tester, client, session);
      expect(find.textContaining('Update available'), findsNothing);
    });

    testWidgets('a stale manifest hides the banner', (tester) async {
      final client = _FakeClient();
      final session = DaemonSession(client)
        ..connected = true
        ..updateStatus = _offer(stale: true);
      addTearDown(session.dispose);
      await _pumpHeader(tester, client, session);
      expect(find.textContaining('Update available'), findsNothing);
    });

    testWidgets('dismiss hides this version only; a newer one returns',
        (tester) async {
      final client = _FakeClient();
      final session = DaemonSession(client)
        ..connected = true
        ..daemonVersion = 'v1.2.3'
        ..updateStatus = _offer();
      addTearDown(session.dispose);
      await _pumpHeader(tester, client, session);
      expect(find.text('Update available: v1.2.4 (current v1.2.3)'),
          findsOneWidget);

      await tester.tap(find.byTooltip('Dismiss'));
      await tester.pump();
      expect(find.textContaining('Update available'), findsNothing);
      expect(session.dismissedUpdateVersion, 'v1.2.4');

      // The same version stays hidden across a rebuild…
      await _pumpHeader(tester, client, session);
      expect(find.textContaining('Update available'), findsNothing);
      // …a newer offer is shown again.
      session.updateStatus = _offer(version: 'v1.3.0');
      await _pumpHeader(tester, client, session);
      expect(find.text('Update available: v1.3.0 (current v1.2.3)'),
          findsOneWidget);
    });

    testWidgets('updating replaces the daemon-down banner', (tester) async {
      final client = _FakeClient();
      final session = DaemonSession(client)
        ..connected = false
        ..updating = true
        ..updateStatus = _offer(state: 'verified');
      addTearDown(session.dispose);
      await _pumpHeader(tester, client, session);
      await tester.pump(const Duration(milliseconds: 20)); // spinner: no settle
      expect(
          find.text('Updating… the app restarts when the installer finishes.'),
          findsOneWidget);
      expect(find.textContaining('Daemon unreachable'), findsNothing);
      expect(find.textContaining('Update available'), findsNothing);
    });

    testWidgets('daemon down (not updating) keeps the red banner only',
        (tester) async {
      final client = _FakeClient();
      final session = DaemonSession(client)
        ..connected = false
        ..updateStatus = _offer();
      addTearDown(session.dispose);
      await _pumpHeader(tester, client, session);
      expect(find.textContaining('Daemon unreachable'), findsOneWidget);
      expect(find.textContaining('Update available'), findsNothing);
    });
  });

  group('UpdateActions', () {
    testWidgets('Windows + installer: Download starts the download and the '
        'reply flips to progress', (tester) async {
      final client = _FakeClient();
      final session = DaemonSession(client)..updateStatus = _offer();
      addTearDown(session.dispose);
      await _pumpBanner(tester, session, HostOs.windows);

      expect(find.widgetWithText(IronButton, 'Download'), findsOneWidget);
      expect(find.widgetWithText(IronButton, 'Install'), findsNothing);
      expect(find.widgetWithText(IronButton, 'Release notes'), findsOneWidget);
      expect(find.textContaining('makepkg'), findsNothing);

      await tester.tap(find.widgetWithText(IronButton, 'Download'));
      await tester.pump();
      expect(client.downloads, 1);
      expect(find.byType(LinearProgressIndicator), findsOneWidget);
      expect(find.text('Downloading… 1.0 MiB / 4.0 MiB'), findsOneWidget);
      expect(find.widgetWithText(IronButton, 'Download'), findsNothing);
    });

    testWidgets('downloading: determinate when the total is known, '
        'indeterminate otherwise', (tester) async {
      final client = _FakeClient();
      final session = DaemonSession(client)
        ..updateStatus = _offer(state: 'downloading', received: 50, total: 200);
      addTearDown(session.dispose);
      await _pumpBanner(tester, session, HostOs.windows);
      var bar = tester
          .widget<LinearProgressIndicator>(find.byType(LinearProgressIndicator));
      expect(bar.value, 0.25);
      expect(find.text('Downloading… 50 B / 200 B'), findsOneWidget);

      session.updateStatus = _offer(state: 'downloading', received: 50);
      await _pumpBanner(tester, session, HostOs.windows);
      await tester.pump(const Duration(milliseconds: 20));
      bar = tester
          .widget<LinearProgressIndicator>(find.byType(LinearProgressIndicator));
      expect(bar.value, isNull);
      expect(find.text('Downloading… 50 B'), findsOneWidget);
    });

    testWidgets('downloaded: Install → consent → apply launches the '
        'installer and raises updating (cleared by the safety timeout)',
        (tester) async {
      final client = _FakeClient();
      final launched = <String>[];
      final session = DaemonSession(client,
          launchInstaller: (p) async => launched.add(p))
        ..updateStatus = _offer(state: 'downloaded');
      await _pumpBanner(tester, session, HostOs.windows);

      expect(find.widgetWithText(IronButton, 'Download'), findsNothing);
      await tester.tap(find.widgetWithText(IronButton, 'Install'));
      await tester.pumpAndSettle();
      expect(find.text('Install v1.2.4'), findsOneWidget);
      expect(
          find.textContaining('Windows will ask for administrator permission'),
          findsOneWidget);
      expect(client.applies, 0); // nothing until consent

      await tester.tap(find.widgetWithText(FilledButton, 'Install'));
      await tester.pump();
      await tester.pump();
      expect(client.applies, 1);
      expect(launched, [_setupPath]);
      expect(session.updating, isTrue);
      expect(session.lastUpdateCheckError, isNull);

      // No new daemon version ever shows up (say, a cancelled UAC): the
      // safety timeout drops the flag.
      await tester.pump(const Duration(minutes: 3));
      expect(session.updating, isFalse);
      session.dispose();
    });

    testWidgets('downloaded: Cancel in the consent dialog applies nothing',
        (tester) async {
      final client = _FakeClient();
      final session = DaemonSession(client, launchInstaller: (_) async {})
        ..updateStatus = _offer(state: 'downloaded');
      addTearDown(session.dispose);
      await _pumpBanner(tester, session, HostOs.windows);

      await tester.tap(find.widgetWithText(IronButton, 'Install'));
      await tester.pumpAndSettle();
      await tester.tap(find.widgetWithText(TextButton, 'Cancel'));
      await tester.pumpAndSettle();
      expect(client.applies, 0);
      expect(session.updating, isFalse);
    });

    testWidgets('a failed launch clears updating and reports via snackbar',
        (tester) async {
      final client = _FakeClient();
      final session = DaemonSession(client,
          launchInstaller: (_) async =>
              throw const UpdateLaunchError('installer not found: "x"'))
        ..updateStatus = _offer(state: 'downloaded');
      addTearDown(session.dispose);
      await _pumpBanner(tester, session, HostOs.windows);

      await tester.tap(find.widgetWithText(IronButton, 'Install'));
      await tester.pumpAndSettle();
      await tester.tap(find.widgetWithText(FilledButton, 'Install'));
      await tester.pump();
      await tester.pump();
      expect(client.applies, 1);
      expect(session.updating, isFalse);
      expect(session.lastUpdateCheckError,
          'Install failed: installer not found: "x"');
      await tester.pump(); // the snackbar's entrance frame
      expect(find.text('Install failed: installer not found: "x"'),
          findsOneWidget);
    });

    testWidgets('a launch failure that lands after the banner was swapped out '
        'still reports via snackbar', (tester) async {
      final client = _FakeClient();
      final launch = Completer<void>();
      final session = DaemonSession(client, launchInstaller: (_) => launch.future)
        ..updateStatus = _offer(state: 'downloaded');
      addTearDown(session.dispose);
      await _pumpBanner(tester, session, HostOs.windows);

      await tester.tap(find.widgetWithText(IronButton, 'Install'));
      await tester.pumpAndSettle();
      await tester.tap(find.widgetWithText(FilledButton, 'Install'));
      await tester.pump();
      await tester.pump();
      // The installer is "running": the host replaced the banner (and so the
      // element whose context started the action) with the updating card.
      expect(session.updating, isTrue);
      expect(find.byType(UpdateActions), findsNothing);
      expect(find.byType(UpdatingBanner), findsOneWidget);

      // Only now does CreateProcess report the failure.
      launch.completeError(
          const UpdateLaunchError('cannot start the installer: boom'));
      await tester.pump();
      expect(session.updating, isFalse);
      expect(session.lastUpdateCheckError,
          'Install failed: cannot start the installer: boom');
      await tester.pump(); // the snackbar's entrance frame
      expect(find.text('Install failed: cannot start the installer: boom'),
          findsOneWidget);
    });

    testWidgets('Install and Download are held while an installer runs',
        (tester) async {
      final client = _FakeClient();
      final session = DaemonSession(client)
        ..updating = true
        ..updateStatus = _offer(state: 'downloaded');
      addTearDown(session.dispose);
      // Hosted the way Settings does: no swap to the updating card there.
      await tester.pumpWidget(MaterialApp(
        home: Scaffold(
          body: UpdateActions(
              session: session,
              status: session.offeredUpdate!,
              host: HostOs.windows),
        ),
      ));
      final install =
          tester.widget<IronButton>(find.widgetWithText(IronButton, 'Install'));
      expect(install.onPressed, isNull);

      session.updateStatus = _offer(state: 'failed');
      await tester.pumpWidget(MaterialApp(
        home: Scaffold(
          body: UpdateActions(
              session: session,
              status: session.offeredUpdate!,
              host: HostOs.windows),
        ),
      ));
      final download = tester
          .widget<IronButton>(find.widgetWithText(IronButton, 'Download'));
      expect(download.onPressed, isNull);
    });

    testWidgets('a failed download offers Download again with a note',
        (tester) async {
      final client = _FakeClient();
      final session = DaemonSession(client)
        ..updateStatus = _offer(state: 'failed');
      addTearDown(session.dispose);
      await _pumpBanner(tester, session, HostOs.windows);
      expect(find.widgetWithText(IronButton, 'Download'), findsOneWidget);
      expect(find.text('Last download failed'), findsOneWidget);
    });

    testWidgets('Linux is notify-only: notes link + the makepkg hint',
        (tester) async {
      final client = _FakeClient();
      // The daemon offers no artifact on a notify-only channel.
      final session = DaemonSession(client)..updateStatus = _offer(artifact: null);
      addTearDown(session.dispose);
      await _pumpBanner(tester, session, HostOs.linux);
      expect(find.widgetWithText(IronButton, 'Download'), findsNothing);
      expect(find.widgetWithText(IronButton, 'Install'), findsNothing);
      expect(find.widgetWithText(IronButton, 'Release notes'), findsOneWidget);
      expect(find.text('Update with makepkg -si from the repository'),
          findsOneWidget);
    });

    testWidgets('Windows with a notify-only artifact kind has no buttons',
        (tester) async {
      final client = _FakeClient();
      final session = DaemonSession(client)
        ..updateStatus = _offer(
            artifact: const UpdateArtifact(os: 'windows', kind: 'none'),
            state: 'downloaded');
      addTearDown(session.dispose);
      await _pumpBanner(tester, session, HostOs.windows);
      expect(find.widgetWithText(IronButton, 'Download'), findsNothing);
      expect(find.widgetWithText(IronButton, 'Install'), findsNothing);
      expect(find.widgetWithText(IronButton, 'Release notes'), findsOneWidget);
      expect(find.textContaining('makepkg'), findsNothing);
    });
  });

  group('UpdateInstallRequests (the tray entry)', () {
    testWidgets('a request opens the same consent dialog; Install applies',
        (tester) async {
      final client = _FakeClient();
      final launched = <String>[];
      final session = DaemonSession(client,
          launchInstaller: (p) async => launched.add(p))
        ..updateStatus = _offer(state: 'downloaded');
      await _pumpRequests(tester, session);

      session.requestInstall();
      await tester.pump(); // the deferred dialog push + its frame
      await tester.pumpAndSettle();
      expect(session.installRequested, isFalse); // claimed
      expect(find.text('Install v1.2.4'), findsOneWidget);
      expect(
          find.textContaining('the connection drops for ~30 s'), findsOneWidget);
      expect(client.applies, 0);

      await tester.tap(find.widgetWithText(FilledButton, 'Install'));
      await tester.pump();
      await tester.pump();
      expect(client.applies, 1);
      expect(launched, [_setupPath]);
      expect(session.updating, isTrue);
      session.dispose();
    });

    testWidgets('Cancel applies nothing; a launch failure is shown',
        (tester) async {
      final client = _FakeClient();
      final session = DaemonSession(client,
          launchInstaller: (_) async =>
              throw const UpdateLaunchError('installer not found: "x"'))
        ..updateStatus = _offer(state: 'downloaded');
      addTearDown(session.dispose);
      await _pumpRequests(tester, session);

      session.requestInstall();
      await tester.pump();
      await tester.pumpAndSettle();
      await tester.tap(find.widgetWithText(TextButton, 'Cancel'));
      await tester.pumpAndSettle();
      expect(client.applies, 0);

      session.requestInstall();
      await tester.pump();
      await tester.pumpAndSettle();
      await tester.tap(find.widgetWithText(FilledButton, 'Install'));
      await tester.pump();
      await tester.pump();
      expect(client.applies, 1);
      expect(session.updating, isFalse);
      await tester.pump(); // the snackbar's entrance frame
      expect(find.text('Install failed: installer not found: "x"'),
          findsOneWidget);
    });

    testWidgets('a request with nothing staged, or while updating, is dropped',
        (tester) async {
      final client = _FakeClient();
      final session = DaemonSession(client)..updateStatus = _offer();
      addTearDown(session.dispose);
      await _pumpRequests(tester, session);

      session.requestInstall(); // only downloaded, nothing to install yet
      await tester.pump();
      await tester.pumpAndSettle();
      expect(session.installRequested, isFalse);
      expect(find.text('Install v1.2.4'), findsNothing);

      session
        ..updateStatus = _offer(state: 'downloaded')
        ..updating = true;
      session.requestInstall();
      await tester.pump();
      await tester.pumpAndSettle();
      expect(find.text('Install v1.2.4'), findsNothing);
      expect(client.applies, 0);
    });
  });
}
