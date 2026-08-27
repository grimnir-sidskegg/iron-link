/// Session plumbing for the update system (no UI): the update status is
/// read from `check_update` once the daemon is confirmed up, an old daemon's
/// "unimplemented verb" error means "no update support" (status stays null),
/// the `update_available` nudge triggers a re-read, and `update_progress`
/// events patch the download counters live.
library;

import 'dart:async';

import 'package:flutter_test/flutter_test.dart';
import 'package:iron_link_flutter/src/app/session.dart';
import 'package:iron_link_flutter/src/ipc/client.dart';
import 'package:iron_link_flutter/src/platform/update_launcher.dart';
import 'package:iron_link_flutter/src/wire/wire.dart';

/// Drives a controllable event stream and a scripted `check_update`: a null
/// [updateStatus] plays an OLD daemon (the verb answers `error`). A null
/// [version] leaves the status poll pending; otherwise it answers idle with
/// that `daemon_version`. `download_update` answers through [downloadReply]
/// so a test can hold the reply while events land.
class _UpdateClient extends DaemonClient {
  _UpdateClient({this.updateStatus, this.version, this.applyReply})
      : super(endpoint: '/dev/null');

  final events = StreamController<Event>();
  UpdateStatus? updateStatus;
  String? version;
  UpdateStatus? applyReply;
  final downloadReply = Completer<UpdateStatus>();
  int checkCalls = 0;
  int applyCalls = 0;

  @override
  Stream<Event> subscribe() => events.stream;

  @override
  Future<Response> status() => version == null
      ? Completer<Response>().future // stays pending
      : Future.value(IdleResponse(daemonVersion: version));

  @override
  Future<UpdateStatus> applyUpdate() async {
    applyCalls++;
    return applyReply ?? (throw const DaemonError('nothing downloaded'));
  }

  @override
  Future<UpdateStatus> downloadUpdate() => downloadReply.future;

  @override
  Future<UpdateStatus> checkUpdate({bool force = false}) async {
    checkCalls++;
    final st = updateStatus;
    if (st == null) {
      // What an old daemon's unknown-verb error surfaces as.
      throw const DaemonError('unimplemented verb: check_update');
    }
    return st;
  }
}

Future<void> _pump() async {
  await Future<void>.value();
  await Future<void>.value();
}

void main() {
  test('the first event confirms the daemon and populates the update status',
      () async {
    final client = _UpdateClient(
        updateStatus: const UpdateStatus(
            currentVersion: 'v1.0.0',
            available: true,
            latestVersion: 'v1.2.3',
            transport: 'tunnel'));
    final session = DaemonSession(client);
    addTearDown(session.dispose);

    session.start();
    await _pump();
    expect(session.updateStatus, isNull); // nothing before the daemon is up

    client.events.add(const StateEvent());
    await _pump();
    expect(client.checkCalls, 1);
    expect(session.updateStatus?.available, isTrue);
    expect(session.updateStatus?.latestVersion, 'v1.2.3');
  });

  test('an old daemon without the verb leaves the update status null',
      () async {
    final client = _UpdateClient(); // checkUpdate throws DaemonError
    final session = DaemonSession(client);
    addTearDown(session.dispose);

    session.start();
    await _pump();
    client.events.add(const StateEvent());
    await _pump();
    expect(client.checkCalls, 1);
    expect(session.updateStatus, isNull); // "no update support", not a crash
  });

  test('update_available re-reads the verb; update_progress patches counters',
      () async {
    final client = _UpdateClient(
        updateStatus: const UpdateStatus(
            currentVersion: 'v1.0.0', available: true, latestVersion: 'v1.2.3'));
    final session = DaemonSession(client);
    addTearDown(session.dispose);

    session.start();
    await _pump();
    client.events.add(const StateEvent());
    await _pump();
    expect(client.checkCalls, 1);

    // The nudge: a later check found something — re-read the truth.
    client.updateStatus = const UpdateStatus(
        currentVersion: 'v1.0.0', available: true, latestVersion: 'v1.3.0');
    client.events.add(const UpdateAvailableEvent(version: 'v1.3.0'));
    await _pump();
    expect(client.checkCalls, 2);
    expect(session.updateStatus?.latestVersion, 'v1.3.0');

    // Progress events patch the download half in place.
    client.events.add(const UpdateProgressEvent(
        received: 1024, total: 4096, state: 'downloading'));
    await _pump();
    expect(session.updateStatus?.downloadState, 'downloading');
    expect(session.updateStatus?.downloadReceived, 1024);
    expect(session.updateStatus?.downloadTotal, 4096);
    expect(session.updateStatus?.latestVersion, 'v1.3.0'); // rest untouched

    client.events.add(const UpdateProgressEvent(
        received: 4096, total: 4096, state: 'downloaded'));
    await _pump();
    expect(session.updateStatus?.downloadState, 'downloaded');
  });

  const installer = UpdateArtifact(
      os: 'windows', arch: 'amd64', kind: 'installer', name: 'setup.exe');
  const downloaded = UpdateStatus(
      currentVersion: 'v1.0.0',
      available: true,
      latestVersion: 'v1.2.3',
      artifact: installer,
      downloadState: 'downloaded');
  const verified = UpdateStatus(
      currentVersion: 'v1.0.0',
      available: true,
      latestVersion: 'v1.2.3',
      artifact: installer,
      downloadState: 'verified',
      setupPath: r'C:\ProgramData\iron-link\updates\setup.exe');

  test('apply launches the verified path; updating holds until a poll '
      'reports a different daemon version', () async {
    final client = _UpdateClient(
        updateStatus: downloaded, version: 'v1.0.0', applyReply: verified);
    final launched = <String>[];
    final session =
        DaemonSession(client, launchInstaller: (p) async => launched.add(p));
    addTearDown(session.dispose);

    session.start(); // the first backstop poll answers v1.0.0
    await _pump();
    expect(session.daemonVersion, 'v1.0.0');

    await session.applyUpdate();
    expect(client.applyCalls, 1);
    expect(launched, [r'C:\ProgramData\iron-link\updates\setup.exe']);
    expect(session.updating, isTrue);
    expect(session.updateBusy, isFalse);
    expect(session.lastUpdateCheckError, isNull);

    // The OLD daemon still answering (the installer has not stopped it yet)
    // keeps the flag up.
    await session.refreshStatus();
    expect(session.updating, isTrue);

    // The restarted daemon answers with its new build: the update landed.
    client.version = 'v1.2.3';
    await session.refreshStatus();
    expect(session.updating, isFalse);
  });

  test('a terminal progress event that lands during the download request '
      'is not regressed by the reply', () async {
    const offer = UpdateStatus(
        currentVersion: 'v1.0.0', available: true, latestVersion: 'v1.2.3');
    final client = _UpdateClient(updateStatus: offer, version: 'v1.0.0');
    final session = DaemonSession(client);
    addTearDown(session.dispose);

    session.start();
    await _pump();
    client.events.add(const StateEvent());
    await _pump();
    expect(session.updateStatus?.downloadState, '');

    final download = session.startUpdateDownload();
    await _pump();
    expect(session.updateBusy, isTrue);

    // The daemon's goroutine failed fast; its event beat the verb's reply.
    client.events.add(const UpdateProgressEvent(state: 'failed'));
    await _pump();
    expect(session.updateStatus?.downloadState, 'failed');

    client.downloadReply.complete(const UpdateStatus(
        currentVersion: 'v1.0.0',
        available: true,
        latestVersion: 'v1.2.3',
        downloadState: 'downloading',
        downloadTotal: 4096));
    await download;
    expect(session.updateStatus?.downloadState, 'failed');
    expect(session.updateBusy, isFalse);
    expect(session.lastUpdateCheckError, isNull);
  });

  test('updating holds when the version baseline came from the apply reply',
      () async {
    // The poll stays pending: the first state event drops its late reply, so
    // daemonVersion is still null when Install is offered.
    final client =
        _UpdateClient(updateStatus: downloaded, applyReply: verified);
    final session = DaemonSession(client, launchInstaller: (_) async {});
    addTearDown(session.dispose);

    session.start();
    await _pump();
    client.events.add(const StateEvent());
    await _pump();
    expect(session.daemonVersion, isNull);

    await session.applyUpdate();
    expect(session.updating, isTrue);

    // The OLD build answering the next poll must not end it.
    client.version = 'v1.0.0';
    await session.refreshStatus();
    expect(session.updating, isTrue);

    client.version = 'v1.2.3';
    await session.refreshStatus();
    expect(session.updating, isFalse);
  });

  test('a second apply while updating launches nothing', () async {
    final client = _UpdateClient(
        updateStatus: downloaded, version: 'v1.0.0', applyReply: verified);
    final launched = <String>[];
    final session =
        DaemonSession(client, launchInstaller: (p) async => launched.add(p));
    addTearDown(session.dispose);

    await session.applyUpdate();
    await session.applyUpdate();
    expect(client.applyCalls, 1);
    expect(launched, hasLength(1));
    expect(session.updating, isTrue);
  });

  test('an install request is claimed exactly once', () {
    final client = _UpdateClient(updateStatus: downloaded);
    final session = DaemonSession(client)..updateStatus = downloaded;
    addTearDown(session.dispose);

    expect(session.takeInstallRequest(), isFalse);
    session.requestInstall();
    expect(session.installRequested, isTrue);
    expect(session.takeInstallRequest(), isTrue);
    expect(session.takeInstallRequest(), isFalse);
    expect(session.installRequested, isFalse);
  });

  test('a failed launch drops updating and reports the error', () async {
    final client = _UpdateClient(
        updateStatus: downloaded, version: 'v1.0.0', applyReply: verified);
    final session = DaemonSession(client,
        launchInstaller: (_) async =>
            throw const UpdateLaunchError('cannot start the installer: no'));
    addTearDown(session.dispose);

    await session.applyUpdate();
    expect(client.applyCalls, 1);
    expect(session.updating, isFalse);
    expect(session.lastUpdateCheckError,
        'Install failed: cannot start the installer: no');
  });

  test('an apply reply without a verified path never launches', () async {
    final client = _UpdateClient(
        updateStatus: downloaded, version: 'v1.0.0', applyReply: downloaded);
    final launched = <String>[];
    final session =
        DaemonSession(client, launchInstaller: (p) async => launched.add(p));
    addTearDown(session.dispose);

    await session.applyUpdate();
    expect(launched, isEmpty);
    expect(session.updating, isFalse);
    expect(session.lastUpdateCheckError, startsWith('Install failed: '));

    // A daemon `error` reply (nothing downloaded) surfaces the same way.
    client.applyReply = null;
    await session.applyUpdate();
    expect(session.lastUpdateCheckError, 'Install failed: nothing downloaded');
    expect(launched, isEmpty);
  });
}
