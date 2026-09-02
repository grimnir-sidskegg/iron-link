import 'dart:convert';

import 'package:flutter/material.dart';

import '../model/routing_draft.dart';
import '../wire/wire.dart';
import 'theme/app_typography.dart';

/// The full-screen routing form editor — schema-driven: rows render from the
/// daemon's [RoutingSchema] and the document round-trips through
/// [RoutingDraft] (logical rules and non-route action targets are preserved
/// verbatim and rendered read-only).
///
/// Pure local state over pre-loaded data: the caller fetches the schema /
/// document / node list and owns the save wire call via [onSave] — the page
/// itself never talks to the daemon (and is widget-testable without one).
class RoutingEditorPage extends StatefulWidget {
  const RoutingEditorPage({
    super.key,
    required this.draft,
    required this.schema,
    required this.nodes,
    required this.onSave,
    this.pickFile,
  });

  final RoutingDraft draft;
  final RoutingSchema schema;

  /// The profile's nodes for the Node target picker, in daemon order.
  final List<({String id, String name})> nodes;

  /// Opens the system file picker and resolves to the picked path (null =
  /// cancelled). Injected — never the plugin directly — so the page stays
  /// widget-testable; null hides the browse buttons entirely.
  final Future<String?> Function()? pickFile;

  /// Persists the built document. True = saved (the page pops with `true`);
  /// false = failed (the callback surfaces the error, the page stays).
  final Future<bool> Function(Map<String, Object?> doc) onSave;

  @override
  State<RoutingEditorPage> createState() => _RoutingEditorPageState();
}

class _RoutingEditorPageState extends State<RoutingEditorPage> {
  RoutingDraft get _draft => widget.draft;

  // Pinned at open: the name field edits the draft without rebuilding.
  late final String _title = _draft.id == null
      ? 'New routing'
      : 'Edit routing: ${_draft.name}';

  Future<void> _save() async {
    final Map<String, Object?> doc;
    try {
      doc = _draft.toValue();
    } on RoutingDraftError catch (e) {
      setState(() => _draft.error = e.message);
      return;
    }
    setState(() => _draft.error = null);
    final ok = await widget.onSave(doc);
    if (ok && mounted) Navigator.of(context).pop(true);
  }

  Color get _outline => Theme.of(context).colorScheme.outline;

  @override
  Widget build(BuildContext context) {
    final draft = _draft;
    return Scaffold(
      appBar: AppBar(
        title: Text(_title),
        actions: [
          FilledButton.icon(
            onPressed: _save,
            icon: const Icon(Icons.save),
            label: const Text('Save'),
          ),
          const SizedBox(width: 12),
        ],
      ),
      body: ListView(
        padding: const EdgeInsets.fromLTRB(24, 16, 24, 24),
        children: [
          if (draft.error != null)
            Card(
              color: Theme.of(context).colorScheme.errorContainer,
              margin: const EdgeInsets.only(bottom: 12),
              child: Padding(
                padding: const EdgeInsets.all(12),
                child: Row(
                  children: [
                    Icon(Icons.error_outline,
                        size: 18,
                        color:
                            Theme.of(context).colorScheme.onErrorContainer),
                    const SizedBox(width: 8),
                    Expanded(
                      child: Text(draft.error!,
                          style: TextStyle(
                              color: Theme.of(context)
                                  .colorScheme
                                  .onErrorContainer)),
                    ),
                  ],
                ),
              ),
            ),
          TextFormField(
            initialValue: draft.name,
            decoration: const InputDecoration(labelText: 'Name'),
            onChanged: (v) => draft.name = v,
          ),
          const SizedBox(height: 16),
          _header(
            'Rules (${draft.rules.length})',
            'Add rule',
            () => setState(() => draft.rules
                .add(FlatRule(target: TargetDraft.defaultFor(widget.schema)))),
          ),
          Text(
            'Each rule routes traffic matching ALL its conditions to its '
            'target; rules apply top-down.',
            style: Theme.of(context)
                .textTheme
                .bodySmall
                ?.copyWith(color: _outline),
          ),
          for (var i = 0; i < draft.rules.length; i++)
            switch (draft.rules[i]) {
              final FlatRule rule => _flatRuleCard(i, rule),
              final OpaqueRule rule => _opaqueRuleCard(i, rule),
            },
          const SizedBox(height: 16),
          Row(
            children: [
              Text('Default (anything not matched above)',
                  style: Theme.of(context).textTheme.titleSmall),
              const SizedBox(width: 16),
              Expanded(child: _targetPicker(draft.defaultTarget)),
            ],
          ),
          const SizedBox(height: 16),
          _header(
            'Rule-sets (${draft.ruleSets.length})',
            'Add rule-set',
            () => setState(() => draft.ruleSets.add(RuleSetDraft())),
          ),
          for (final rs in draft.ruleSets) _ruleSetCard(rs),
        ],
      ),
    );
  }

  Widget _header(String title, String addLabel, VoidCallback onAdd) {
    return Row(
      children: [
        Text(title, style: Theme.of(context).textTheme.titleMedium),
        const Spacer(),
        TextButton.icon(
          onPressed: onAdd,
          icon: const Icon(Icons.add),
          label: Text(addLabel),
        ),
      ],
    );
  }

  Widget _flatRuleCard(int i, FlatRule rule) {
    return Card(
      key: ObjectKey(rule),
      margin: const EdgeInsets.symmetric(vertical: 6),
      child: Padding(
        padding: const EdgeInsets.fromLTRB(16, 8, 8, 8),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Text('Rule ${i + 1}',
                    style: Theme.of(context).textTheme.titleSmall),
                const SizedBox(width: 16),
                Expanded(child: _targetPicker(rule.target)),
                IconButton(
                  tooltip: 'Remove rule',
                  onPressed: () => setState(() => _draft.rules.removeAt(i)),
                  icon: const Icon(Icons.delete_outline),
                ),
              ],
            ),
            for (final cond in rule.conds) _condRow(rule, cond),
            Align(
                alignment: Alignment.centerLeft,
                child: _addConditionMenu(rule)),
          ],
        ),
      ),
    );
  }

  Widget _opaqueRuleCard(int i, OpaqueRule rule) {
    return Card(
      key: ObjectKey(rule),
      margin: const EdgeInsets.symmetric(vertical: 6),
      child: ExpansionTile(
        shape: const Border(),
        collapsedShape: const Border(),
        title: Row(
          children: [
            Text('Rule ${i + 1}',
                style: Theme.of(context).textTheme.titleSmall),
            const SizedBox(width: 16),
            Expanded(
              child: Text(
                'logical / advanced rule — preserved; edit raw via '
                'Edit as JSON',
                style: TextStyle(color: _outline),
                overflow: TextOverflow.ellipsis,
              ),
            ),
            IconButton(
              tooltip: 'Remove rule',
              onPressed: () => setState(() => _draft.rules.removeAt(i)),
              icon: const Icon(Icons.delete_outline),
            ),
          ],
        ),
        children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(16, 0, 16, 12),
            child: Align(
              alignment: Alignment.centerLeft,
              child: SelectableText(
                const JsonEncoder.withIndent('  ').convert(rule.raw),
                style: const TextStyle(fontFamily: kMonoFamily, fontSize: 12),
              ),
            ),
          ),
        ],
      ),
    );
  }

  /// The target dropdown over `schema.targets` plus the node picker shown
  /// only for the Node token; switching kinds away from Node clears the
  /// picked node. An [OpaqueTarget] renders as a read-only preserved label.
  Widget _targetPicker(TargetDraft target) {
    if (target is! RouteTarget) {
      return Text('advanced action target (preserved)',
          style: TextStyle(color: _outline));
    }
    // A kind the schema does not list (a future daemon) must still render.
    final kinds = [
      if (!widget.schema.targets.contains(target.kind)) target.kind,
      ...widget.schema.targets,
    ];
    return Row(
      children: [
        DropdownButton<String>(
          value: target.kind,
          items: [
            for (final k in kinds) DropdownMenuItem(value: k, child: Text(k)),
          ],
          onChanged: (v) => setState(() {
            if (v == null) return;
            target.kind = v;
            if (v != nodeToken) target.node = null;
          }),
        ),
        if (target.kind == nodeToken) ...[
          const SizedBox(width: 12),
          Flexible(
            child: ConstrainedBox(
              constraints: const BoxConstraints(maxWidth: 280),
              child: DropdownButton<String>(
                isExpanded: true,
                // A stored id missing from the profile renders as unpicked.
                value: widget.nodes.any((n) => n.id == target.node)
                    ? target.node
                    : null,
                hint: const Text('(pick node)'),
                items: [
                  for (final n in widget.nodes)
                    DropdownMenuItem(
                      value: n.id,
                      child: Text(n.name, overflow: TextOverflow.ellipsis),
                    ),
                ],
                onChanged: (v) => setState(() => target.node = v),
              ),
            ),
          ),
        ],
      ],
    );
  }

  // No `supported` check here on purpose: an existing condition renders even
  // when it cannot match on this daemon's platform — only the add menu
  // filters.
  Widget _condRow(FlatRule rule, CondDraft cond) {
    final input = switch (cond.kind) {
      'bool' => Checkbox(
          value: cond.boolValue,
          onChanged: (v) => setState(() => cond.boolValue = v ?? false),
        ),
      'string' || 'number' => _condField(cond),
      // Array kinds ("strings" / "ports" / "numbers" / unknown): a vertical
      // per-item list.
      _ => null,
    };
    if (input == null) return _arrayCond(rule, cond);
    return Padding(
      key: ObjectKey(cond),
      padding: const EdgeInsets.symmetric(vertical: 2),
      child: Row(
        children: [
          ConstrainedBox(
            constraints: const BoxConstraints(minWidth: 160),
            child: Text(cond.key),
          ),
          const SizedBox(width: 12),
          Expanded(
            child: Align(alignment: Alignment.centerLeft, child: input),
          ),
          _removeCondButton(rule, cond),
        ],
      ),
    );
  }

  Widget _condField(CondDraft cond) {
    return ConstrainedBox(
      constraints: const BoxConstraints(maxWidth: 220),
      child: TextFormField(
        initialValue: cond.value,
        decoration: InputDecoration(isDense: true, hintText: _hintFor(cond.key)),
        onChanged: (v) => cond.value = v,
      ),
    );
  }

  Widget _removeCondButton(FlatRule rule, CondDraft cond) {
    return IconButton(
      tooltip: 'Remove condition',
      iconSize: 18,
      onPressed: () => setState(() => rule.conds.remove(cond)),
      icon: const Icon(Icons.close),
    );
  }

  /// An array-kind condition: the key row, one dense field per item, and an
  /// "add value" affordance (zero items render just the affordance — the
  /// "has no values" validation fires on save).
  Widget _arrayCond(FlatRule rule, CondDraft cond) {
    final hint = _hintFor(cond.key);
    final browsable = widget.pickFile != null &&
        (cond.key == 'process_path' || cond.key == 'process_name');
    return Padding(
      key: ObjectKey(cond),
      padding: const EdgeInsets.symmetric(vertical: 2),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Expanded(child: Text(cond.key)),
              _removeCondButton(rule, cond),
            ],
          ),
          for (final item in cond.items)
            Padding(
              // Keyed by the item wrapper: removing a middle item must not
              // shift the remaining fields' edit state.
              key: ObjectKey(item),
              padding: const EdgeInsets.only(left: 16, bottom: 4),
              child: Row(
                children: [
                  Expanded(
                    child: TextFormField(
                      initialValue: item.value,
                      decoration:
                          InputDecoration(isDense: true, hintText: hint),
                      onChanged: (v) => item.value = v,
                    ),
                  ),
                  if (browsable)
                    IconButton(
                      tooltip: 'Browse…',
                      iconSize: 18,
                      onPressed: () => _browse(cond, item),
                      icon: const Icon(Icons.folder_open),
                    ),
                  IconButton(
                    tooltip: 'Remove value',
                    iconSize: 18,
                    onPressed: () =>
                        setState(() => cond.items.remove(item)),
                    icon: const Icon(Icons.close),
                  ),
                ],
              ),
            ),
          Padding(
            padding: const EdgeInsets.only(left: 8),
            child: TextButton.icon(
              onPressed: () => setState(() => cond.items.add(CondItem())),
              icon: const Icon(Icons.add, size: 18),
              label: const Text('add value'),
            ),
          ),
        ],
      ),
    );
  }

  /// Picks a file into [item]: the basename for `process_name` (the daemon
  /// matches bare executable names), the full path otherwise. The wrapper is
  /// REPLACED rather than mutated so the ObjectKey-ed field rebuilds and
  /// displays the picked value.
  Future<void> _browse(CondDraft cond, CondItem item) async {
    final path = await widget.pickFile!();
    if (path == null || !mounted) return;
    final value = cond.key == 'process_name'
        ? path.split(RegExp(r'[/\\]')).last
        : path;
    final i = cond.items.indexOf(item);
    if (i < 0) return; // the row was removed while the dialog was up
    setState(() => cond.items[i] = CondItem(value));
  }

  String _hintFor(String key) {
    for (final c in widget.schema.conditions) {
      if (c.key == key) return c.hint;
    }
    return '';
  }

  /// Offers schema conditions that are supported on the daemon's platform
  /// AND not already present on the rule. Picking one seeds an empty draft
  /// of the schema's kind.
  Widget _addConditionMenu(FlatRule rule) {
    final available = [
      for (final spec in widget.schema.conditions)
        if (spec.supported && !rule.conds.any((c) => c.key == spec.key)) spec,
    ];
    final primary = Theme.of(context).colorScheme.primary;
    return PopupMenuButton<ConditionSpec>(
      enabled: available.isNotEmpty,
      tooltip: 'Add condition',
      onSelected: (spec) => setState(
          () => rule.conds.add(CondDraft(key: spec.key, kind: spec.kind))),
      itemBuilder: (context) => [
        for (final spec in available)
          PopupMenuItem(value: spec, child: Text(spec.key)),
      ],
      child: Padding(
        padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 6),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            Icon(Icons.add, size: 18, color: primary),
            const SizedBox(width: 4),
            Text('Add condition', style: TextStyle(color: primary)),
          ],
        ),
      ),
    );
  }

  Widget _ruleSetCard(RuleSetDraft rs) {
    return Card(
      key: ObjectKey(rs),
      margin: const EdgeInsets.symmetric(vertical: 6),
      child: Padding(
        padding: const EdgeInsets.fromLTRB(16, 4, 8, 12),
        child: Column(
          children: [
            Row(
              children: [
                SizedBox(
                  width: 160,
                  child: TextFormField(
                    initialValue: rs.tag,
                    decoration:
                        const InputDecoration(isDense: true, labelText: 'tag'),
                    onChanged: (v) => rs.tag = v,
                  ),
                ),
                const SizedBox(width: 12),
                Expanded(
                  child: TextFormField(
                    initialValue: rs.url,
                    decoration: const InputDecoration(
                        isDense: true, labelText: 'url', hintText: 'https://…'),
                    onChanged: (v) => rs.url = v,
                  ),
                ),
                IconButton(
                  tooltip: 'Remove rule-set',
                  onPressed: () =>
                      setState(() => _draft.ruleSets.remove(rs)),
                  icon: const Icon(Icons.delete_outline),
                ),
              ],
            ),
            const SizedBox(height: 4),
            Row(
              children: [
                Checkbox(
                  value: rs.binary,
                  onChanged: (v) => setState(() => rs.binary = v ?? true),
                ),
                const Text('binary (.srs)'),
                const SizedBox(width: 24),
                SizedBox(
                  width: 200,
                  child: TextFormField(
                    initialValue: rs.detour,
                    decoration: const InputDecoration(
                        isDense: true, labelText: 'detour', hintText: 'none'),
                    onChanged: (v) => rs.detour = v,
                  ),
                ),
              ],
            ),
          ],
        ),
      ),
    );
  }
}
