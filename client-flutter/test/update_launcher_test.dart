/// Unit tests for the Windows apply launch's path guard and argv. The
/// spawner is injected, so nothing forks; the "exists" check runs against a
/// real temp file.
library;

import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:iron_link_flutter/src/platform/update_launcher.dart';

class _NoProcess implements Process {
  @override
  dynamic noSuchMethod(Invocation invocation) => throw UnimplementedError();
}

/// Records every spawn as `exe|argv|mode` (a List inside a record would
/// compare by identity).
class _Spawner {
  final calls = <String>[];
  bool fail = false;

  Future<Process> call(String exe, List<String> args,
      {ProcessStartMode mode = ProcessStartMode.normal}) async {
    if (fail) throw ProcessException(exe, args, 'access denied', 5);
    final detached = mode == ProcessStartMode.detached ? 'detached' : 'attached';
    calls.add('$exe|${args.join(' ')}|$detached');
    return _NoProcess();
  }
}

void main() {
  late Directory dir;
  late String setup;

  setUp(() {
    dir = Directory.systemTemp.createTempSync('iron_link_update_');
    setup = '${dir.path}${Platform.pathSeparator}iron-link-setup.exe';
    File(setup).writeAsStringSync('not really an installer');
  });

  tearDown(() => dir.deleteSync(recursive: true));

  test('a verified absolute .exe launches detached with the silent argv',
      () async {
    final s = _Spawner();
    await launchUpdateInstaller(setup, spawn: s.call);
    expect(s.calls,
        ['$setup|/SILENT /SUPPRESSMSGBOXES /NORESTART /UPDATE=1|detached']);
  });

  test('a relative path is rejected before any spawn', () async {
    final s = _Spawner();
    await expectLater(
        launchUpdateInstaller('iron-link-setup.exe', spawn: s.call),
        throwsA(isA<UpdateLaunchError>()));
    await expectLater(launchUpdateInstaller('', spawn: s.call),
        throwsA(isA<UpdateLaunchError>()));
    expect(s.calls, isEmpty);
  });

  test('an absolute path that is not an .exe is rejected', () async {
    final s = _Spawner();
    final notExe = '${dir.path}${Platform.pathSeparator}iron-link-setup.msi';
    File(notExe).writeAsStringSync('x');
    await expectLater(launchUpdateInstaller(notExe, spawn: s.call),
        throwsA(isA<UpdateLaunchError>()));
    expect(s.calls, isEmpty);
  });

  test('a missing file is rejected', () async {
    final s = _Spawner();
    final missing = '${dir.path}${Platform.pathSeparator}gone.exe';
    await expectLater(launchUpdateInstaller(missing, spawn: s.call),
        throwsA(isA<UpdateLaunchError>()));
    expect(s.calls, isEmpty);
  });

  test('a CreateProcess failure surfaces as a launch error', () async {
    final s = _Spawner()..fail = true;
    await expectLater(
        launchUpdateInstaller(setup, spawn: s.call),
        throwsA(isA<UpdateLaunchError>().having(
            (e) => e.message, 'message', contains('cannot start'))));
  });
}
