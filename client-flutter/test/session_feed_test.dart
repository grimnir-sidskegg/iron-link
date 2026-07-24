/// Unit tests for the log-feed message normalization: sing-box bakes the
/// level + the daemon uptime into every line, which we strip (the connection
/// id bracket is kept for colouring).
library;

import 'package:flutter_test/flutter_test.dart';
import 'package:iron_link_flutter/src/app/session.dart';

void main() {
  group('normalizeFeedMessage', () {
    test('strips level + uptime, keeps the connection-id bracket', () {
      expect(
          normalizeFeedMessage(
              'info', 'INFO[15905] [2273664267 134ms] dns: exchanged OPT'),
          '[2273664267 134ms] dns: exchanged OPT');
    });

    test('strips level + uptime when there is no id bracket', () {
      expect(normalizeFeedMessage('info', 'INFO[42] router: loaded rule-set'),
          'router: loaded rule-set');
    });

    test('handles a space-separated level', () {
      expect(normalizeFeedMessage('error', 'ERROR[7] failed to dial'),
          'failed to dial');
    });

    test('matches when the level is a longer spelling of the token', () {
      expect(normalizeFeedMessage('warning', 'WARN[3] slow resolver'),
          'slow resolver');
    });

    test('leaves a message that merely opens with a non-matching level word',
        () {
      expect(normalizeFeedMessage('info', 'Error parsing config'),
          'Error parsing config');
    });

    test('leaves an ordinary message whose first word is not the level', () {
      expect(normalizeFeedMessage('info', 'dns: exchanged'), 'dns: exchanged');
    });

    test('leaves our own bracketed feed lines (role/stage) intact', () {
      expect(normalizeFeedMessage('error', '[direct/connect] reset by peer'),
          '[direct/connect] reset by peer');
    });

    // sing-box >= v1.13 colourises the platform-writer line: the level wraps in
    // \x1B[37m…\x1B[0m and the connection id in \x1B[38;5;NNNm…\x1B[0m. The
    // escapes must be stripped so the prefix-strip + the connection-id colour
    // span still work; the message body (incl. the id) survives verbatim.
    test('strips ANSI colour codes from a v1.13 trace line', () {
      const esc = '\x1B';
      expect(
          normalizeFeedMessage(
              'trace',
              '$esc[37mTRACE$esc[0m[3831] [$esc[38;5;144m3804873600$esc[0m 4m46s] '
                  'connection: connection download closed'),
          '[3804873600 4m46s] connection: connection download closed');
    });

    test('strips ANSI from a coloured info line with no id bracket', () {
      const esc = '\x1B';
      expect(
          normalizeFeedMessage(
              'info', '$esc[36mINFO$esc[0m[42] router: loaded rule-set'),
          'router: loaded rule-set');
    });
  });
}
