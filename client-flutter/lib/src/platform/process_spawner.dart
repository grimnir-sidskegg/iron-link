/// The `Process.start` / `Process.run` seams the platform helpers go through
/// — injected in tests so an exact argv can be asserted without forking.
library;

import 'dart:io';

typedef ProcessSpawner = Future<Process> Function(
    String executable, List<String> arguments,
    {ProcessStartMode mode});

/// For the helpers that read a command's output (the service probes); a test
/// fake answers each argv with a canned [ProcessResult].
typedef ProcessRunner = Future<ProcessResult> Function(
    String executable, List<String> arguments,
    {Map<String, String>? environment, bool runInShell});
