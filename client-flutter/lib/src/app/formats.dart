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

/// Trims an RFC 3339 timestamp to a local `YYYY-MM-DD HH:MM` for lists. A
/// zero stamp (year 1, the daemon's "never" before it learned to send an
/// empty one) reads as an em-dash too.
String formatTimestamp(String? rfc3339) {
  if (rfc3339 == null || rfc3339.isEmpty) return '—';
  final parsed = DateTime.tryParse(rfc3339);
  if (parsed == null) return rfc3339;
  if (parsed.year < 1971) return '—';
  final local = parsed.toLocal();
  String two(int v) => v.toString().padLeft(2, '0');
  return '${local.year}-${two(local.month)}-${two(local.day)} '
      '${two(local.hour)}:${two(local.minute)}';
}

/// How long ago an RFC 3339 timestamp was, coarsely: `just now` / `5 min ago`
/// / `3 h ago` / `2 d ago`; `never` for an absent or empty stamp, and for a
/// zero stamp (year 1, the daemon's "never" before it learned to send an
/// empty one). A stamp the parser rejects is shown verbatim. A stamp ahead of
/// [now] (clock skew) reads as `just now` rather than a negative age.
String formatRelativeTime(String? rfc3339, {DateTime? now}) {
  if (rfc3339 == null || rfc3339.isEmpty) return 'never';
  final parsed = DateTime.tryParse(rfc3339);
  if (parsed == null) return rfc3339;
  if (parsed.year < 1971) return 'never';
  final age = (now ?? DateTime.now()).toUtc().difference(parsed.toUtc());
  if (age.inMinutes < 1) return 'just now';
  if (age.inHours < 1) return '${age.inMinutes} min ago';
  if (age.inDays < 1) return '${age.inHours} h ago';
  return '${age.inDays} d ago';
}

/// A refresh interval in seconds as a short label: whole days above one day
/// (`7 d`), whole hours as `8 h` (a day is `24 h`, matching the preset it
/// is offered as), mixed as `2 h 30 min`, and anything under an hour in
/// minutes (`10 min`, with the odd seconds appended as `10 min 30 s`).
String formatInterval(int secs) {
  if (secs > 86400 && secs % 86400 == 0) return '${secs ~/ 86400} d';
  final h = secs ~/ 3600;
  final m = (secs % 3600) ~/ 60;
  final s = secs % 60;
  if (h > 0) {
    if (m == 0 && s == 0) return '$h h';
    return s == 0 ? '$h h $m min' : '$h h $m min $s s';
  }
  if (s == 0) return '$m min';
  return m == 0 ? '$s s' : '$m min $s s';
}
