/// The tray's "Update to vX…" entry: present only for an offered update,
/// gated while an update action or a launched installer is in flight, and on
/// click it surfaces the window and — Windows with a staged installer — hands
/// the shell an install request (never the apply verb directly: the consent
/// dialog lives in the widget tree).
library;

import 'package:flutter_test/flutter_test.dart';
import 'package:iron_link_flutter/src/app/session.dart';
import 'package:iron_link_flutter/src/ipc/client.dart';
import 'package:iron_link_flutter/src/platform/open_url.dart';
import 'package:iron_link_flutter/src/platform/tray/tray_controller.dart';
import 'package:iron_link_flutter/src/platform/tray/tray_menu_item.dart';
import 'package:iron_link_flutter/src/wire/wire.dart';

/// A tray with no platform behind it: the menu model is read directly and the
/// window show is recorded instead of touching window_manager.
class _TestTray extends TrayController {
  _TestTray({required super.session, required super.host})
      : super(client: DaemonClient(endpoint: '/dev/null'));

  int shown = 0;

  List<TrayMenuItem> menu() => buildMenu();

  @override
  Future<void> showWindow() async => shown++;

  @override
  Future<void> platformStart(List<TrayMenuItem> menu, TrayState state) async {}
  @override
  Future<void> platformSetMenu(List<TrayMenuItem> menu) async {}
  @override
  Future<void> platformSetIcon(TrayState state) async {}
  @override
  Future<void> platformDispose() async {}
}

const _installer = UpdateArtifact(
    os: 'windows', arch: 'amd64', kind: 'installer', name: 'setup.exe');

UpdateStatus _offer({String state = '', bool stale = false}) => UpdateStatus(
    currentVersion: 'v1.2.3',
    available: true,
    stale: stale,
    latestVersion: 'v1.2.4',
    artifact: _installer,
    downloadState: state);

TrayMenuItem? _updateItem(_TestTray tray) {
  for (final item in tray.menu()) {
    if (item.id == 8) return item;
  }
  return null;
}

void main() {
  test('the entry exists only for a fresh offered update', () {
    final session = DaemonSession(DaemonClient(endpoint: '/dev/null'));
    addTearDown(session.dispose);
    final tray = _TestTray(session: session, host: HostOs.windows);

    expect(_updateItem(tray), isNull);
    session.updateStatus = _offer(stale: true);
    expect(_updateItem(tray), isNull);
    session.updateStatus = _offer();
    expect(_updateItem(tray)?.label, 'Update to v1.2.4…');
  });

  test('the entry is held while busy or while an installer runs', () {
    final session = DaemonSession(DaemonClient(endpoint: '/dev/null'))
      ..updateStatus = _offer(state: 'downloaded');
    addTearDown(session.dispose);
    final tray = _TestTray(session: session, host: HostOs.windows);

    expect(_updateItem(tray)!.isEnabled, isTrue);
    session.updateBusy = true;
    expect(_updateItem(tray)!.isEnabled, isFalse);
    session.updateBusy = false;
    session.updating = true;
    expect(_updateItem(tray)!.isEnabled, isFalse);
  });

  test('Windows + staged installer: the click shows the window and raises an '
      'install request (no apply verb)', () async {
    final session = DaemonSession(DaemonClient(endpoint: '/dev/null'))
      ..updateStatus = _offer(state: 'downloaded');
    addTearDown(session.dispose);
    final tray = _TestTray(session: session, host: HostOs.windows);

    _updateItem(tray)!.onClick!();
    await pumpEventQueue();
    expect(tray.shown, 1);
    expect(session.installRequested, isTrue);
    expect(session.updating, isFalse); // nothing launched from here
  });

  test('not yet downloaded, or not Windows: the click only shows the window',
      () async {
    final session = DaemonSession(DaemonClient(endpoint: '/dev/null'))
      ..updateStatus = _offer();
    addTearDown(session.dispose);
    final windows = _TestTray(session: session, host: HostOs.windows);
    _updateItem(windows)!.onClick!();
    await pumpEventQueue();
    expect(windows.shown, 1);
    expect(session.installRequested, isFalse);

    session.updateStatus = _offer(state: 'downloaded');
    final linux = _TestTray(session: session, host: HostOs.linux);
    _updateItem(linux)!.onClick!();
    await pumpEventQueue();
    expect(linux.shown, 1);
    expect(session.installRequested, isFalse);
  });
}
