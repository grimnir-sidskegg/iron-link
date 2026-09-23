/// Widget tests for the daemon-down banner in the real StatusHeader: the
/// probe's detail is rendered under the headline, Start is offered only when
/// the report allows it, and a click goes to the injected starter. The probe
/// and the starter are fakes — the real ones run systemctl / launchctl /
/// sc.exe, and the real Start would pop UAC under a native Windows
/// `flutter test`.
library;

import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:iron_link_flutter/src/app/session.dart';
import 'package:iron_link_flutter/src/ipc/client.dart';
import 'package:iron_link_flutter/src/platform/daemon_service.dart';
import 'package:iron_link_flutter/src/platform/open_url.dart';
import 'package:iron_link_flutter/src/ui/status_header.dart';
import 'package:iron_link_flutter/src/ui/widgets/iron_widgets.dart';
import 'package:iron_link_flutter/src/wire/wire.dart';

const _stopped = ServiceReport(
    ServiceState.stopped, 'The iron-link service is stopped.',
    canStart: true);
const _running = ServiceReport(ServiceState.running,
    'The service is running but /dev/null is unreachable. See: journalctl -u iron-link');

/// Records the probes (with their cause) and the starts.
class _Fakes {
  _Fakes({this.report = _stopped, this.outcome = (StartOutcome.launched, '')});

  ServiceReport report;
  StartResult outcome;
  final probes = <Object?>[];
  int starts = 0;

  Future<ServiceReport> probe({Object? cause}) async {
    probes.add(cause);
    return report;
  }

  Future<StartResult> start() async {
    starts++;
    return outcome;
  }
}

/// The Linux/Windows label; macOS reads "Start daemon" (its own test).
final _startButton = find.widgetWithText(IronButton, 'Start service');

Future<void> _pump(WidgetTester tester, DaemonSession session, _Fakes f,
    {HostOs host = HostOs.linux}) async {
  await tester.pumpWidget(MaterialApp(
    home: Scaffold(
      body: SingleChildScrollView(
        child: StatusHeader(
          client: session.client,
          session: session,
          host: host,
          probe: f.probe,
          start: f.start,
        ),
      ),
    ),
  ));
}

/// A dead socket, as the first failed connect records it.
const _enoent = SocketException('connect', osError: OSError('', 2));

/// A passive session (no subscribe loop) with the connect failure already
/// recorded, unless [cause] is null.
DaemonSession _session({Object? cause = _enoent}) {
  final session = DaemonSession(DaemonClient(endpoint: '/dev/null'))
    ..unreachableCause = cause;
  addTearDown(session.dispose);
  return session;
}

void main() {
  testWidgets('headline, detail and Start when the service can be started',
      (tester) async {
    final f = _Fakes();
    final session = _session();
    await _pump(tester, session, f);
    await tester.pump();
    expect(find.text('Daemon unreachable at /dev/null.'), findsOneWidget);
    expect(find.text('The iron-link service is stopped.'), findsOneWidget);
    expect(_startButton, findsOneWidget);
    expect(find.textContaining('sudo'), findsNothing);
    expect(f.probes, [_enoent]);
  });

  testWidgets('no Start when the report says so', (tester) async {
    final f = _Fakes(report: _running);
    await _pump(tester, _session(), f);
    await tester.pump();
    expect(find.text(_running.detail), findsOneWidget);
    expect(_startButton, findsNothing);
  });

  testWidgets('macOS: the button reads Start daemon', (tester) async {
    await _pump(tester, _session(), _Fakes(), host: HostOs.macos);
    await tester.pump();
    expect(find.widgetWithText(IronButton, 'Start daemon'), findsOneWidget);
    expect(_startButton, findsNothing);
  });

  testWidgets('the probe waits for the connect failure\'s cause, then runs once',
      (tester) async {
    final f = _Fakes();
    final session = _session(cause: null);
    await _pump(tester, session, f);
    await tester.pump();
    expect(find.text('Daemon unreachable at /dev/null.'), findsOneWidget);
    expect(find.text(_stopped.detail), findsNothing);
    expect(f.probes, isEmpty);

    const cause = SocketException('connect', osError: OSError('', 13));
    session
      ..unreachableCause = cause
      ..notifyListeners();
    await tester.pump();
    expect(f.probes, [cause]);
    await tester.pump(); // the report's frame
    expect(find.text(_stopped.detail), findsOneWidget);

    // Later reconnect failures are not news.
    session
      ..unreachableCause = const SocketException('connect')
      ..notifyListeners();
    await tester.pump();
    expect(f.probes, hasLength(1));
  });

  testWidgets('Start: launched → held while busy, re-probed after a pause',
      (tester) async {
    final f = _Fakes();
    final session = _session();
    await _pump(tester, session, f);
    await tester.pump();

    await tester.tap(_startButton);
    await tester.pump();
    expect(f.starts, 1);
    expect(tester.widget<IronButton>(_startButton).onPressed, isNull);
    expect(f.probes, hasLength(1));

    await tester.pump(const Duration(seconds: 2));
    expect(f.probes, hasLength(2));
    expect(tester.widget<IronButton>(_startButton).onPressed, isNotNull);
    expect(find.byType(SnackBar), findsNothing);
  });

  testWidgets('Start: cancelled → nothing; failed → a snackbar',
      (tester) async {
    final f = _Fakes(outcome: (StartOutcome.cancelled, ''));
    final session = _session();
    await _pump(tester, session, f);
    await tester.pump();

    await tester.tap(_startButton);
    await tester.pump();
    await tester.pump();
    expect(f.starts, 1);
    expect(f.probes, hasLength(1));
    expect(find.byType(SnackBar), findsNothing);
    expect(tester.widget<IronButton>(_startButton).onPressed, isNotNull);

    f.outcome = (StartOutcome.failed, 'No authentication agent is available.');
    await tester.tap(_startButton);
    await tester.pump();
    await tester.pump(); // the snackbar's entrance frame
    expect(f.starts, 2);
    expect(find.text('No authentication agent is available.'), findsOneWidget);
    expect(f.probes, hasLength(1));
  });

  // The session's reconnect bookkeeping, without a socket: the private
  // handlers are driven through a subscribe() that ends at once.
  test('a subscribe failure records its cause', () async {
    final client = _FailingClient();
    final session = DaemonSession(client);
    addTearDown(session.dispose);
    var notified = 0;
    session.addListener(() => notified++);
    session.start();
    expect(session.unreachableCause, isNull);
    await Future<void>.delayed(Duration.zero);
    expect(session.connected, isFalse);
    expect(session.unreachableCause, client.cause);
    expect(notified, 1); // the first cause is news, once
  });

  test('a stream that ends without an error records a cause too', () async {
    final session = DaemonSession(_FailingClient(closes: true));
    addTearDown(session.dispose);
    session.start();
    await Future<void>.delayed(Duration.zero);
    expect(session.unreachableCause, isNotNull);
  });
}

/// A client whose subscribe stream fails immediately with a
/// [DaemonUnreachable] — or, with [closes], ends cleanly at once; status
/// polls fail either way.
class _FailingClient extends DaemonClient {
  _FailingClient({this.closes = false}) : super(endpoint: '/dev/null');

  final bool closes;
  final cause = const SocketException('connect', osError: OSError('', 2));

  @override
  Stream<Event> subscribe() => closes
      ? const Stream<Event>.empty()
      : Stream<Event>.error(DaemonUnreachable('/dev/null', cause));

  @override
  Future<Response> status() async => throw DaemonUnreachable('/dev/null', cause);
}
