/// The pure routing-draft model, ported test-for-test from the `#[cfg(test)]`
/// suite of the former egui routing editor (adapted to the
/// per-item [CondItem] editing this model deliberately deviates with), plus
/// a round-trip over the shared contract fixtures (`routing_config` parsed
/// against the real `routing_schema`).
library;

import 'dart:convert';
import 'dart:io';

import 'package:collection/collection.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:iron_link_flutter/src/model/routing_draft.dart';
import 'package:iron_link_flutter/src/wire/wire.dart';

const fixturesRoot = '../contract/fixtures';

Map<String, Object?> readFixture(String relative) {
  final body = File('$fixturesRoot/$relative').readAsStringSync();
  return jsonDecode(body) as Map<String, Object?>;
}

/// A small schema for the tests: a strings condition, a ports condition, a
/// bool condition, the standard route targets, and the two rule-set formats.
RoutingSchema testSchema() => const RoutingSchema(
      conditions: [
        ConditionSpec(
            key: 'domain_suffix', kind: 'strings', hint: 'e.g. .youtube.com'),
        ConditionSpec(key: 'port', kind: 'ports', hint: 'e.g. 80 443'),
        ConditionSpec(key: 'rule_set', kind: 'strings', hint: 'rule-set tags'),
        ConditionSpec(key: 'ip_is_private', kind: 'bool'),
      ],
      targets: ['DefaultProxy', 'Direct', 'Block', 'Node'],
      ruleSetFormats: ['binary', 'source'],
    );

void expectDeepEquals(Object? actual, Object? expected) =>
    expect(const DeepCollectionEquality().equals(actual, expected), isTrue,
        reason: 'actual   ${jsonEncode(actual)}\n'
            'expected ${jsonEncode(expected)}');

Matcher throwsDraftError(Matcher message) => throwsA(
    isA<RoutingDraftError>().having((e) => e.message, 'message', message));

void main() {
  test('a flat rule builds the expected document', () {
    final ed = RoutingDraft.empty()..name = 'split';
    ed.rules.add(FlatRule(
      target: RouteTarget('DefaultProxy'),
      conds: [
        CondDraft(
            key: 'domain_suffix',
            kind: 'strings',
            items: [CondItem('.youtube.com'), CondItem(' .ggpht.com ')]),
      ],
    ));

    expectDeepEquals(ed.toValue(), {
      'name': 'split',
      'rule_sets': <Object?>[],
      'rules': [
        {
          'domain_suffix': ['.youtube.com', '.ggpht.com'],
          'target': 'DefaultProxy',
        },
      ],
      'default_target': 'Direct',
    });
  });

  test('fromValue round-trips a flat rule', () {
    final doc = <String, Object?>{
      'name': 'split',
      'rule_sets': <Object?>[],
      'rules': [
        {
          'domain_suffix': ['.youtube.com', '.ggpht.com'],
          'target': 'DefaultProxy',
        },
      ],
      'default_target': 'Direct',
    };

    final ed = RoutingDraft.fromValue(doc, testSchema());
    expect(ed.error, isNull);
    expect(ed.name, 'split');
    expect(ed.rules, hasLength(1));
    // toValue reproduces the same document.
    expectDeepEquals(ed.toValue(), doc);

    // And the parsed drafts are what we expect.
    final rule = ed.rules.single as FlatRule;
    final target = rule.target as RouteTarget;
    expect(target.kind, 'DefaultProxy');
    expect(target.node, isNull);
    expect(rule.conds, hasLength(1));
    expect(rule.conds.single.key, 'domain_suffix');
    expect([for (final i in rule.conds.single.items) i.value],
        ['.youtube.com', '.ggpht.com']);
  });

  test('a bad port value is rejected', () {
    final ed = RoutingDraft.empty();
    ed.rules.add(FlatRule(
      target: RouteTarget('Block'),
      conds: [
        CondDraft(key: 'port', kind: 'ports', items: [CondItem('70000')]),
      ],
    ));
    expect(ed.toValue, throwsDraftError(contains('invalid port')));
  });

  test('an array condition with only blank items is rejected', () {
    final ed = RoutingDraft.empty();
    ed.rules.add(FlatRule(
      target: RouteTarget('Block'),
      conds: [
        CondDraft(
            key: 'domain_suffix',
            kind: 'strings',
            items: [CondItem(), CondItem('  ')]),
      ],
    ));
    expect(ed.toValue, throwsDraftError(contains('has no values')));
  });

  test('a node target without a node is rejected', () {
    final ed = RoutingDraft.empty();
    ed.rules.add(FlatRule(
      target: RouteTarget('Node'),
      conds: [
        CondDraft(
            key: 'domain_suffix', kind: 'strings', items: [CondItem('x.com')]),
      ],
    ));
    expect(ed.toValue, throwsDraftError(contains('pick a node')));
  });

  test('an undefined rule-set reference is rejected', () {
    final ed = RoutingDraft.empty();
    ed.rules.add(FlatRule(
      target: RouteTarget('Block'),
      conds: [
        CondDraft(key: 'rule_set', kind: 'strings', items: [CondItem('missing')]),
      ],
    ));
    expect(ed.toValue, throwsDraftError(contains('undefined rule-set')));
  });

  test('a flat rule with no conditions is rejected', () {
    final ed = RoutingDraft.empty();
    ed.rules.add(FlatRule(target: RouteTarget('Block')));
    expect(ed.toValue, throwsDraftError(contains('no conditions')));
  });

  test('the data-loss guard preserves logical rules and advanced conditions',
      () {
    // A document with BOTH a logical rule AND a flat rule carrying an
    // advanced (non-schema) condition key. The editor must preserve the
    // logical rule verbatim and keep `wifi_ssid` on the flat rule across a
    // fromValue → toValue round-trip.
    final doc = <String, Object?>{
      'id': 'abc',
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
        {
          'target': 'DefaultProxy',
          'domain_suffix': ['.x.com'],
          'wifi_ssid': ['HomeNet'],
        },
      ],
      'default_target': 'Direct',
    };

    final ed = RoutingDraft.fromValue(doc, testSchema());
    expect(ed.error, isNull);
    expect(ed.rules, hasLength(2));

    // The logical rule is opaque (preserved verbatim).
    final opaque = ed.rules[0] as OpaqueRule;
    expectDeepEquals(opaque.raw, (doc['rules'] as List)[0]);

    // The flat rule kept its advanced condition in `extra`.
    final flat = ed.rules[1] as FlatRule;
    expect(flat.conds, hasLength(1));
    expect(flat.conds.single.key, 'domain_suffix');
    expectDeepEquals(flat.extra['wifi_ssid'], ['HomeNet']);

    // The whole document round-trips (id + both rules + ssid).
    expectDeepEquals(ed.toValue(), doc);
  });

  test('a malformed document yields an error note, not a throw', () {
    final ed = RoutingDraft.fromValue('not an object', testSchema());
    expect(ed.error, isNotNull);
  });

  test('the routing_config contract fixture round-trips unchanged', () {
    final doc = readFixture('responses/routing_config.json')['routing_config']
        as Map<String, Object?>;
    final schema = (Response.fromJson(
                readFixture('responses/routing_schema.json'))
            as RoutingSchemaResponse)
        .routingSchema;

    final ed = RoutingDraft.fromValue(doc, schema);
    expect(ed.error, isNull);
    expect(ed.id, 'r1');
    expect(ed.name, 'basic');
    expectDeepEquals(ed.toValue(), doc);
  });

  test('a value with internal spaces round-trips verbatim', () {
    // Regression: the egui reference edits arrays as one comma-joined text
    // and re-splits on commas AND spaces, corrupting this Windows path into
    // two broken tokens. Per-item editing must keep it whole.
    final doc = <String, Object?>{
      'name': 'firefox-only',
      'rule_sets': <Object?>[],
      'rules': [
        {
          'process_path': ['C:\\Program Files\\Firefox\\firefox.exe'],
          'target': 'DefaultProxy',
        },
      ],
      'default_target': 'Direct',
    };
    // The REAL fixture schema: process_path is a "strings" condition there.
    final schema = (Response.fromJson(
                readFixture('responses/routing_schema.json'))
            as RoutingSchemaResponse)
        .routingSchema;

    final ed = RoutingDraft.fromValue(doc, schema);
    expect(ed.error, isNull);
    final rule = ed.rules.single as FlatRule;
    expect(rule.conds.single.items.single.value,
        'C:\\Program Files\\Firefox\\firefox.exe');
    expectDeepEquals(ed.toValue(), doc);
  });
}
