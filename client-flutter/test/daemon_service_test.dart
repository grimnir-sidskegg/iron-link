/// Unit tests for the daemon-service probe and Start: each OS's parser and
/// classifier over the real command outputs, and the Start result mapping.
/// The subprocess runner and the Windows elevation are injected — nothing
/// here runs systemctl, launchctl, sc.exe or ShellExecuteW (CI runs these
/// natively on Windows, where the real Start would pop UAC).
library;

import 'dart:async';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:iron_link_flutter/src/platform/daemon_service.dart';
import 'package:iron_link_flutter/src/platform/open_url.dart';

ProcessResult _r(int code, String out, [String err = '']) =>
    ProcessResult(1, code, out, err);

/// Answers each command from a table keyed by `exe argv…` and records the
/// calls; a command not in the table fails like a missing executable.
class _Runner {
  _Runner(this.answers);

  final Map<String, ProcessResult> answers;
  final calls = <String>[];
  bool hang = false;

  Future<ProcessResult> call(String exe, List<String> args,
      {Map<String, String>? environment, bool runInShell = false}) {
    final key = '$exe ${args.join(' ')}';
    calls.add(key);
    if (hang) return Completer<ProcessResult>().future;
    final r = answers[key];
    if (r == null) throw ProcessException(exe, args, 'no such command', 2);
    return Future.value(r);
  }
}

// ---- Linux ----

const _systemctlShow =
    'systemctl show --no-pager -p LoadState,ActiveState,SubState,Result,NRestarts iron-link.service';
const _systemctlStart = 'systemctl start iron-link.service';

/// `systemctl show` output as systemd prints it (its own property order).
String _sd(
        {String load = 'loaded',
        required String active,
        required String sub,
        String result = 'success',
        String restarts = '0'}) =>
    'LoadState=$load\nActiveState=$active\nSubState=$sub\nResult=$result\nNRestarts=$restarts\n';

final _sdStopped = _sd(active: 'inactive', sub: 'dead');
final _sdRunning = _sd(active: 'active', sub: 'running');

const _linuxSock = '/run/iron-link/iron-link.sock';
const _linuxEnv = {'IRON_LINK_SOCKET': _linuxSock};

Future<ServiceReport> _linux(String show, {Object? cause}) =>
    probeDaemonService(
        host: HostOs.linux,
        run: _Runner({_systemctlShow: _r(0, show)}).call,
        environment: _linuxEnv,
        cause: cause);

// ---- macOS ----

const _launchctlPrint = 'launchctl print system/org.iron-link.daemon';
const _osascript =
    '/usr/bin/osascript -e do shell script "launchctl print system/org.iron-link.daemon >/dev/null 2>&1 && launchctl kickstart -k system/org.iron-link.daemon || launchctl bootstrap system /Library/LaunchDaemons/org.iron-link.daemon.plist" with prompt "iron-link needs to start its background daemon." with administrator privileges';
const _plist = '/Library/LaunchDaemons/org.iron-link.daemon.plist';

// `launchctl print system/org.iron-link.daemon` on the running service, as
// printed (tabs; the coalition blocks carry their own `state = active`).
const _lcRunning = 'system/org.iron-link.daemon = {\n'
    '\tactive count = 1\n'
    '\tpath = /Library/LaunchDaemons/org.iron-link.daemon.plist\n'
    '\ttype = LaunchDaemon\n'
    '\tstate = running\n'
    '\n'
    '\tprogram = /usr/local/bin/iron-link-daemon\n'
    '\targuments = {\n'
    '\t\t/usr/local/bin/iron-link-daemon\n'
    '\t}\n'
    '\n'
    '\tstdout path = /Users/administrator/Library/Application Support/iron-link/daemon.log\n'
    '\tstderr path = /Users/administrator/Library/Application Support/iron-link/daemon.log\n'
    '\tdefault environment = {\n'
    '\t\tPATH => /usr/bin:/bin:/usr/sbin:/sbin\n'
    '\t}\n'
    '\n'
    '\tenvironment = {\n'
    '\t\tOSLogRateLimit => 64\n'
    '\t\tSUDO_GID => 20\n'
    '\t\tSUDO_UID => 501\n'
    '\t\tIRON_LINK_CONFIG_DIR => /Users/administrator/Library/Application Support/iron-link\n'
    '\t\tXPC_SERVICE_NAME => org.iron-link.daemon\n'
    '\t}\n'
    '\n'
    '\tdomain = system\n'
    '\tminimum runtime = 10\n'
    '\texit timeout = 5\n'
    '\truns = 1\n'
    '\tpid = 10526\n'
    '\timmediate reason = speculative\n'
    '\tforks = 0\n'
    '\texecs = 1\n'
    '\tinitialized = 1\n'
    '\ttrampolined = 1\n'
    '\tstarted suspended = 0\n'
    '\tproxy started suspended = 0\n'
    '\tchecked allocations = 0 (queried = 1)\n'
    '\tchecked allocations reason = no host\n'
    '\tchecked allocations flags = 0x0\n'
    '\tlast exit code = (never exited)\n'
    '\n'
    '\tresource coalition = {\n'
    '\t\tID = 4782\n'
    '\t\ttype = resource\n'
    '\t\tstate = active\n'
    '\t\tactive count = 1\n'
    '\t\tname = org.iron-link.daemon\n'
    '\t}\n'
    '\n'
    '\tjetsam coalition = {\n'
    '\t\tID = 4783\n'
    '\t\ttype = jetsam\n'
    '\t\tstate = active\n'
    '\t\tactive count = 1\n'
    '\t\tname = org.iron-link.daemon\n'
    '\t}\n'
    '\n'
    '\tspawn type = daemon (3)\n'
    '\tjetsam priority = 40\n'
    '\tjetsam memory limit (active) = (unlimited)\n'
    '\tjetsam memory limit (inactive) = (unlimited)\n'
    '\tjetsamproperties category = daemon\n'
    '\tjetsam thread limit = 32\n'
    '\tcpumon = default\n'
    '\n'
    '\tproperties = keepalive | runatload | inferred program\n'
    '}\n';

/// The same service after its process exited: no pid line, an exit code.
final _lcNotRunning = _lcRunning
    .replaceFirst('\tstate = running\n', '\tstate = not running\n')
    .replaceFirst('\tpid = 10526\n', '')
    .replaceFirst('(never exited)', '1');

/// launchd could not exec the program: scheduled again and again, EX_CONFIG.
final _lcSpawnScheduled = _lcRunning
    .replaceFirst('\tstate = running\n', '\tstate = spawn scheduled\n')
    .replaceFirst('\tpid = 10526\n', '')
    .replaceFirst('(never exited)', '78: EX_CONFIG');

const _lcNotLoadedErr =
    'Bad request.\nCould not find service "org.iron-link.daemon" in domain for system\n';

const _macRoot = '/Users/administrator/Library/Application Support/iron-link';
const _macSock = '$_macRoot/iron-link.sock';
const _macEnv = {'IRON_LINK_CONFIG_DIR': _macRoot, 'IRON_LINK_SOCKET': _macSock};

Future<ServiceReport> _mac(ProcessResult print, {bool plist = true}) =>
    probeDaemonService(
        host: HostOs.macos,
        run: _Runner({_launchctlPrint: print}).call,
        environment: _macEnv,
        exists: (p) => plist && p == _plist);

// ---- Windows ----

const _sc = r'C:\Windows\System32\sc.exe';
const _scQuery = '$_sc query iron-link';
const _pipe = r'\\.\pipe\iron-link';
const _winEnv = {'SystemRoot': r'C:\Windows', 'IRON_LINK_SOCKET': _pipe};

// `sc.exe query iron-link` output (CRLF; two spaces between code and word).
const _scStopped = '\r\n'
    'SERVICE_NAME: iron-link\r\n'
    '        TYPE               : 10  WIN32_OWN_PROCESS\r\n'
    '        STATE              : 1  STOPPED\r\n'
    '        WIN32_EXIT_CODE    : 1077  (0x435)\r\n'
    '        SERVICE_EXIT_CODE  : 0  (0x0)\r\n'
    '        CHECKPOINT         : 0x0\r\n'
    '        WAIT_HINT          : 0x0\r\n';
const _scRunningRu = '\r\n'
    'Имя_службы: iron-link\r\n'
    '        Тип                : 10  WIN32_OWN_PROCESS\r\n'
    '        Состояние          : 4  RUNNING\r\n'
    '                                (STOPPABLE, NOT_PAUSABLE, ACCEPTS_SHUTDOWN)\r\n'
    '        Код_выхода_Win32   : 0  (0x0)\r\n'
    '        Код_выхода_службы  : 0  (0x0)\r\n'
    '        Контрольная_точка  : 0x0\r\n'
    '        Ожидание           : 0x0\r\n';
const _scStartPending = '\r\n'
    'SERVICE_NAME: iron-link\r\n'
    '        TYPE               : 10  WIN32_OWN_PROCESS\r\n'
    '        STATE              : 2  START_PENDING\r\n'
    '                                (NOT_STOPPABLE, NOT_PAUSABLE, IGNORES_SHUTDOWN)\r\n'
    '        WIN32_EXIT_CODE    : 0  (0x0)\r\n'
    '        SERVICE_EXIT_CODE  : 0  (0x0)\r\n'
    '        CHECKPOINT         : 0x0\r\n'
    '        WAIT_HINT          : 0x7d0\r\n';
const _scNotInstalled =
    '[SC] EnumQueryServicesStatus:OpenService FAILED 1060:\r\n'
    '\r\n'
    'The specified service does not exist as an installed service.\r\n'
    '\r\n';

Future<ServiceReport> _win(ProcessResult query,
        {bool sc = true, bool daemonExe = true}) =>
    probeDaemonService(
        host: HostOs.windows,
        run: _Runner({_scQuery: query}).call,
        environment: _winEnv,
        exists: (p) =>
            (sc && p == _sc) || (daemonExe && p.endsWith('iron-link-daemon.exe')));

void main() {
  group('probe: Linux (systemd)', () {
    test('not-found → not installed, no button', () async {
      final rep = await _linux(_sd(load: 'not-found', active: 'inactive', sub: 'dead'));
      expect(rep.state, ServiceState.notInstalled);
      expect(rep.canStart, isFalse);
      expect(rep.detail,
          'No iron-link service is installed. Start the daemon by hand: sudo iron-link-daemon');
    });

    test('masked → stopped, no button', () async {
      final rep = await _linux(_sd(load: 'masked', active: 'inactive', sub: 'dead'));
      expect(rep.state, ServiceState.stopped);
      expect(rep.canStart, isFalse);
      expect(rep.detail,
          'The iron-link service is masked. Unmask it: sudo systemctl unmask iron-link');
    });

    test('bad-setting / error → unknown, names the load state', () async {
      var rep = await _linux(_sd(load: 'bad-setting', active: 'inactive', sub: 'dead'));
      expect(rep.state, ServiceState.unknown);
      expect(rep.detail,
          'The iron-link unit file failed to load (bad-setting). See: systemctl status iron-link');
      rep = await _linux(_sd(load: 'error', active: 'inactive', sub: 'dead'));
      expect(rep.detail, startsWith('The iron-link unit file failed to load (error).'));
    });

    test('inactive, clean → stopped with Start', () async {
      final rep = await _linux(_sdStopped);
      expect(rep.state, ServiceState.stopped);
      expect(rep.canStart, isTrue);
      expect(rep.detail, 'The iron-link service is stopped.');
    });

    test('failed with a result → stopped with Start and the last result',
        () async {
      final rep = await _linux(_sd(
          active: 'failed', sub: 'failed', result: 'start-limit-hit', restarts: '5'));
      expect(rep.state, ServiceState.stopped);
      expect(rep.canStart, isTrue);
      expect(rep.detail,
          'The iron-link service is stopped. Last run ended with start-limit-hit after 5 restarts.');
    });

    test('auto-restart (queued too) → starting, no button', () async {
      for (final sub in ['auto-restart', 'auto-restart-queued']) {
        final rep = await _linux(_sd(
            active: 'activating', sub: sub, result: 'exit-code', restarts: '3'));
        expect(rep.state, ServiceState.starting);
        expect(rep.canStart, isFalse);
        expect(rep.detail,
            'The iron-link service keeps restarting (3 restarts, last result exit-code). See: journalctl -u iron-link');
      }
    });

    test('plain activating / reloading → starting', () async {
      var rep = await _linux(_sd(active: 'activating', sub: 'start'));
      expect(rep.state, ServiceState.starting);
      expect(rep.detail, 'The iron-link service is starting.');
      rep = await _linux(_sd(active: 'reloading', sub: 'reload'));
      expect(rep.detail, 'The iron-link service is starting.');
    });

    test('deactivating → stopping, no button', () async {
      final rep = await _linux(_sd(active: 'deactivating', sub: 'stop-sigterm'));
      expect(rep.state, ServiceState.stopped);
      expect(rep.canStart, isFalse);
      expect(rep.detail, 'The iron-link service is stopping.');
    });

    test('active: the cause tells the running-but-unreachable cases apart',
        () async {
      const eacces = SocketException('connect', osError: OSError('', 13));
      const refused = SocketException('connect', osError: OSError('', 111));
      var rep = await _linux(_sdRunning, cause: eacces);
      expect(rep.state, ServiceState.running);
      expect(rep.canStart, isFalse);
      expect(rep.detail,
          'The service is running but this user cannot open its socket. Add yourself to the iron-link group (sudo gpasswd -a \$USER iron-link) and log in again.');

      rep = await _linux(_sdRunning, cause: refused);
      expect(rep.detail,
          'A stale socket file at $_linuxSock shadows the running service. Remove it: rm $_linuxSock');

      rep = await _linux(_sdRunning, cause: TimeoutException('connect'));
      expect(rep.detail,
          'The service is running but $_linuxSock is unreachable. See: journalctl -u iron-link');
      rep = await _linux(_sdRunning);
      expect(rep.detail,
          'The service is running but $_linuxSock is unreachable. See: journalctl -u iron-link');
    });

    test('systemctl missing → unknown with a fallback', () async {
      final rep = await probeDaemonService(
          host: HostOs.linux, run: _Runner({}).call, environment: _linuxEnv);
      expect(rep.state, ServiceState.unknown);
      expect(rep.detail,
          'Cannot query the iron-link service. See: systemctl status iron-link');
    });

    // A widget test only for its fake clock: the 5 s probe timeout elapses
    // under pump() instead of in real time.
    testWidgets('a hung probe → unknown after the timeout', (tester) async {
      final runner = _Runner({})..hang = true;
      final report = probeDaemonService(
          host: HostOs.linux, run: runner.call, environment: _linuxEnv);
      await tester.pump(const Duration(seconds: 6));
      expect((await report).state, ServiceState.unknown);
    });
  });

  group('start: Linux (systemd)', () {
    Future<StartResult> start(ProcessResult r) =>
        startDaemonService(host: HostOs.linux, run: _Runner({_systemctlStart: r}).call);

    test('exit 0 → launched', () async {
      expect(await start(_r(0, '')), (StartOutcome.launched, ''));
    });

    test('a dismissed polkit dialog → cancelled', () async {
      expect(
          await start(_r(1, '', 'Failed to start iron-link.service: Access denied\n')),
          (StartOutcome.cancelled, ''));
    });

    test('no authentication agent → failed with the sudo fallback', () async {
      const want = (
        StartOutcome.failed,
        'No authentication agent is available. Run: sudo systemctl start iron-link'
      );
      expect(
          await start(_r(1, '',
              'Failed to start iron-link.service: Interactive authentication required.\n')),
          want);
      expect(
          await start(_r(1, '',
              'Failed to start iron-link.service: org.freedesktop.DBus.Error.InteractiveAuthorizationRequired\n')),
          want);
    });

    test('any other error → failed with systemctl\'s text', () async {
      expect(
          await start(_r(1, '', 'Failed to start iron-link.service: Unit iron-link.service not found.\n')),
          (StartOutcome.failed, 'Failed to start iron-link.service: Unit iron-link.service not found.'));
    });

    test('systemctl missing → failed', () async {
      expect(await startDaemonService(host: HostOs.linux, run: _Runner({}).call),
          (StartOutcome.failed, 'Cannot run systemctl. Run: sudo systemctl start iron-link'));
    });
  });

  group('probe: macOS (launchd)', () {
    test('rc 113 with the plist present → stopped with Start', () async {
      final rep = await _mac(_r(113, '', _lcNotLoadedErr));
      expect(rep.state, ServiceState.stopped);
      expect(rep.canStart, isTrue);
      expect(rep.detail, 'The daemon is installed but not loaded in launchd.');
    });

    test('rc 113 without the plist → not installed', () async {
      final rep = await _mac(_r(113, '', _lcNotLoadedErr), plist: false);
      expect(rep.state, ServiceState.notInstalled);
      expect(rep.canStart, isFalse);
      expect(rep.detail,
          'The daemon is not installed. Run install.sh from the downloaded bundle.');
    });

    test('running: the nested coalition state lines are ignored', () async {
      final rep = await _mac(_r(0, _lcRunning));
      expect(rep.state, ServiceState.running);
      expect(rep.canStart, isFalse);
      expect(rep.detail,
          'launchd reports the daemon running (pid 10526) but $_macSock is unreachable. See: $_macRoot/daemon.log');
    });

    test('not running → stopped with Start and the exit code', () async {
      final rep = await _mac(_r(0, _lcNotRunning));
      expect(rep.state, ServiceState.stopped);
      expect(rep.canStart, isTrue);
      expect(rep.detail,
          'The daemon is loaded but not running (last exit code 1).');
    });

    test('spawn scheduled / EX_CONFIG → missing binary, no button', () async {
      // Together, and each on its own.
      for (final out in [
        _lcSpawnScheduled,
        _lcNotRunning.replaceFirst('last exit code = 1', 'last exit code = 78: EX_CONFIG'),
        _lcNotRunning.replaceFirst('state = not running', 'state = spawn scheduled'),
      ]) {
        final rep = await _mac(_r(0, out));
        expect(rep.state, ServiceState.stopped);
        expect(rep.canStart, isFalse);
        expect(rep.detail,
            'The daemon binary is missing at /usr/local/bin/iron-link-daemon. Run install.sh again.');
      }
    });

    test('a penalty-boxed service is the missing binary too', () async {
      final rep = await _mac(_r(
          0,
          _lcNotRunning.replaceFirst('inferred program\n',
              'inferred program | penalty box\n')));
      expect(rep.canStart, isFalse);
      expect(rep.detail, startsWith('The daemon binary is missing at'));
    });

    test('an unexpected state → unknown with the raw state', () async {
      final rep = await _mac(_r(
          0, _lcRunning.replaceFirst('\tstate = running\n', '\tstate = waiting\n')));
      expect(rep.state, ServiceState.unknown);
      expect(rep.detail, 'launchd reports the daemon as "waiting".');
    });

    test('launchctl missing or failing → unknown with a fallback', () async {
      var rep = await probeDaemonService(
          host: HostOs.macos, run: _Runner({}).call, environment: _macEnv);
      expect(rep.state, ServiceState.unknown);
      expect(rep.detail,
          'Cannot query launchd. See: launchctl print system/org.iron-link.daemon');
      rep = await _mac(_r(1, '', 'Unrecognized target specifier.\n'));
      expect(rep.state, ServiceState.unknown);
      expect(rep.detail,
          'Cannot query launchd (launchctl print exited 1). See: launchctl print system/org.iron-link.daemon');
    });
  });

  group('start: macOS (osascript)', () {
    test('one osascript call, the script as one argv element', () async {
      final runner = _Runner({_osascript: _r(0, '')});
      expect(await startDaemonService(host: HostOs.macos, run: runner.call),
          (StartOutcome.launched, ''));
      expect(runner.calls, [_osascript]);
    });

    Future<StartResult> start(String stderr) => startDaemonService(
        host: HostOs.macos, run: _Runner({_osascript: _r(1, '', stderr)}).call);

    test('-128 → cancelled', () async {
      expect(await start('0:271: execution error: User canceled. (-128)\n'),
          (StartOutcome.cancelled, ''));
    });

    test('no dialog possible → failed with the Terminal fallback', () async {
      const want = (
        StartOutcome.failed,
        'No admin dialog is available. Run in Terminal: sudo launchctl bootstrap system /Library/LaunchDaemons/org.iron-link.daemon.plist'
      );
      expect(
          await start('0:271: execution error: No user interaction allowed. (-1713)\n'),
          want);
      expect(
          await start('0:271: execution error: No user interaction allowed. (-60007)\n'),
          want);
    });

    test('any other error → failed with its message', () async {
      expect(
          await start(
              '0:271: execution error: Bootstrap failed: 5: Input/output error (1)\n'),
          (StartOutcome.failed, 'Bootstrap failed: 5: Input/output error'));
    });

    test('osascript missing → failed', () async {
      expect(await startDaemonService(host: HostOs.macos, run: _Runner({}).call), (
        StartOutcome.failed,
        'Cannot run osascript. Run in Terminal: sudo launchctl bootstrap system /Library/LaunchDaemons/org.iron-link.daemon.plist'
      ));
    });
  });

  group('probe: Windows (sc.exe)', () {
    test('no sc.exe → unknown with the elevated-prompt fallback', () async {
      final rep = await _win(_r(0, _scStopped), sc: false);
      expect(rep.state, ServiceState.unknown);
      expect(rep.detail,
          'Cannot query the iron-link service (sc.exe not found). Start it from an elevated prompt: sc start iron-link');
    });

    test('sc.exe failing to run → unknown with the elevated-prompt fallback',
        () async {
      final rep = await probeDaemonService(
          host: HostOs.windows,
          run: _Runner({}).call,
          environment: _winEnv,
          exists: (_) => true);
      expect(rep.state, ServiceState.unknown);
      expect(rep.detail,
          'Cannot query the iron-link service. Start it from an elevated prompt: sc start iron-link');
    });

    test('exit 1060 → not installed', () async {
      final rep = await _win(_r(1060, _scNotInstalled));
      expect(rep.state, ServiceState.notInstalled);
      expect(rep.canStart, isFalse);
      expect(rep.detail,
          'The iron-link service is not installed. Run the installer again.');
      // Old builds exited 0 and only printed the failure.
      expect((await _win(_r(0, _scNotInstalled))).state,
          ServiceState.notInstalled);
    });

    test('exit 5 → unknown (access denied)', () async {
      final rep = await _win(_r(5, ''));
      expect(rep.state, ServiceState.unknown);
      expect(rep.detail, 'Cannot query the iron-link service (access denied).');
    });

    test('STOPPED with the daemon exe in place → stopped with Start',
        () async {
      final rep = await _win(_r(0, _scStopped));
      expect(rep.state, ServiceState.stopped);
      expect(rep.canStart, isTrue);
      expect(rep.detail,
          'The iron-link service is stopped. It may have missed its start window at boot.');
    });

    test('STOPPED without the daemon exe → no button, names the path',
        () async {
      final rep = await _win(_r(0, _scStopped), daemonExe: false);
      expect(rep.state, ServiceState.stopped);
      expect(rep.canStart, isFalse);
      expect(rep.detail, startsWith('The daemon binary is missing at '));
      expect(rep.detail, contains('iron-link-daemon.exe. An antivirus may'));
    });

    test('RUNNING on a Russian locale → running, no button', () async {
      final rep = await _win(_r(0, _scRunningRu));
      expect(rep.state, ServiceState.running);
      expect(rep.canStart, isFalse);
      expect(rep.detail,
          'The service is running but the pipe $_pipe is unreachable. See: %APPDATA%\\iron-link\\daemon.log');
    });

    test('START_PENDING / CONTINUE_PENDING → starting; STOP_PENDING → stopping',
        () async {
      var rep = await _win(_r(0, _scStartPending));
      expect(rep.state, ServiceState.starting);
      expect(rep.detail, 'The iron-link service is starting.');
      rep = await _win(_r(
          0, _scStartPending.replaceFirst('2  START_PENDING', '5  CONTINUE_PENDING')));
      expect(rep.state, ServiceState.starting);
      expect(rep.detail, 'The iron-link service is starting.');
      rep = await _win(
          _r(0, _scStartPending.replaceFirst('2  START_PENDING', '3  STOP_PENDING')));
      expect(rep.state, ServiceState.stopped);
      expect(rep.canStart, isFalse);
      expect(rep.detail, 'The iron-link service is stopping.');
    });

    test('PAUSED → unknown with the keyword', () async {
      final rep = await _win(_r(
          0, _scStopped.replaceFirst('1  STOPPED', '7  PAUSED')));
      expect(rep.state, ServiceState.unknown);
      expect(rep.detail, 'The iron-link service is PAUSED.');
    });
  });

  group('start: Windows (ShellExecute runas)', () {
    test('elevates sc.exe start; the return code maps to the outcome',
        () async {
      final calls = <String>[];
      Future<StartResult> start(int code) => startDaemonService(
          host: HostOs.windows,
          runAs: (exe, args) {
            calls.add('$exe|$args');
            return code;
          });
      expect(await start(42), (StartOutcome.launched, ''));
      expect(await start(5), (StartOutcome.cancelled, ''));
      expect(await start(2), (
        StartOutcome.failed,
        'Cannot start the service (ShellExecute code 2). Start it from an elevated prompt: sc start iron-link'
      ));
      // The root is the host's SystemRoot (or C:\Windows without one).
      expect(calls, hasLength(3));
      expect(calls, everyElement(endsWith(r'\System32\sc.exe|start iron-link')));
    });
  });
}
