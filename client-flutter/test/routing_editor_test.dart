/// Widget tests for the routing form editor, over the shared contract
/// fixtures: the `routing_config` document parsed against the real
/// `routing_schema`. The page takes pre-loaded data (no client, no wire) —
/// `onSave` is a plain callback the tests capture.
library;

import 'dart:convert';
import 'dart:io';

import 'package:collection/collection.dart';
import 'package:flutter/gestures.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:iron_link_flutter/src/model/routing_draft.dart';
import 'package:iron_link_flutter/src/ui/routing_editor_page.dart';
import 'package:iron_link_flutter/src/ui/widgets/iron_widgets.dart';
import 'package:iron_link_flutter/src/wire/wire.dart';

const fixturesRoot = '../contract/fixtures';

Map<String, Object?> readFixture(String relative) {
  final body = File('$fixturesRoot/$relative').readAsStringSync();
  return jsonDecode(body) as Map<String, Object?>;
}

RoutingSchema fixtureSchema() =>
    (Response.fromJson(readFixture('responses/routing_schema.json'))
            as RoutingSchemaResponse)
        .routingSchema;

Map<String, Object?> fixtureDoc() =>
    readFixture('responses/routing_config.json')['routing_config']
        as Map<String, Object?>;

Widget harness(
  RoutingDraft draft, {
  Future<bool> Function(Map<String, Object?> doc)? onSave,
  Future<String?> Function()? pickFile,
  List<({String id, String name, bool isGroup})> nodes = const [
    (id: 'n1', name: 'Tokyo', isGroup: false),
    (id: 'g1', name: 'Fastest', isGroup: true),
  ],
}) {
  return MaterialApp(
    home: RoutingEditorPage(
      draft: draft,
      schema: fixtureSchema(),
      nodes: nodes,
      onSave: onSave ?? (_) async => false,
      pickFile: pickFile,
    ),
  );
}

/// A draft over one flat rule with [conds], for the array-item tests.
RoutingDraft oneRuleDraft(List<CondDraft> conds) {
  final draft = RoutingDraft.empty()..name = 'procs';
  draft.rules.add(FlatRule(target: RouteTarget('Direct'), conds: conds));
  return draft;
}

/// A flat rule over one domain_suffix value.
FlatRule suffixRule(String suffix, [String target = 'Direct']) => FlatRule(
      target: RouteTarget(target),
      conds: [
        CondDraft(key: 'domain_suffix', kind: 'strings', items: [suffix]),
      ],
    );

/// The chip tests run on touch AND desktop platforms: only desktop unfocuses
/// a text field on a tap outside it (the commit-on-focus-loss path).
final chipPlatforms = TargetPlatformVariant.all();

/// Pumps [draft] and expands its first rule (rules start collapsed).
Future<void> pumpExpanded(
  WidgetTester tester,
  RoutingDraft draft, {
  Future<bool> Function(Map<String, Object?> doc)? onSave,
  Future<String?> Function()? pickFile,
}) async {
  await tester.pumpWidget(harness(draft, onSave: onSave, pickFile: pickFile));
  await tester.tap(find.byIcon(Icons.expand_more).first);
  await tester.pumpAndSettle();
}

/// Taps Save on the already-pumped page and returns the saved document.
Future<Map<String, Object?>?> tapSave(
    WidgetTester tester, List<Map<String, Object?>> sink) async {
  await tester.tap(find.text('Save'));
  await tester.pump();
  return sink.lastOrNull;
}

/// The saved rules' JSON, for the tests that only care about `rules`.
List<Object?> savedRules(Map<String, Object?>? doc) =>
    doc!['rules']! as List<Object?>;

void main() {
  testWidgets('rules render collapsed from the fixture document',
      (tester) async {
    final draft = RoutingDraft.fromValue(fixtureDoc(), fixtureSchema());
    await tester.pumpWidget(harness(draft));

    expect(find.text('Edit routing: basic'), findsOneWidget);
    expect(find.text('Rules (1)'), findsOneWidget);
    expect(find.text('domain_keyword example.com'), findsOneWidget);
    // Target pills carry the schema tokens verbatim.
    expect(find.widgetWithText(IronTag, 'DefaultProxy'), findsOneWidget);
    expect(find.widgetWithText(IronTag, 'Direct'), findsOneWidget); // default
    expect(find.text('Everything else'), findsOneWidget);
    expect(find.text('Rule-sets (0)'), findsOneWidget);
    // Collapsed: no form.
    expect(find.byType(InputChip), findsNothing);
    expect(find.text('Add condition'), findsNothing);
  });

  testWidgets('a click expands a rule into its form and collapses it again',
      (tester) async {
    final draft = RoutingDraft.fromValue(fixtureDoc(), fixtureSchema());
    await tester.pumpWidget(harness(draft));

    await tester.tap(find.text('domain_keyword example.com'));
    await tester.pumpAndSettle();
    expect(find.widgetWithText(InputChip, 'example.com'), findsOneWidget);
    expect(find.text('Add condition'), findsOneWidget);

    await tester.tap(find.byIcon(Icons.expand_less).first);
    await tester.pumpAndSettle();
    expect(find.byType(InputChip), findsNothing);
  });

  testWidgets('a logical rule shows the preserved label and its JSON',
      (tester) async {
    final doc = <String, Object?>{
      'name': 'advanced',
      'rule_sets': <Object?>[],
      'rules': [
        {
          'type': 'logical',
          'mode': 'or',
          'target': 'Block',
          'rules': [
            {
              'target': 'Block',
              'domain': ['a'],
            },
          ],
        },
      ],
      'default_target': 'Direct',
    };
    await tester
        .pumpWidget(harness(RoutingDraft.fromValue(doc, fixtureSchema())));

    expect(find.textContaining('logical / advanced rule'), findsOneWidget);
    // No target pill for the opaque rule — only the default's.
    expect(find.byType(IronTag), findsOneWidget);

    await tester.tap(find.textContaining('logical / advanced rule'));
    await tester.pumpAndSettle();
    expect(find.textContaining('"mode": "or"'), findsOneWidget);
  });

  testWidgets('an unsupported existing condition still renders',
      (tester) async {
    // wifi_ssid is in the fixture schema with supported=false: an existing
    // value renders as a row, but the add menu must not offer it.
    final doc = <String, Object?>{
      'name': 'mobile',
      'rule_sets': <Object?>[],
      'rules': [
        {
          'target': 'Direct',
          'wifi_ssid': ['HomeNet'],
          'domain_keyword': ['x'],
        },
      ],
      'default_target': 'Direct',
    };
    await pumpExpanded(tester, RoutingDraft.fromValue(doc, fixtureSchema()));

    expect(find.text('wifi_ssid'), findsOneWidget);
    expect(find.widgetWithText(InputChip, 'HomeNet'), findsOneWidget);

    await tester.tap(find.text('Add condition'));
    await tester.pumpAndSettle();
    expect(find.widgetWithText(PopupMenuItem<ConditionSpec>, 'domain'),
        findsOneWidget);
    // Unsupported on this platform → filtered.
    expect(find.widgetWithText(PopupMenuItem<ConditionSpec>, 'wifi_ssid'),
        findsNothing);
    // Already present on the rule → filtered.
    expect(find.widgetWithText(PopupMenuItem<ConditionSpec>, 'domain_keyword'),
        findsNothing);
  });

  testWidgets('save with an empty name shows the validation error',
      (tester) async {
    final draft = RoutingDraft.empty()..name = '';
    var saveCalled = false;
    await tester.pumpWidget(harness(draft, onSave: (_) async {
      saveCalled = true;
      return true;
    }));

    await tester.tap(find.text('Save'));
    await tester.pump();
    expect(find.text('routing name cannot be empty'), findsOneWidget);
    expect(saveCalled, isFalse);
  });

  testWidgets('dragging a rule by its handle reorders the saved rules',
      (tester) async {
    final draft = RoutingDraft.empty()..name = 'order';
    draft.rules.addAll(
        [suffixRule('.a.example.com'), suffixRule('.b.example.com')]);
    final sink = <Map<String, Object?>>[];
    await tester.pumpWidget(harness(draft, onSave: (d) async {
      sink.add(d);
      return false;
    }));

    final handle = find.byIcon(Icons.drag_indicator).first;
    final drag = await tester.startGesture(tester.getCenter(handle));
    await tester.pump();
    for (var i = 0; i < 5; i++) {
      await drag.moveBy(const Offset(0, 20));
      await tester.pump();
    }
    await drag.up();
    await tester.pumpAndSettle();

    final rules = savedRules(await tapSave(tester, sink));
    expect([for (final r in rules) (r! as Map)['domain_suffix']], [
      ['.b.example.com'],
      ['.a.example.com'],
    ]);
    // Numbers follow the order: rule 1 now summarises the moved-up rule.
    final first =
        find.ancestor(of: find.text('1'), matching: find.byType(Row)).first;
    expect(
        find.descendant(
            of: first, matching: find.text('domain_suffix .b.example.com')),
        findsOneWidget);
  });

  testWidgets('Add rule appends an expanded rule and scrolls it into view',
      (tester) async {
    final draft = RoutingDraft.empty()..name = 'many';
    for (var i = 0; i < 12; i++) {
      draft.rules.add(suffixRule('.r$i.example.com'));
    }
    await tester.pumpWidget(harness(draft));

    // The footer is built lazily: scroll until it is, then park the button
    // at the viewport's bottom edge so the new rule lands below the fold.
    await tester.scrollUntilVisible(find.text('Add rule'), 200,
        scrollable: find.byType(Scrollable).first);
    await Scrollable.ensureVisible(tester.element(find.text('Add rule')),
        alignment: 1);
    await tester.pumpAndSettle();
    await tester.tap(find.text('Add rule'));
    await tester.pumpAndSettle();

    expect(draft.rules, hasLength(13));
    expect((draft.rules.last as FlatRule).conds, isEmpty);
    // Expanded (its form is the only one open) and on screen.
    expect(find.text('Add condition').hitTestable(), findsOneWidget);
    expect(find.text('13').hitTestable(), findsOneWidget);
  });

  testWidgets('Enter commits a chip, tapping a chip edits it, × removes it',
      (tester) async {
    final draft = oneRuleDraft([
      CondDraft(key: 'process_name', kind: 'strings', items: ['firefox']),
    ]);
    await pumpExpanded(tester, draft);
    final cond = (draft.rules.single as FlatRule).conds.single;

    // A value with an internal space stays whole.
    await tester.enterText(find.byType(TextField).last, ' my browser ');
    await tester.pump();
    await tester.testTextInput.receiveAction(TextInputAction.done);
    await tester.pump();
    expect(cond.items, ['firefox', 'my browser']);
    expect(cond.pending, isEmpty);
    expect(find.widgetWithText(InputChip, 'my browser'), findsOneWidget);

    await tester.tap(find.widgetWithText(InputChip, 'firefox'));
    await tester.pump();
    expect(cond.items, ['my browser']);
    expect(cond.pending, 'firefox');
    expect(
        tester.widget<TextField>(find.byType(TextField).last).controller!.text,
        'firefox');

    await tester.tap(find.byTooltip('Remove value'));
    await tester.pump();
    expect(cond.items, isEmpty);
    expect(find.byType(InputChip), findsNothing);
  }, variant: chipPlatforms);

  testWidgets('re-editing chips without changes keeps the saved order',
      (tester) async {
    final draft = oneRuleDraft([
      CondDraft(
          key: 'domain_suffix', kind: 'strings', items: ['.a', '.b', '.c']),
    ]);
    final sink = <Map<String, Object?>>[];
    await pumpExpanded(tester, draft, onSave: (d) async {
      sink.add(d);
      return false;
    });

    // Edit the last chip, then switch to the first: the last goes back in
    // place; the first is saved from the input, also in place.
    await tester.tap(find.widgetWithText(InputChip, '.c'));
    await tester.pump();
    await tester.tap(find.widgetWithText(InputChip, '.a'));
    await tester.pump();
    final rules = savedRules(await tapSave(tester, sink));
    expect((rules.single! as Map)['domain_suffix'], ['.a', '.b', '.c']);
  }, variant: chipPlatforms);

  testWidgets('removing a middle chip keeps the others and the typed input',
      (tester) async {
    final draft = oneRuleDraft([
      CondDraft(
          key: 'domain_suffix', kind: 'strings', items: ['.a', '.b', '.c']),
    ]);
    final sink = <Map<String, Object?>>[];
    await pumpExpanded(tester, draft, onSave: (d) async {
      sink.add(d);
      return false;
    });
    final cond = (draft.rules.single as FlatRule).conds.single;

    await tester.enterText(find.byType(TextField).last, '.d');
    await tester.pump();
    await tester.tap(find.byTooltip('Remove value').at(1));
    await tester.pump();
    expect(cond.items, ['.a', '.c']);
    expect(
        tester.widget<TextField>(find.byType(TextField).last).controller!.text,
        '.d');
    final rules = savedRules(await tapSave(tester, sink));
    expect((rules.single! as Map)['domain_suffix'], ['.a', '.c', '.d']);
  }, variant: chipPlatforms);

  testWidgets('on desktop a tap outside the input commits it as a chip',
      (tester) async {
    final draft = oneRuleDraft([
      CondDraft(key: 'process_name', kind: 'strings', items: ['firefox']),
    ]);
    await pumpExpanded(tester, draft);
    final cond = (draft.rules.single as FlatRule).conds.single;

    await tester.enterText(find.byType(TextField).last, 'zen');
    await tester.pump(); // the focused field's tap region is now live
    await tester.tapAt(const Offset(780, 590), kind: PointerDeviceKind.mouse);
    await tester.pump();
    expect(cond.items, ['firefox', 'zen']);
    expect(cond.pending, isEmpty);
    expect(find.widgetWithText(InputChip, 'zen'), findsOneWidget);
  }, variant: TargetPlatformVariant.desktop());

  testWidgets('uncommitted input is included on save', (tester) async {
    final draft = oneRuleDraft([
      CondDraft(key: 'process_name', kind: 'strings', items: ['firefox']),
    ]);
    final sink = <Map<String, Object?>>[];
    await pumpExpanded(tester, draft, onSave: (d) async {
      sink.add(d);
      return false;
    });

    await tester.enterText(find.byType(TextField).last, 'zen');
    await tester.pump();
    final rules = savedRules(await tapSave(tester, sink));
    expect((rules.single! as Map)['process_name'], ['firefox', 'zen']);
  }, variant: chipPlatforms);

  testWidgets('a long chip value ellipsizes; tapping it edits the full value',
      (tester) async {
    tester.view
      ..physicalSize = const Size(640, 800)
      ..devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    final path = 'C:\\Program Files\\${'Deeply Nested Folder\\' * 5}app.exe';
    await pumpExpanded(tester, oneRuleDraft([
      CondDraft(key: 'process_path', kind: 'strings', items: [path]),
    ]));

    // One ellipsized line inside the chip — not a wrapped label the chip
    // silently cuts mid-glyph.
    final label = tester.renderObject<RenderParagraph>(find.text(path));
    expect(label.maxLines, 1);
    expect(label.didExceedMaxLines, isTrue);
    expect(tester.getSize(find.text(path)).width,
        lessThanOrEqualTo(tester.getSize(find.byType(InputChip)).width));

    await tester.tap(find.byType(InputChip));
    await tester.pump();
    expect(
        tester.widget<TextField>(find.byType(TextField).last).controller!.text,
        path);
  });

  testWidgets('a long node name ellipsizes instead of overflowing',
      (tester) async {
    tester.view
      ..physicalSize = const Size(640, 800)
      ..devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    final long = 'Very long node name ' * 4; // ~80 chars
    final draft = RoutingDraft.empty()..name = 'long';
    draft.rules.add(FlatRule(
        target: RouteTarget(nodeToken, node: 'n1'),
        conds: [
          CondDraft(key: 'domain_suffix', kind: 'strings', items: ['.x']),
        ]));
    await tester.pumpWidget(harness(draft, nodes: [
      (id: 'n1', name: long, isGroup: false),
      (id: 'g1', name: long, isGroup: true),
    ]));

    // Collapsed, expanded (the pill becomes the picker), and the open menu:
    // any RenderFlex overflow would fail the test as a FlutterError.
    await tester.tap(find.byIcon(Icons.expand_more).first);
    await tester.pumpAndSettle();
    await tester.tap(find.byTooltip('Change target').first);
    await tester.pumpAndSettle();
    await tester.tapAt(const Offset(5, 5)); // dismiss the menu
    await tester.pumpAndSettle();

    // The chevron is still on screen and works.
    await tester.tap(find.byIcon(Icons.expand_less).first);
    await tester.pumpAndSettle();
    expect(find.byIcon(Icons.expand_less), findsNothing);
  });

  testWidgets('browse adds the basename for process_name and the full path '
      'for process_path', (tester) async {
    final draft = oneRuleDraft([
      CondDraft(key: 'process_name', kind: 'strings'),
      CondDraft(key: 'process_path', kind: 'strings'),
      CondDraft(key: 'domain_suffix', kind: 'strings', items: ['.x.com']),
    ]);
    await pumpExpanded(tester, draft,
        pickFile: () async => '/usr/bin/firefox');

    // Only the two process_* conditions offer browsing.
    expect(find.byTooltip('Browse…'), findsNWidgets(2));

    await tester.tap(find.byTooltip('Browse…').first);
    await tester.pump();
    await tester.tap(find.byTooltip('Browse…').last);
    await tester.pump();
    expect(find.widgetWithText(InputChip, 'firefox'), findsOneWidget);
    expect(find.widgetWithText(InputChip, '/usr/bin/firefox'), findsOneWidget);

    final conds = (draft.rules.single as FlatRule).conds;
    expect(conds[0].items, ['firefox']);
    expect(conds[1].items, ['/usr/bin/firefox']);
  });

  testWidgets('without an injected picker there are no browse buttons',
      (tester) async {
    await pumpExpanded(tester,
        oneRuleDraft([CondDraft(key: 'process_path', kind: 'strings')]));
    expect(find.byTooltip('Browse…'), findsNothing);
  });

  testWidgets('the target menu lists schema tokens and nodes; an auto group '
      'saves as a Node target', (tester) async {
    final draft = RoutingDraft.empty()..name = 'pin';
    draft.rules.add(suffixRule('.x.example.com', 'Block'));
    final sink = <Map<String, Object?>>[];
    await pumpExpanded(tester, draft, onSave: (d) async {
      sink.add(d);
      return false;
    });
    expect(find.widgetWithText(IronTag, 'Block'), findsOneWidget);

    await tester.tap(find.byTooltip('Change target').first);
    await tester.pumpAndSettle();
    for (final label in ['DefaultProxy', 'Direct', 'Node', 'Tokyo']) {
      expect(find.text(label), findsWidgets, reason: label);
    }
    // Only the group carries the auto badge.
    expect(find.widgetWithText(IronKindChip, 'auto'), findsOneWidget);

    await tester.tap(find.text('Fastest'));
    await tester.pumpAndSettle();
    expect(find.widgetWithText(IronTag, 'Fastest'), findsOneWidget);

    final rules = savedRules(await tapSave(tester, sink));
    expect((rules.single! as Map)['target'], {'Node': 'g1'});
  });

  testWidgets('a pure open then save round-trips the document unchanged',
      (tester) async {
    final doc = fixtureDoc();
    final draft = RoutingDraft.fromValue(doc, fixtureSchema());
    final sink = <Map<String, Object?>>[];
    await tester.pumpWidget(harness(draft, onSave: (d) async {
      sink.add(d);
      return false;
    }));

    final saved = await tapSave(tester, sink);
    expect(saved, isNotNull);
    expect(const DeepCollectionEquality().equals(saved, doc), isTrue,
        reason: 'saved    ${jsonEncode(saved)}\n'
            'expected ${jsonEncode(doc)}');
    expect(draft.error, isNull);
  });
}
