/// The routing pieces the form editor and the Routing page's diagram share:
/// target colours / labels / pills and the one-line rule summary.
library;

import 'package:flutter/material.dart';

import '../model/routing_draft.dart';
import 'theme/app_colors.dart';
import 'theme/app_typography.dart';
import 'widgets/iron_widgets.dart';

/// The pill colour for a schema target token; an unknown (future) token
/// is neutral.
Color routeTargetColor(BuildContext context, String kind) {
  final c = context.appColors;
  return switch (kind) {
    'DefaultProxy' || nodeToken => c.routeProxy,
    'Direct' => c.routeDirect,
    'Block' => c.routeBlocked,
    _ => c.iron.dim,
  };
}

/// A route target's label: the schema token verbatim, or for a Node target
/// the node's name — a stored id missing from [nodes] shows as-is.
String routeTargetLabel(RouteTarget target, List<NodeRef> nodes) {
  if (target.kind != nodeToken) return target.kind;
  final id = target.node ?? '';
  return nodes.where((n) => n.id == id).firstOrNull?.name ??
      (id.isEmpty ? nodeToken : id);
}

/// A read-only target pill: a coloured [IronTag] labelled by
/// [routeTargetLabel]; an [OpaqueTarget] is a preserved label. Width-capped
/// and ellipsized, so a long node name never pushes its row's trailing
/// controls away.
class RouteTargetPill extends StatelessWidget {
  const RouteTargetPill(this.target, {super.key, required this.nodes});

  final TargetDraft target;
  final List<NodeRef> nodes;

  @override
  Widget build(BuildContext context) {
    final target = this.target;
    return ConstrainedBox(
      constraints: const BoxConstraints(maxWidth: 240),
      child: target is RouteTarget
          ? IronTag(routeTargetLabel(target, nodes),
              color: routeTargetColor(context, target.kind))
          : Text('advanced action target (preserved)',
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
              style: TextStyle(color: context.iron.dim)),
    );
  }
}

/// A rule's collapsed one-liner: `key value(+N)` per condition, keys in
/// mono + dim, values in text colour; preserved extra keys by name only. An
/// [OpaqueRule] says it is preserved.
class RuleSummary extends StatelessWidget {
  const RuleSummary(this.rule, {super.key});

  final RuleDraft rule;

  @override
  Widget build(BuildContext context) {
    final t = context.iron;
    final rule = this.rule;
    if (rule is! FlatRule) {
      return Text(
        'logical / advanced rule — preserved; edit raw via Edit as JSON',
        style: TextStyle(color: t.dim),
        overflow: TextOverflow.ellipsis,
      );
    }
    final spans = <InlineSpan>[];
    void add(String key, String value) {
      if (spans.isNotEmpty) {
        spans.add(TextSpan(text: '  ·  ', style: TextStyle(color: t.faint)));
      }
      spans.add(TextSpan(text: key, style: context.mono(12, color: t.dim)));
      if (value.isNotEmpty) {
        spans.add(TextSpan(text: ' $value', style: TextStyle(color: t.text)));
      }
    }

    for (final c in rule.conds) {
      add(c.key, switch (c.kind) {
        'bool' => '${c.boolValue}',
        'string' || 'number' => c.value.trim(),
        _ when c.items.length > 1 => '${c.items.first} +${c.items.length - 1}',
        _ => c.items.firstOrNull ?? '',
      });
    }
    for (final key in rule.extra.keys) {
      add(key, '');
    }
    if (spans.isEmpty) {
      return Text('no conditions', style: TextStyle(color: t.faint));
    }
    return Text.rich(TextSpan(children: spans),
        maxLines: 1, overflow: TextOverflow.ellipsis);
  }
}
