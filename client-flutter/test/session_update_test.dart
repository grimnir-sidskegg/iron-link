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
import 'package:iron_link_flutter/src/wire/wire.dart';

/// Drives a controllable event stream and a scripted `check_update`: a null
/// [updateStatus] plays an OLD daemon (the verb answers `error`).
class _UpdateClient extends DaemonClient {
  _UpdateClient({this.updateStatus}) : super(endpoint: '/dev/null');

  final events = StreamController<Event>();
  UpdateStatus? updateStatus;
  int checkCalls = 0;

  @override
  Stream<Event> subscribe() => events.stream;

  @override
  Future<Response> status() => Completer<Response>().future; // stays pending

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
}
