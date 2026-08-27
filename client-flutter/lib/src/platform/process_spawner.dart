/// The `Process.start` seam the platform launch helpers spawn through —
/// injected in tests so an exact argv can be asserted without forking.
library;

import 'dart:io';

typedef ProcessSpawner = Future<Process> Function(
    String executable, List<String> arguments,
    {ProcessStartMode mode});
