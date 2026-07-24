/// Small display formatters shared by the pages.
library;

/// `1023 B` / `4.0 KiB` / `2.8 MiB` / `1.2 GiB`.
String formatBytes(int bytes) {
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  var value = bytes.toDouble();
  var unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  return unit == 0 ? '$bytes ${units[0]}' : '${value.toStringAsFixed(1)} ${units[unit]}';
}

/// Bytes-per-second with the same scaling.
String formatRate(int bytesPerSec) => '${formatBytes(bytesPerSec)}/s';

/// `42s` / `5m 03s` / `2h 11m`.
String formatUptime(int secs) {
  if (secs < 60) return '${secs}s';
  final m = secs ~/ 60;
  if (m < 60) return '${m}m ${(secs % 60).toString().padLeft(2, '0')}s';
  return '${m ~/ 60}h ${(m % 60).toString().padLeft(2, '0')}m';
}

/// `58 ms`, or an em-dash for a failed probe.
String formatLatency(int? ms) => ms == null ? '—' : '$ms ms';

/// Trims an RFC 3339 timestamp to a local `YYYY-MM-DD HH:MM` for lists.
String formatTimestamp(String? rfc3339) {
  if (rfc3339 == null || rfc3339.isEmpty) return '—';
  final parsed = DateTime.tryParse(rfc3339);
  if (parsed == null) return rfc3339;
  final local = parsed.toLocal();
  String two(int v) => v.toString().padLeft(2, '0');
  return '${local.year}-${two(local.month)}-${two(local.day)} '
      '${two(local.hour)}:${two(local.minute)}';
}
