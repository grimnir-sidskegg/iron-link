/// Widget tests for the routing form editor, over the shared contract
/// fixtures: the `routing_config` document parsed against the real
/// `routing_schema`. The page takes pre-loaded data (no client, no wire) —
/// `onSave` is a plain callback the tests capture.
library;

import 'dart:convert';
import 'dart:io';

import 'package:collection/collection.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:iron_link_flutter/src/model/routing_draft.dart';
import 'package:iron_link_flutter/src/ui/routing_editor_page.dart';
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
  RoutingDraft draft,
  RoutingSchema schema, {
  Future<bool> Function(Map<String, Object?> doc)? onSave,
  Future<String?> Function()? pickFile,
}) {
  return MaterialApp(
    home: RoutingEditorPage(
      draft: draft,
      schema: schema,
      nodes: const [(id: 'n1', name: 'Tokyo')],
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

/// Finds the target dropdown whose current value is [kind] — dropdown text
/// can't be matched with find.text (every item's label sits in the closed
/// button's IndexedStack).
Finder dropdownWithValue(String kind) => find.byWidgetPredicate(
    (w) => w is DropdownButton<String> && w.value == kind);

void main() {
  testWidgets('rule cards render from the fixture document', (tester) async {
    final schema = fixtureSchema();
    final draft = RoutingDraft.fromValue(fixtureDoc(), schema);
    await tester.pumpWidget(harness(draft, schema));

    expect(find.text('Edit routing: basic'), findsOneWidget);
    expect(find.text('Rules (1)'), findsOneWidget);
    expect(find.text('Rule 1'), findsOneWidget);
    expect(find.text('domain_keyword'), findsOneWidget);
    expect(find.text('example.com'), findsOneWidget);
    expect(dropdownWithValue('DefaultProxy'), findsOneWidget); // the rule
    expect(dropdownWithValue('Direct'), findsOneWidget); // the default
    expect(find.text('Rule-sets (0)'), findsOneWidget);
  });

  testWidgets('a logical rule shows the preserved label', (tester) async {
    final schema = fixtureSchema();
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
    final draft = RoutingDraft.fromValue(doc, schema);
    await tester.pumpWidget(harness(draft, schema));

    expect(find.textContaining('logical / advanced rule'), findsOneWidget);
    // No editable target picker for the opaque rule — only the default's.
    expect(find.byType(DropdownButton<String>), findsOneWidget);
  });

  testWidgets('an unsupported existing condition still renders',
      (tester) async {
    // wifi_ssid is in the fixture schema with supported=false: an existing
    // value renders as a row, but the add menu must not offer it.
    final schema = fixtureSchema();
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
    final draft = RoutingDraft.fromValue(doc, schema);
    await tester.pumpWidget(harness(draft, schema));

    expect(find.text('wifi_ssid'), findsOneWidget);
    expect(find.text('HomeNet'), findsOneWidget);

    // The per-item rows push the menu below the test viewport.
    await tester.ensureVisible(find.text('Add condition'));
    await tester.pumpAndSettle();
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
    final schema = fixtureSchema();
    final draft = RoutingDraft.empty()..name = '';
    var saveCalled = false;
    await tester.pumpWidget(harness(draft, schema, onSave: (_) async {
      saveCalled = true;
      return true;
    }));

    await tester.tap(find.text('Save'));
    await tester.pump();
    expect(find.text('routing name cannot be empty'), findsOneWidget);
    expect(saveCalled, isFalse);
  });

  testWidgets('an array condition renders one field per item and "add value" '
      'appends an editable row', (tester) async {
    final schema = fixtureSchema();
    final draft = oneRuleDraft([
      CondDraft(
          key: 'process_name',
          kind: 'strings',
          items: [CondItem('firefox'), CondItem('chrome')]),
    ]);
    await tester.pumpWidget(harness(draft, schema));

    expect(find.text('process_name'), findsOneWidget);
    expect(find.text('firefox'), findsOneWidget);
    expect(find.text('chrome'), findsOneWidget);

    final items = (draft.rules.single as FlatRule).conds.single.items;
    await tester.tap(find.text('add value'));
    await tester.pump();
    expect(items, hasLength(3));
    await tester.enterText(find.byType(TextFormField).last, 'zen');
    expect(items.last.value, 'zen');
  });

  testWidgets('removing the middle item leaves the other fields intact',
      (tester) async {
    final schema = fixtureSchema();
    final draft = oneRuleDraft([
      CondDraft(
          key: 'process_name',
          kind: 'strings',
          items: [CondItem('firefox'), CondItem('chrome'), CondItem('zen')]),
    ]);
    await tester.pumpWidget(harness(draft, schema));

    // Edit the LAST field first: live edit state (not just initialValue)
    // must survive the structural change — this guards the per-item keying.
    await tester.enterText(find.byType(TextFormField).at(3), 'zen-edited');
    await tester.tap(find.byTooltip('Remove value').at(1));
    await tester.pump();

    expect(find.text('firefox'), findsOneWidget);
    expect(find.text('zen-edited'), findsOneWidget);
    expect(find.text('chrome'), findsNothing);
    final items = (draft.rules.single as FlatRule).conds.single.items;
    expect([for (final i in items) i.value], ['firefox', 'zen-edited']);
  });

  testWidgets('browse fills the basename for process_name and the full path '
      'for process_path', (tester) async {
    final schema = fixtureSchema();
    final draft = oneRuleDraft([
      CondDraft(key: 'process_name', kind: 'strings', items: [CondItem()]),
      CondDraft(key: 'process_path', kind: 'strings', items: [CondItem()]),
      CondDraft(
          key: 'domain_suffix', kind: 'strings', items: [CondItem('.x.com')]),
    ]);
    await tester.pumpWidget(
        harness(draft, schema, pickFile: () async => '/usr/bin/firefox'));

    // Only the two process_* items offer browsing.
    expect(find.byTooltip('Browse…'), findsNWidgets(2));

    await tester.tap(find.byTooltip('Browse…').first);
    await tester.pump();
    expect(find.text('firefox'), findsOneWidget);

    await tester.tap(find.byTooltip('Browse…').last);
    await tester.pump();
    expect(find.text('/usr/bin/firefox'), findsOneWidget);

    final conds = (draft.rules.single as FlatRule).conds;
    expect(conds[0].items.single.value, 'firefox');
    expect(conds[1].items.single.value, '/usr/bin/firefox');
  });

  testWidgets('without an injected picker there are no browse buttons',
      (tester) async {
    final schema = fixtureSchema();
    final draft = oneRuleDraft([
      CondDraft(key: 'process_path', kind: 'strings', items: [CondItem()]),
    ]);
    await tester.pumpWidget(harness(draft, schema));
    expect(find.byTooltip('Browse…'), findsNothing);
  });

  testWidgets('a pure open then save round-trips the document unchanged',
      (tester) async {
    final schema = fixtureSchema();
    final doc = fixtureDoc();
    final draft = RoutingDraft.fromValue(doc, schema);
    Map<String, Object?>? saved;
    await tester.pumpWidget(harness(draft, schema, onSave: (d) async {
      saved = d;
      return false; // keep the page up — nothing to pop in the harness
    }));

    await tester.tap(find.text('Save'));
    await tester.pump();
    expect(saved, isNotNull);
    expect(const DeepCollectionEquality().equals(saved, doc), isTrue,
        reason: 'saved    ${jsonEncode(saved)}\n'
            'expected ${jsonEncode(doc)}');
    expect(draft.error, isNull);
  });
}
