/// Widget tests for [DiagnosisDialog]: the window must appear the instant the
/// action is taken — driven by the in-flight `diagnose` future — so a node
/// that runs its stages out to a timeout no longer leaves the screen blank
/// until the whole probe returns.
library;

import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:iron_link_flutter/src/ipc/client.dart';
import 'package:iron_link_flutter/src/ui/dialogs.dart';
import 'package:iron_link_flutter/src/wire/wire.dart';

Widget _host(Future<DiagnosisInfo> pending) => MaterialApp(
      home: Scaffold(
        body: DiagnosisDialog(node: 'tokyo', pending: pending),
      ),
    );

void main() {
  testWidgets('shows a spinner immediately while the probe is in flight',
      (tester) async {
    final probe = Completer<DiagnosisInfo>();
    await tester.pumpWidget(_host(probe.future));

    // Window is already on screen — before the daemon has answered.
    expect(find.text('Diagnosing tokyo'), findsOneWidget);
    expect(find.byType(CircularProgressIndicator), findsOneWidget);
    expect(find.text('Close'), findsOneWidget);

    // Verdict lands → the spinner is replaced by the staged report.
    probe.complete(const DiagnosisInfo(
      node: 'tokyo',
      ok: false,
      failedStage: 'handshake',
      stages: [
        StageInfo(stage: 'resolve', ok: true, durationMs: 12),
        StageInfo(
            stage: 'handshake', ok: false, durationMs: 5000, error: 'timeout'),
      ],
    ));
    await tester.pumpAndSettle();

    expect(find.byType(CircularProgressIndicator), findsNothing);
    expect(find.text('Diagnosis: tokyo'), findsOneWidget);
    expect(find.text('Failed at: handshake'), findsOneWidget);
    expect(find.text('handshake · 5000 ms'), findsOneWidget);
    expect(find.text('timeout'), findsOneWidget);
  });

  testWidgets('renders the daemon error inside the same window',
      (tester) async {
    final probe = Completer<DiagnosisInfo>();
    await tester.pumpWidget(_host(probe.future));
    expect(find.byType(CircularProgressIndicator), findsOneWidget);

    probe.completeError(const DaemonError('no such node'));
    await tester.pumpAndSettle();

    expect(find.byType(CircularProgressIndicator), findsNothing);
    expect(find.text('Diagnosis: tokyo'), findsOneWidget);
    expect(find.text('no such node'), findsOneWidget);
  });
}
