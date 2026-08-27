/// Unit tests for the browser opener's scheme gate: only an https URL with a
/// host reaches the per-OS opener command. The spawner is injected, so
/// nothing forks.
library;

import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:iron_link_flutter/src/platform/open_url.dart';

/// A [Process] nobody looks at — the helpers only await the spawn.
class _NoProcess implements Process {
  @override
  dynamic noSuchMethod(Invocation invocation) => throw UnimplementedError();
}

/// Records every spawn as `exe|argv|mode` (or fails them all when [fail] is
/// set). A joined string, because a List inside a record compares by
/// identity.
class _Spawner {
  final calls = <String>[];
  bool fail = false;

  Future<Process> call(String exe, List<String> args,
      {ProcessStartMode mode = ProcessStartMode.normal}) async {
    if (fail) throw ProcessException(exe, args, 'no such opener', 2);
    final detached = mode == ProcessStartMode.detached ? 'detached' : 'attached';
    calls.add('$exe|${args.join(' ')}|$detached');
    return _NoProcess();
  }
}

void main() {
  const url = 'https://example.com/iron-link/notes/v1.2.3';

  test('isOpenableUrl accepts only https with a host', () {
    expect(isOpenableUrl(url), isTrue);
    expect(isOpenableUrl('HTTPS://example.com/'), isTrue); // scheme is case-insensitive
    expect(isOpenableUrl('http://example.com/'), isFalse);
    expect(isOpenableUrl('file:///etc/passwd'), isFalse);
    expect(isOpenableUrl('javascript:alert(1)'), isFalse);
    expect(isOpenableUrl('https://'), isFalse); // no host
    expect(isOpenableUrl('example.com/notes'), isFalse); // no scheme
    expect(isOpenableUrl(''), isFalse);
  });

  test('each OS gets its opener command, detached', () async {
    final s = _Spawner();
    expect(await openUrl(url, host: HostOs.linux, spawn: s.call), isTrue);
    expect(await openUrl(url, host: HostOs.windows, spawn: s.call), isTrue);
    expect(await openUrl(url, host: HostOs.macos, spawn: s.call), isTrue);
    expect(s.calls, [
      'xdg-open|$url|detached',
      'rundll32|url.dll,FileProtocolHandler $url|detached',
      'open|$url|detached',
    ]);
  });

  test('a non-https URL never reaches the opener', () async {
    final s = _Spawner();
    for (final bad in [
      'http://example.com/',
      'file:///etc/passwd',
      'javascript:alert(1)',
      'https://',
      '',
    ]) {
      expect(await openUrl(bad, host: HostOs.linux, spawn: s.call), isFalse,
          reason: bad);
    }
    expect(s.calls, isEmpty);
  });

  test('an OS without an opener, or a failed spawn, reports false', () async {
    final s = _Spawner();
    expect(await openUrl(url, host: HostOs.other, spawn: s.call), isFalse);
    expect(s.calls, isEmpty);

    s.fail = true;
    expect(await openUrl(url, host: HostOs.linux, spawn: s.call), isFalse);
  });
}
