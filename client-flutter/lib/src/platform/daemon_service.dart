/// The service-manager side of the daemon-down banner: what the OS says about
/// the daemon service when the client cannot reach the daemon
/// ([probeDaemonService]), and the one elevated Start that fixes the common
/// case of an installed-but-stopped service ([startDaemonService]). The app
/// stays unprivileged; a Start raises one consent dialog per click — polkit
/// on Linux, the administrator dialog on macOS, UAC on Windows.
///
/// Every probe is one short subprocess any user may run (`systemctl show`,
/// `launchctl print`, `sc.exe query`); its output is parsed into a
/// [ServiceReport] whose [ServiceReport.detail] is the banner text, worded
/// here so the widget renders it as is.
library;

import 'dart:async';
import 'dart:io';

import '../ipc/endpoint.dart';
import 'open_url.dart';
import 'process_spawner.dart';
import 'shell_execute_windows.dart';

enum ServiceState { notInstalled, stopped, starting, running, unknown }

/// What the service manager reports, already worded for the banner.
class ServiceReport {
  const ServiceReport(this.state, this.detail, {this.canStart = false});

  final ServiceState state;

  /// The explanation and, where there is one, the manual remedy.
  final String detail;

  /// Whether an elevated [startDaemonService] is the fix (the Start button).
  final bool canStart;
}

enum StartOutcome { launched, cancelled, failed }

/// A [startDaemonService] result; [message] is set for [StartOutcome.failed]
/// only.
typedef StartResult = (StartOutcome outcome, String message);

/// A probe answers in milliseconds; a wedged service manager must not hold
/// the banner.
const _probeTimeout = Duration(seconds: 5);

/// Runs one command. Null when it could not be started or, under a
/// [timeout], did not finish in time. A Start runs without one: its consent
/// dialog owns the wait, and [ProcessRunner] hands back no handle to kill
/// the child with — a deadline would only orphan the dialog behind a
/// failure message and let a second click open another.
Future<ProcessResult?> _run(ProcessRunner run, String exe, List<String> args,
    {Duration? timeout}) async {
  try {
    final result = run(exe, args);
    return await (timeout == null ? result : result.timeout(timeout));
  } on ProcessException {
    return null;
  } on TimeoutException {
    return null;
  }
}

/// Asks the host's service manager about the daemon service. [cause] is the
/// connect failure the client saw (a SocketException, a Windows PipeError, a
/// TimeoutException) — it tells the running-but-unreachable cases apart. The
/// endpoint the report names is resolved the way the client resolves it,
/// through [environment] / [exists]; [run] is the subprocess seam.
Future<ServiceReport> probeDaemonService({
  HostOs? host,
  ProcessRunner run = Process.run,
  Map<String, String>? environment,
  bool Function(String)? exists,
  Object? cause,
}) async {
  final ep = Endpoint(environment: environment, exists: exists);
  final has = exists ?? ((p) => File(p).existsSync());
  return switch (host ?? HostOs.current) {
    HostOs.linux => await _probeSystemd(run, ep.socketPath(), cause),
    HostOs.macos =>
      await _probeLaunchd(run, has, ep.socketPath(), ep.configRoot()),
    HostOs.windows => await _probeScm(
        run, environment ?? Platform.environment, has, ep.socketPath()),
    HostOs.other => const ServiceReport(
        ServiceState.unknown, 'No service manager to ask on this platform.'),
  };
}

/// Starts the daemon service behind one elevation dialog: `systemctl start`
/// on Linux (polkit's agent asks), an osascript `with administrator
/// privileges` on macOS, `sc.exe start` through UAC on Windows ([runAs], see
/// [shellExecuteRunAs]). [StartOutcome.launched] means the command was
/// accepted, not that the daemon is up — the caller re-probes.
Future<StartResult> startDaemonService({
  HostOs? host,
  ProcessRunner run = Process.run,
  RunAs runAs = shellExecuteRunAs,
}) async {
  return switch (host ?? HostOs.current) {
    HostOs.linux => await _startSystemd(run),
    HostOs.macos => await _startLaunchd(run),
    HostOs.windows => await _startScm(runAs),
    // The probe never offers Start here.
    HostOs.other => throw UnsupportedError('no service manager on this platform'),
  };
}

// ---- Linux: systemd ----

/// `systemctl show` answers KEY=VALUE lines (locale-proof; exit 0 even for a
/// unit that does not exist — that is LoadState=not-found) in systemd's own
/// property order, so the lines go into a map. `is-active` is useless here:
/// it prints `inactive` for a missing unit too.
final _systemdField = RegExp(r'^([^=]+)=(.*)$', multiLine: true);

Future<ServiceReport> _probeSystemd(
    ProcessRunner run, String endpoint, Object? cause) async {
  final r = await _run(
      run,
      'systemctl',
      [
        'show',
        '--no-pager',
        '-p',
        'LoadState,ActiveState,SubState,Result,NRestarts',
        'iron-link.service',
      ],
      timeout: _probeTimeout);
  if (r == null || r.exitCode != 0) {
    return const ServiceReport(ServiceState.unknown,
        'Cannot query the iron-link service. See: systemctl status iron-link');
  }
  final p = {
    for (final m in _systemdField.allMatches(r.stdout as String)) m[1]!: m[2]!
  };
  final load = p['LoadState'] ?? 'unknown';
  final active = p['ActiveState'] ?? 'unknown';
  final result = p['Result'] ?? 'success';
  final restarts = p['NRestarts'] ?? '0';
  switch (load) {
    case 'not-found':
      // No button: pkexec would break the daemon's SUDO_UID path resolution.
      return const ServiceReport(ServiceState.notInstalled,
          'No iron-link service is installed. Start the daemon by hand: sudo iron-link-daemon');
    case 'masked':
      return const ServiceReport(ServiceState.stopped,
          'The iron-link service is masked. Unmask it: sudo systemctl unmask iron-link');
    case 'loaded':
      break;
    default:
      return ServiceReport(ServiceState.unknown,
          'The iron-link unit file failed to load ($load). See: systemctl status iron-link');
  }
  switch (active) {
    case 'inactive' || 'failed':
      final last = result == 'success'
          ? ''
          : ' Last run ended with $result after $restarts restarts.';
      return ServiceReport(
          ServiceState.stopped, 'The iron-link service is stopped.$last',
          canStart: true);
    case 'activating' || 'reloading':
      return (p['SubState'] ?? '').startsWith('auto-restart')
          ? ServiceReport(ServiceState.starting,
              'The iron-link service keeps restarting ($restarts restarts, last result $result). See: journalctl -u iron-link')
          : const ServiceReport(
              ServiceState.starting, 'The iron-link service is starting.');
    case 'active':
      // The raw errno behind the connect failure: 13 EACCES, 111 ECONNREFUSED.
      return ServiceReport(
          ServiceState.running,
          switch (cause is SocketException ? cause.osError?.errorCode : null) {
            13 =>
              'The service is running but this user cannot open its socket. Add yourself to the iron-link group (sudo gpasswd -a \$USER iron-link) and log in again.',
            111 =>
              'A stale socket file at $endpoint shadows the running service. Remove it: rm $endpoint',
            _ =>
              'The service is running but $endpoint is unreachable. See: journalctl -u iron-link',
          });
    case 'deactivating':
      return const ServiceReport(
          ServiceState.stopped, 'The iron-link service is stopping.');
    default:
      return ServiceReport(ServiceState.unknown,
          'The iron-link service is in state $active. See: systemctl status iron-link');
  }
}

/// `systemctl start` asks polkit itself (interactive authorization is its
/// default), so the session's agent shows the password dialog; no polkit
/// rule is needed. No `reset-failed` first: the 10 s start-limit window has
/// long passed by the time a human clicks, and a second command would prompt
/// a second time.
Future<StartResult> _startSystemd(ProcessRunner run) async {
  final r = await _run(run, 'systemctl', ['start', 'iron-link.service']);
  if (r == null) {
    return (
      StartOutcome.failed,
      'Cannot run systemctl. Run: sudo systemctl start iron-link'
    );
  }
  if (r.exitCode == 0) return (StartOutcome.launched, '');
  final err = (r.stderr as String).trim();
  // A dismissed or failed polkit dialog is "Access denied".
  if (err.contains('Access denied')) return (StartOutcome.cancelled, '');
  // No agent: "Interactive authentication required." (systemctl's wording of
  // org.freedesktop.DBus.Error.InteractiveAuthorizationRequired).
  final low = err.toLowerCase();
  if (low.contains('interactive authentication') ||
      low.contains('interactiveauthorizationrequired')) {
    return (
      StartOutcome.failed,
      'No authentication agent is available. Run: sudo systemctl start iron-link'
    );
  }
  return (
    StartOutcome.failed,
    err.isEmpty ? 'systemctl start exited ${r.exitCode}' : err
  );
}

// ---- macOS: launchd ----

const _launchdPlist = '/Library/LaunchDaemons/org.iron-link.daemon.plist';

/// The top-level fields of `launchctl print system/<label>`: exactly one
/// leading tab. Nested blocks (the coalitions) carry their own
/// `\t\tstate = active` and must not be read as the service's.
final _launchdField = RegExp(r'^\t([^\t]+?) = (.*)$', multiLine: true);

/// `launchctl print` is unprivileged and answers in ~10 ms; a service that is
/// not bootstrapped is rc 113 ("Could not find service … in domain for
/// system").
Future<ServiceReport> _probeLaunchd(ProcessRunner run,
    bool Function(String) exists, String endpoint, String configRoot) async {
  final r = await _run(run, 'launchctl', ['print', 'system/org.iron-link.daemon'],
      timeout: _probeTimeout);
  if (r == null) {
    return const ServiceReport(ServiceState.unknown,
        'Cannot query launchd. See: launchctl print system/org.iron-link.daemon');
  }
  if (r.exitCode == 113) {
    return exists(_launchdPlist)
        ? const ServiceReport(ServiceState.stopped,
            'The daemon is installed but not loaded in launchd.',
            canStart: true)
        : const ServiceReport(ServiceState.notInstalled,
            'The daemon is not installed. Run install.sh from the downloaded bundle.');
  }
  if (r.exitCode != 0) {
    return ServiceReport(ServiceState.unknown,
        'Cannot query launchd (launchctl print exited ${r.exitCode}). See: launchctl print system/org.iron-link.daemon');
  }
  final p = {
    for (final m in _launchdField.allMatches(r.stdout as String)) m[1]!: m[2]!
  };
  final state = p['state'] ?? '';
  final exit = p['last exit code'] ?? '';
  if (state == 'spawn scheduled' ||
      (p['properties'] ?? '').contains('penalty box') ||
      exit.startsWith('78')) {
    // launchd cannot exec the program (EX_CONFIG). No button: a kickstart of
    // a penalty-boxed service blocks forever.
    return ServiceReport(ServiceState.stopped,
        'The daemon binary is missing at ${p['program'] ?? '/usr/local/bin/iron-link-daemon'}. Run install.sh again.');
  }
  return switch (state) {
    'running' => ServiceReport(ServiceState.running,
        'launchd reports the daemon running (pid ${p['pid'] ?? '?'}) but $endpoint is unreachable. See: $configRoot/daemon.log'),
    'not running' => ServiceReport(ServiceState.stopped,
        'The daemon is loaded but not running (last exit code $exit).',
        canStart: true),
    _ => ServiceReport(
        ServiceState.unknown, 'launchd reports the daemon as "$state".'),
  };
}

/// One administrator dialog covers both states: a loaded service is
/// kickstarted, an unloaded one is bootstrapped from its plist. Passed as
/// argv, not through a shell. The dialog is titled "osascript" (an
/// in-process NSAppleScript would name the app; a later refinement). Release
/// builds have no App Sandbox, so the subprocess is allowed; a Debug build is
/// sandboxed and cannot run it.
const _launchdStartScript =
    'do shell script "launchctl print system/org.iron-link.daemon >/dev/null 2>&1 && launchctl kickstart -k system/org.iron-link.daemon || launchctl bootstrap system /Library/LaunchDaemons/org.iron-link.daemon.plist" with prompt "iron-link needs to start its background daemon." with administrator privileges';

const _launchdManual =
    'Run in Terminal: sudo launchctl bootstrap system /Library/LaunchDaemons/org.iron-link.daemon.plist';

/// osascript exits 1 on any error and prints
/// `<a>:<b>: execution error: <message> (<number>)`.
final _osascriptError = RegExp(r'execution error: (.*) \((-?\d+)\)$');

Future<StartResult> _startLaunchd(ProcessRunner run) async {
  final r = await _run(run, '/usr/bin/osascript', ['-e', _launchdStartScript]);
  if (r == null) {
    return (StartOutcome.failed, 'Cannot run osascript. $_launchdManual');
  }
  if (r.exitCode == 0) return (StartOutcome.launched, '');
  final err = (r.stderr as String).trim();
  final m = _osascriptError.firstMatch(err);
  return switch (m?[2]) {
    '-128' => (StartOutcome.cancelled, ''), // User canceled.
    // No UI session / user interaction not allowed.
    '-60007' || '-1713' => (
        StartOutcome.failed,
        'No admin dialog is available. $_launchdManual'
      ),
    _ => (
        StartOutcome.failed,
        m?[1] ?? (err.isEmpty ? 'osascript exited ${r.exitCode}' : err)
      ),
  };
}

// ---- Windows: the service control manager ----

/// The state line of `sc.exe query`: `STATE : 4  RUNNING`. The label is
/// localized (and may arrive mis-decoded from the OEM code page), the
/// keyword is ASCII; two spaces separate code and keyword.
final _scmState = RegExp(
    r'\b(\d+)\s+(STOPPED|START_PENDING|STOP_PENDING|RUNNING|CONTINUE_PENDING|PAUSE_PENDING|PAUSED)\b');

String _scExe(Map<String, String> env) =>
    '${env['SystemRoot'] ?? r'C:\Windows'}\\System32\\sc.exe';

Future<ServiceReport> _probeScm(ProcessRunner run, Map<String, String> env,
    bool Function(String) exists, String endpoint) async {
  final sc = _scExe(env);
  if (!exists(sc)) {
    return const ServiceReport(ServiceState.unknown,
        'Cannot query the iron-link service (sc.exe not found). Start it from an elevated prompt: sc start iron-link');
  }
  final r = await _run(run, sc, ['query', 'iron-link'], timeout: _probeTimeout);
  if (r == null) {
    return const ServiceReport(ServiceState.unknown,
        'Cannot query the iron-link service. Start it from an elevated prompt: sc start iron-link');
  }
  final out = r.stdout as String;
  final m = _scmState.firstMatch(out);
  if (m == null) {
    // A missing service exits 1060; some old builds exited 0 and only
    // printed "OpenService FAILED 1060".
    if (r.exitCode == 1060 || out.contains('1060')) {
      return const ServiceReport(ServiceState.notInstalled,
          'The iron-link service is not installed. Run the installer again.');
    }
    return ServiceReport(
        ServiceState.unknown,
        r.exitCode == 5
            ? 'Cannot query the iron-link service (access denied).'
            : 'Cannot query the iron-link service (sc query exited ${r.exitCode}).');
  }
  switch (m[2]) {
    case 'STOPPED':
      // The installer's layout: {app}\app\<client>.exe, {app}\iron-link-daemon.exe.
      final exe =
          '${File(Platform.resolvedExecutable).parent.parent.path}${Platform.pathSeparator}iron-link-daemon.exe';
      return exists(exe)
          ? const ServiceReport(ServiceState.stopped,
              'The iron-link service is stopped. It may have missed its start window at boot.',
              canStart: true)
          : ServiceReport(ServiceState.stopped,
              'The daemon binary is missing at $exe. An antivirus may have quarantined it or an update was interrupted; restore it or run the installer again.');
    case 'START_PENDING' || 'CONTINUE_PENDING':
      return const ServiceReport(
          ServiceState.starting, 'The iron-link service is starting.');
    case 'STOP_PENDING':
      return const ServiceReport(
          ServiceState.stopped, 'The iron-link service is stopping.');
    case 'RUNNING':
      return ServiceReport(ServiceState.running,
          'The service is running but the pipe $endpoint is unreachable. See: %APPDATA%\\iron-link\\daemon.log');
    default:
      return ServiceReport(
          ServiceState.unknown, 'The iron-link service is ${m[2]}.');
  }
}

/// `sc.exe start` through UAC rather than the daemon exe itself: no argv
/// coupling to the daemon, and both are console programs anyway.
/// Fire-and-forget — the service manager reports the start, the re-probe
/// reads it.
Future<StartResult> _startScm(RunAs runAs) async {
  final code = await runAs(_scExe(Platform.environment), 'start iron-link');
  if (code > 32) return (StartOutcome.launched, '');
  // SE_ERR_ACCESSDENIED: the consent prompt was declined.
  if (code == 5) return (StartOutcome.cancelled, '');
  return (
    StartOutcome.failed,
    'Cannot start the service (ShellExecute code $code). Start it from an elevated prompt: sc start iron-link'
  );
}
