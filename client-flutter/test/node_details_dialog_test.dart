/// Widget test for [NodeDetailsDialog] — the read-only node inspector. It must
/// render EVERY configured field, flattening nested security/transport params
/// to `dotted.path` rows so nothing (address, SNI, …) is hidden.
library;

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:iron_link_flutter/src/ui/dialogs.dart';

const _config = <String, Object?>{
  'name': 'Tokyo',
  'protocol': 'vless',
  'profile': {
    'server_name': 'Tokyo',
    'uuid': '00000000-0000-0000-0000-000000000001',
    'address': '198.51.100.7',
    'port': 443,
    'encryption': 'none',
    'security': {
      'kind': 'reality',
      'reality': {'sni': 'example.com', 'fp': 'chrome'},
    },
    'transport': {'kind': 'tcp'},
  },
};

void main() {
  testWidgets('flattens the config to labelled rows, nested fields included',
      (tester) async {
    await tester.pumpWidget(const MaterialApp(
      home: Scaffold(body: NodeDetailsDialog(config: _config)),
    ));

    // Title is the node name; protocol is surfaced up front.
    expect(find.text('Tokyo'), findsWidgets);
    expect(find.text('protocol'), findsOneWidget);

    // Top-level endpoint fields.
    expect(find.text('address'), findsOneWidget);
    expect(find.text('198.51.100.7'), findsOneWidget);
    expect(find.text('port'), findsOneWidget);
    expect(find.text('443'), findsOneWidget);

    // Nested security front is flattened to a dotted path — not hidden.
    expect(find.text('security.reality.sni'), findsOneWidget);
    expect(find.text('example.com'), findsOneWidget);
    expect(find.text('transport.kind'), findsOneWidget);

    expect(find.text('Close'), findsOneWidget);
  });

  testWidgets('an empty config shows a placeholder, not a crash',
      (tester) async {
    await tester.pumpWidget(const MaterialApp(
      home: Scaffold(body: NodeDetailsDialog(config: {})),
    ));
    expect(find.text('No fields to show.'), findsOneWidget);
  });
}
