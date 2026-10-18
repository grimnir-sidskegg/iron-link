/// The routing-form draft model (schema-driven) — the pure half of the
/// routing editor; the rendering lives under `ui/`.
///
/// One deliberate design point: array-kind conditions are edited as a list
/// of whole items ([CondDraft.items]) rather than one comma-joined text. A
/// joined text re-split on commas AND spaces corrupts any value with an
/// internal space (e.g. a Windows `process_path` like
/// `C:\Program Files\...`); here items round-trip verbatim and are never
/// re-tokenized.
///
/// The drafts operate on the routing config as an opaque JSON document —
/// there is NO typed routing model on the client side. Forms render from a
/// [RoutingSchema] the daemon serves (the condition table + valid targets +
/// rule-set formats, computed from the daemon's pinned sing-box and its
/// platform), and the document round-trips through
/// [RoutingDraft.fromValue] / [RoutingDraft.toValue].
///
/// ## No data loss
///
/// Anything the editor cannot represent as a flat, schema-known rule is
/// preserved VERBATIM rather than refused or flattened:
///
/// - A logical rule (`type == "logical"`), or any rule that isn't a plain
///   JSON object, rides through as an opaque [OpaqueRule].
/// - A non-route action target (sniff/resolve/hijack-dns/unknown) rides
///   through as an [OpaqueTarget], rendered read-only.
/// - Every key on an editable rule that the editor does NOT surface as a
///   target or a schema condition is stashed in [FlatRule.extra] and
///   re-emitted on save.
///
/// Pure Dart (no Flutter imports), like the wire layer it builds on.
library;

import 'dart:convert';

import '../wire/wire.dart';

/// The schema target token that denotes "route to a specific node" — the only
/// target whose editor surface is a node picker rather than a bare label.
const String nodeToken = 'Node';

/// A validation failure from a draft's `toValue` — [message] is the
/// user-facing note the editor shows until the next save attempt.
final class RoutingDraftError implements Exception {
  const RoutingDraftError(this.message);

  final String message;

  @override
  String toString() => message;
}

/// Re-throws a [RoutingDraftError] with location context prepended, so a
/// deep validation failure still names its rule ("rule 3: ...").
T _prefixed<T>(String prefix, T Function() body) {
  try {
    return body();
  } on RoutingDraftError catch (e) {
    throw RoutingDraftError('$prefix: ${e.message}');
  }
}

/// A route target the editor can edit ([RouteTarget], modelled directly off
/// the schema's `targets` list) or one it preserves verbatim ([OpaqueTarget]).
sealed class TargetDraft {
  const TargetDraft();

  /// The default target for a fresh rule: route to the default proxy if the
  /// schema offers it, else the first non-node route target, else "Direct".
  factory TargetDraft.defaultFor(RoutingSchema schema) {
    final kind = schema.targets.contains('DefaultProxy')
        ? 'DefaultProxy'
        : schema.targets.firstWhere((t) => t != nodeToken,
            orElse: () => 'Direct');
    return RouteTarget(kind);
  }

  /// Parses a target JSON value into a draft. A bare route string or a
  /// `{"Node":"<id>"}` object is editable; anything else is preserved opaque.
  factory TargetDraft.fromValue(Object? v) {
    if (v is String) return RouteTarget(v);
    if (v is Map<String, Object?> && v.length == 1) {
      final id = v[nodeToken];
      if (id is String) return RouteTarget(nodeToken, node: id);
    }
    return OpaqueTarget(v);
  }

  /// Encodes this draft back into a target JSON value; throws
  /// [RoutingDraftError] if a node target has no node picked.
  Object? toValue();
}

/// A schema route target. [kind] is the schema token (e.g. "Direct",
/// "Block", "DefaultProxy", "Node", or any future string the daemon sends);
/// [node] is the chosen node id (a wire string) only when [kind] is
/// [nodeToken].
final class RouteTarget extends TargetDraft {
  RouteTarget(this.kind, {this.node});

  String kind;
  String? node;

  @override
  Object? toValue() {
    if (kind != nodeToken) return kind;
    final id = node;
    if (id == null || id.isEmpty) {
      throw const RoutingDraftError("pick a node for the 'Node' target");
    }
    return <String, Object?>{nodeToken: id};
  }
}

/// A target the editor can't represent — preserved verbatim.
final class OpaqueTarget extends TargetDraft {
  const OpaqueTarget(this.raw);

  final Object? raw;

  @override
  Object? toValue() => raw;
}

/// One condition row on an editable rule: the schema [key], the cached
/// schema [kind] (driving the widget + JSON shape), and the raw input
/// ([value] for string/number, [boolValue] for the checkbox kind, [items] +
/// [pending] for array kinds).
final class CondDraft {
  CondDraft({
    required this.key,
    required this.kind,
    this.value = '',
    this.boolValue = false,
    List<String>? items,
    this.pending = '',
    this.pendingAt,
  }) : items = items ?? [];

  /// Decodes a JSON condition value into a draft for [key]/[kind].
  factory CondDraft.fromValue(String key, String kind, Object? v) {
    var value = '';
    var boolValue = false;
    final items = <String>[];
    switch (kind) {
      case 'bool':
        boolValue = v is bool ? v : false;
      case 'string':
        value = v is String ? v : '';
      case 'number':
        value = v is num ? v.toString() : '';
      // "strings" / "ports" / "numbers" — one item per array element,
      // strings verbatim (internal spaces included).
      default:
        if (v is List) {
          for (final i in v) {
            items.add(i is String ? i : jsonEncode(i));
          }
        }
    }
    return CondDraft(
        key: key, kind: kind, value: value, boolValue: boolValue, items: items);
  }

  final String key;
  final String kind;
  String value;
  bool boolValue;
  final List<String> items;

  /// Array kinds only: the typed-but-uncommitted input. [toValue] encodes it
  /// as one more item, so a value is never lost just because it was not
  /// committed before a save.
  String pending;

  /// Where [pending] sits among [items] (null or out of range = the end), so
  /// re-editing an item and saving keeps the value order verbatim.
  int? pendingAt;

  /// [pendingAt] clamped to a valid insert index.
  int get pendingIndex {
    final at = pendingAt;
    return at != null && at <= items.length ? at : items.length;
  }

  /// Encodes this condition's input into its JSON value per [kind],
  /// validating ports/numbers; throws [RoutingDraftError].
  ///
  /// All numeric parses pin radix 10: a bare `int.tryParse` would accept
  /// "0x10", which is not a valid port or number here.
  Object? toValue() {
    switch (kind) {
      case 'bool':
        return boolValue;
      case 'string':
        return value.trim();
      case 'number':
        final t = value.trim();
        final n = int.tryParse(t, radix: 10);
        if (n == null) {
          throw RoutingDraftError("condition '$key': invalid number '$t'");
        }
        return n;
      case 'ports':
        return _itemsToValue((tok) {
          final p = int.tryParse(tok, radix: 10);
          if (p == null || p < 1 || p > 65535) {
            throw RoutingDraftError("condition '$key': invalid port '$tok'");
          }
          return p;
        });
      case 'numbers':
        return _itemsToValue((tok) {
          final n = int.tryParse(tok, radix: 10);
          if (n == null) {
            throw RoutingDraftError("condition '$key': invalid number '$tok'");
          }
          return n;
        });
      // "strings" and any unknown kind: a string array.
      default:
        return _itemsToValue((tok) => tok);
    }
  }

  /// Trims each item (and [pending]), drops blank ones, and maps the rest
  /// through [parse] — NO re-tokenizing, so an item may contain spaces and
  /// commas. Errors if nothing remains.
  List<Object?> _itemsToValue(Object? Function(String tok) parse) {
    final out = <Object?>[];
    for (final item in [...items]..insert(pendingIndex, pending)) {
      final tok = item.trim();
      if (tok.isEmpty) continue;
      out.add(parse(tok));
    }
    if (out.isEmpty) {
      throw RoutingDraftError("condition '$key' has no values");
    }
    return out;
  }
}

/// One rule in the editor — either a [FlatRule] the editor renders field by
/// field, or an [OpaqueRule] preserved verbatim (logical / non-object).
sealed class RuleDraft {
  const RuleDraft();

  /// Parses one rule JSON value into a draft: a logical rule (or a
  /// non-object) becomes opaque; a plain object is split into target +
  /// schema conditions + preserved [FlatRule.extra].
  factory RuleDraft.fromValue(Object? rule, RoutingSchema schema) {
    if (rule is! Map<String, Object?>) return OpaqueRule(rule);
    if (rule['type'] == 'logical') return OpaqueRule(rule);

    // Key presence matters here and below: a present-but-unrepresentable
    // value must become opaque, only a MISSING key takes the default.
    final target = rule.containsKey('target')
        ? TargetDraft.fromValue(rule['target'])
        : TargetDraft.defaultFor(schema);

    final conds = <CondDraft>[];
    final extra = <String, Object?>{};
    for (final entry in rule.entries) {
      if (entry.key == 'target') continue;
      // A schema condition whose kind the editor can edit → a CondDraft;
      // everything else (unknown/advanced keys, stray type/mode/rules) is
      // preserved verbatim in `extra`.
      ConditionSpec? spec;
      for (final c in schema.conditions) {
        if (c.key == entry.key) {
          spec = c;
          break;
        }
      }
      if (spec != null) {
        conds.add(CondDraft.fromValue(entry.key, spec.kind, entry.value));
      } else {
        extra[entry.key] = entry.value;
      }
    }
    return FlatRule(target: target, conds: conds, extra: extra);
  }
}

/// An editable flat rule: its target, its schema conditions, and [extra] —
/// every other key of the original rule object, kept verbatim so unknown /
/// advanced conditions and any future keys survive a save unchanged.
final class FlatRule extends RuleDraft {
  FlatRule({required this.target, List<CondDraft>? conds,
      Map<String, Object?>? extra})
      : conds = conds ?? [],
        extra = extra ?? {};

  TargetDraft target;
  final List<CondDraft> conds;
  final Map<String, Object?> extra;
}

/// A rule the editor can't round-trip (logical, or not a plain object).
final class OpaqueRule extends RuleDraft {
  const OpaqueRule(this.raw);

  final Object? raw;
}

/// One rule-set source draft. [binary] toggles the format
/// "binary"/"source"; [detour] is the download detour ("" =
/// `download_detour: null`).
final class RuleSetDraft {
  RuleSetDraft({this.tag = '', this.url = '', this.binary = true,
      this.detour = ''});

  String tag;
  String url;
  bool binary;
  String detour;
}

/// The routing editor's draft state — drafts over a JSON document, no
/// rendering concerns.
final class RoutingDraft {
  /// A blank draft for a NEW routing config (no id; default target Direct;
  /// empty rules + rule-sets).
  RoutingDraft.empty()
      : name = 'new-routing',
        id = null,
        ruleSets = [],
        rules = [],
        defaultTarget = RouteTarget('Direct'),
        error = null;

  /// Parses a routing-config document into drafts, defensively: a malformed
  /// document never throws — it yields whatever parsed plus an [error] note.
  factory RoutingDraft.fromValue(Object? doc, RoutingSchema schema) {
    final ed = RoutingDraft.empty();

    if (doc is! Map<String, Object?>) {
      ed.error = 'routing document is not a JSON object';
      return ed;
    }
    String? note;

    ed.name = doc['name'] is String ? doc['name'] as String : 'new-routing';
    ed.id = doc['id'] is String ? doc['id'] as String : null;

    // Rule-sets.
    final ruleSets = doc['rule_sets'];
    if (ruleSets is List) {
      for (final rs in ruleSets) {
        if (rs is! Map<String, Object?>) {
          note ??= 'some rule-sets were not objects';
          continue;
        }
        ed.ruleSets.add(RuleSetDraft(
          tag: rs['tag'] is String ? rs['tag'] as String : '',
          url: rs['url'] is String ? rs['url'] as String : '',
          binary: rs['format'] != 'source',
          detour: rs['download_detour'] is String
              ? rs['download_detour'] as String
              : '',
        ));
      }
    }

    // Rules.
    final rules = doc['rules'];
    if (rules is List) {
      for (final rule in rules) {
        ed.rules.add(RuleDraft.fromValue(rule, schema));
      }
    }

    // Default target.
    ed.defaultTarget = doc.containsKey('default_target')
        ? TargetDraft.fromValue(doc['default_target'])
        : RouteTarget('Direct');

    ed.error = note;
    return ed;
  }

  String name;

  /// Non-null when editing an existing config (preserve its id on save);
  /// null when creating a new one.
  String? id;
  final List<RuleSetDraft> ruleSets;
  final List<RuleDraft> rules;
  TargetDraft defaultTarget;

  /// A parse / last-save error, shown until the next save attempt.
  String? error;

  /// Rebuilds the routing-config document from the current drafts; throws
  /// [RoutingDraftError]. Validates: a non-empty name; a node target that
  /// has a node; flat rules with at least one condition; rule-set conditions
  /// referencing only defined tags; no condition key colliding with a
  /// preserved [FlatRule.extra] key.
  Map<String, Object?> toValue() {
    final name = this.name.trim();
    if (name.isEmpty) {
      throw const RoutingDraftError('routing name cannot be empty');
    }

    // Rule-sets (skip blank tags) + the set of defined tags for validation.
    final ruleSetsOut = <Object?>[];
    final defined = <String>{};
    for (final rs in ruleSets) {
      final tag = rs.tag.trim();
      if (tag.isEmpty) continue;
      defined.add(tag);
      final detour = rs.detour.trim();
      ruleSetsOut.add(<String, Object?>{
        'tag': tag,
        'url': rs.url.trim(),
        'format': rs.binary ? 'binary' : 'source',
        'download_detour': detour.isEmpty ? null : detour,
      });
    }

    // Rules.
    final rulesOut = <Object?>[];
    for (var i = 0; i < rules.length; i++) {
      final n = i + 1;
      switch (rules[i]) {
        case OpaqueRule(:final raw):
          rulesOut.add(raw);
        case FlatRule(:final target, :final conds, :final extra):
          if (conds.isEmpty) {
            throw RoutingDraftError('rule $n has no conditions');
          }
          final m = Map<String, Object?>.of(extra);
          for (final cond in conds) {
            if (m.containsKey(cond.key)) {
              throw RoutingDraftError("rule $n: condition '${cond.key}' "
                  'duplicates an existing key');
            }
            final value = _prefixed('rule $n', cond.toValue);
            // Validate rule-set references against the defined tags.
            if (cond.key == 'rule_set' && value is List) {
              for (final t in value) {
                if (t is String && !defined.contains(t)) {
                  throw RoutingDraftError(
                      "rule $n references undefined rule-set '$t'");
                }
              }
            }
            m[cond.key] = value;
          }
          m['target'] = _prefixed('rule $n', target.toValue);
          rulesOut.add(m);
      }
    }

    return <String, Object?>{
      if (id != null) 'id': id,
      'name': name,
      'rule_sets': ruleSetsOut,
      'rules': rulesOut,
      'default_target': _prefixed('default target', defaultTarget.toValue),
    };
  }
}

/// A node or auto group a Node target can point at, as the pickers and the
/// routing diagram see it (from `list_nodes`, in daemon order).
typedef NodeRef = ({String id, String name, bool isGroup});

/// One destination lane of the routing diagram: a distinct route [target]
/// (kind + node id), the [node] it resolves to (Node lanes whose id is in the
/// node list only), the 0-based indices of the [rules] routing there, and
/// whether it is the config's default target.
final class RouteLane {
  RouteLane(this.target, this.node);

  final RouteTarget target;
  final NodeRef? node;
  final List<int> rules = [];
  bool isDefault = false;
}

/// The diagram lanes of [draft]: one per distinct route target used by a
/// flat rule or the default target — the default target's lane first, then
/// first-appearance order. Opaque rules and targets get no lane. No routing
/// (null) = one default DefaultProxy lane: everything goes via the active
/// node.
List<RouteLane> routeLanes(RoutingDraft? draft, List<NodeRef> nodes) {
  final lanes = <String, RouteLane>{};
  RouteLane lane(RouteTarget t) => lanes.putIfAbsent(
      '${t.kind}\u0000${t.node}',
      () => RouteLane(
          RouteTarget(t.kind, node: t.node),
          t.kind == nodeToken
              ? nodes.where((n) => n.id == t.node).firstOrNull
              : null));
  final fallback = draft?.defaultTarget ?? RouteTarget('DefaultProxy');
  if (fallback is RouteTarget) lane(fallback).isDefault = true;
  for (final (i, rule) in (draft?.rules ?? const <RuleDraft>[]).indexed) {
    if (rule case FlatRule(target: final RouteTarget t)) lane(t).rules.add(i);
  }
  return [...lanes.values];
}
