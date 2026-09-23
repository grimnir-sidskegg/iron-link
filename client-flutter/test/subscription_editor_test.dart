/// Widget tests for [promptEditSubscription] — the subscription editor with
/// the auto-update toggle, the refresh-interval presets and the last
/// refresh / last failure info rows. Opened from a stub button so the test
/// captures exactly what the dialog resolves to.
library;

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:iron_link_flutter/src/ui/dialogs.dart';
import 'package:iron_link_flutter/src/wire/wire.dart';

/// A fixed clock three hours after [healthy]'s stamp.
final now = DateTime.utc(2026, 6, 10, 15, 0, 0);

const healthy = SubscriptionInfo(
  id: 's1',
  name: 'main',
  url: 'https://example.com/sub',
  lastUpdated: '2026-06-10T12:00:00Z',
  nodeCount: 42,
  updateIntervalSec: 28800,
);

const failing = SubscriptionInfo(
  id: 's2',
  name: 'backup',
  url: 'https://example.com/sub2',
  enabled: false,
  lastUpdated: '2026-06-09T12:00:00Z',
  updateIntervalSec: 86400,
  lastError: 'subscription fetch: HTTP 503',
);

/// Pumps a page whose button opens the editor for [sub]; the resolved edit
/// (or null on cancel) lands in [results].
Future<void> _open(
  WidgetTester tester,
  SubscriptionInfo sub,
  List<SubscriptionEdit?> results,
) async {
  // Tall enough that the whole form is on screen for the taps.
  tester.view.physicalSize = const Size(1000, 1200);
  tester.view.devicePixelRatio = 1.0;
  addTearDown(tester.view.reset);
  await tester.pumpWidget(
    MaterialApp(
      home: Scaffold(
        body: Builder(
          builder: (context) => TextButton(
            onPressed: () async => results.add(
              await promptEditSubscription(context, sub, now: now),
            ),
            child: const Text('open'),
          ),
        ),
      ),
    ),
  );
  await tester.tap(find.text('open'));
  await tester.pumpAndSettle();
}

DropdownButtonFormField<int> _intervalField(WidgetTester tester) => tester
    .widget(find.byWidgetPredicate((w) => w is DropdownButtonFormField<int>));

void main() {
  testWidgets('shows the relative age, the toggle and the stored preset', (
    tester,
  ) async {
    final results = <SubscriptionEdit?>[];
    await _open(tester, healthy, results);

    expect(find.text('Auto-update'), findsOneWidget);
    expect(
      find.text('Nodes stay; only the background refresh stops'),
      findsOneWidget,
    );
    expect(find.text('Last updated: 3 h ago'), findsOneWidget);
    expect(find.textContaining('Last attempt failed'), findsNothing);
    expect(find.text('Refresh every'), findsOneWidget);
    // The stored 8 h preset is preselected and the dropdown is live.
    expect(find.text('8 h'), findsOneWidget);
    expect(_intervalField(tester).onChanged, isNotNull);

    await tester.tap(find.text('Save'));
    await tester.pumpAndSettle();
    final edit = results.single!;
    expect(edit.enabled, isTrue);
    expect(edit.updateIntervalSec, isNull); // unchanged → not sent
    expect(edit.format, isNull); // unchanged → not sent
  });

  testWidgets('a changed interval is returned', (tester) async {
    final results = <SubscriptionEdit?>[];
    await _open(tester, healthy, results);

    await tester.tap(find.text('8 h'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('7 d').last);
    await tester.pumpAndSettle();
    await tester.tap(find.text('Save'));
    await tester.pumpAndSettle();
    expect(results.single!.updateIntervalSec, 604800);
  });

  testWidgets(
    'auto-update off greys out the dropdown but keeps its value; the last '
    'failure is shown',
    (tester) async {
      final results = <SubscriptionEdit?>[];
      await _open(tester, failing, results);

      expect(find.text('Last updated: 1 d ago'), findsOneWidget);
      expect(
        find.text('Last attempt failed: subscription fetch: HTTP 503'),
        findsOneWidget,
      );
      // Disabled: no onChanged, but the stored 24 h value is still shown.
      expect(_intervalField(tester).onChanged, isNull);
      expect(find.text('24 h'), findsOneWidget);

      // Turning the switch on re-enables the dropdown in place.
      await tester.tap(find.byType(Switch));
      await tester.pumpAndSettle();
      expect(_intervalField(tester).onChanged, isNotNull);

      await tester.tap(find.text('Save'));
      await tester.pumpAndSettle();
      final edit = results.single!;
      expect(edit.enabled, isTrue);
      expect(edit.updateIntervalSec, isNull);
    },
  );

  testWidgets('a stored interval outside the presets joins the list', (
    tester,
  ) async {
    const odd = SubscriptionInfo(
      id: 's3',
      name: 'odd',
      url: 'https://example.com/sub3',
      updateIntervalSec: 9000,
    );
    final results = <SubscriptionEdit?>[];
    await _open(tester, odd, results);

    expect(find.text('Last updated: never'), findsOneWidget);
    expect(find.text('2 h 30 min'), findsOneWidget);
    await tester.tap(find.text('Save'));
    await tester.pumpAndSettle();
    expect(results.single!.updateIntervalSec, isNull);
  });

  testWidgets(
    'a stored interval below the daemon floor is shown but not echoed back',
    (tester) async {
      // A pre-floor value: the editor must still let the other fields be
      // saved, so an untouched interval stays off the wire.
      const belowFloor = SubscriptionInfo(
        id: 's4',
        name: 'old',
        url: 'https://example.com/sub4',
        updateIntervalSec: 300,
      );
      final results = <SubscriptionEdit?>[];
      await _open(tester, belowFloor, results);

      expect(find.text('5 min'), findsOneWidget);
      await tester.enterText(find.widgetWithText(TextField, 'old'), 'renamed');
      await tester.tap(find.text('Save'));
      await tester.pumpAndSettle();
      final edit = results.single!;
      expect(edit.name, 'renamed');
      expect(edit.updateIntervalSec, isNull);
    },
  );

  testWidgets('cancel resolves to null', (tester) async {
    final results = <SubscriptionEdit?>[];
    await _open(tester, healthy, results);
    await tester.tap(find.text('Cancel'));
    await tester.pumpAndSettle();
    expect(results.single, isNull);
  });
}
