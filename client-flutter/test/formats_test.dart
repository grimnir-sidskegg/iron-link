/// Unit tests for the display formatters the subscription editor relies on:
/// the coarse relative age of a refresh stamp and the interval labels.
library;

import 'package:flutter_test/flutter_test.dart';
import 'package:iron_link_flutter/src/app/formats.dart';

void main() {
  group('formatRelativeTime', () {
    final now = DateTime.utc(2026, 6, 10, 15, 0, 0);

    test('absent or empty stamp reads as never', () {
      expect(formatRelativeTime(null, now: now), 'never');
      expect(formatRelativeTime('', now: now), 'never');
    });

    test('a zero stamp reads as never, not as an age of centuries', () {
      expect(formatRelativeTime('0001-01-01T00:00:00Z', now: now), 'never');
    });

    test('coarse buckets: just now / min / h / d', () {
      expect(formatRelativeTime('2026-06-10T14:59:30Z', now: now), 'just now');
      expect(formatRelativeTime('2026-06-10T14:55:00Z', now: now), '5 min ago');
      expect(formatRelativeTime('2026-06-10T12:00:00Z', now: now), '3 h ago');
      expect(formatRelativeTime('2026-06-08T09:00:00Z', now: now), '2 d ago');
    });

    test('a stamp ahead of the clock is not a negative age', () {
      expect(formatRelativeTime('2026-06-10T16:00:00Z', now: now), 'just now');
    });

    test('an offset stamp is compared in absolute time', () {
      expect(
        formatRelativeTime('2026-06-10T14:00:00+02:00', now: now),
        '3 h ago',
      );
    });

    test('an unparseable stamp is shown verbatim', () {
      expect(formatRelativeTime('soon', now: now), 'soon');
    });
  });

  group('formatInterval', () {
    test('presets', () {
      expect(formatInterval(3600), '1 h');
      expect(formatInterval(14400), '4 h');
      expect(formatInterval(28800), '8 h');
      expect(formatInterval(43200), '12 h');
      expect(formatInterval(86400), '24 h');
      expect(formatInterval(604800), '7 d');
    });

    test('off-preset values', () {
      expect(formatInterval(9000), '2 h 30 min');
      expect(formatInterval(600), '10 min');
      expect(formatInterval(630), '10 min 30 s');
      expect(formatInterval(172800), '2 d');
      expect(formatInterval(90000), '25 h');
      expect(formatInterval(45), '45 s');
    });
  });
}
