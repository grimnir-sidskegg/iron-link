import 'dart:convert';

import 'package:flutter/material.dart';

import '../model/routing_draft.dart';
import '../wire/wire.dart';
import 'routing_widgets.dart';
import 'theme/app_colors.dart';
import 'theme/app_typography.dart';
import 'widgets/iron_widgets.dart';

/// The full-screen routing form editor — schema-driven: rows render from the
/// daemon's [RoutingSchema] and the document round-trips through
/// [RoutingDraft] (logical rules and non-route action targets are preserved
/// verbatim and rendered read-only).
///
/// Rules are a reorderable list of one-line summaries; a click expands one
/// into its form. Which rules are expanded is page state, never the draft.
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

  /// The profile's nodes and auto groups for the Node target picker, in
  /// daemon order.
  final List<NodeRef> nodes;

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

/// Width of the condition-key column in an expanded rule.
const double _keyWidth = 148;

class _RoutingEditorPageState extends State<RoutingEditorPage> {
  RoutingDraft get _draft => widget.draft;

  // Pinned at open: the name field edits the draft without rebuilding.
  late final String _title = _draft.id == null
      ? 'New routing'
      : 'Edit routing: ${_draft.name}';

  final Set<RuleDraft> _expanded = {};
  bool _ruleSetsOpen = false;

  /// A just-added rule that scrolls itself into view on its first build.
  RuleDraft? _reveal;

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

  void _addRule() {
    final rule = FlatRule(target: TargetDraft.defaultFor(widget.schema));
    setState(() {
      _draft.rules.add(rule);
      _expanded.add(rule);
      _reveal = rule;
    });
  }

  @override
  Widget build(BuildContext context) {
    final draft = _draft;
    final t = context.iron;
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
      body: ReorderableListView(
        padding: const EdgeInsets.fromLTRB(24, 16, 24, 24),
        buildDefaultDragHandles: false,
        onReorderItem: (from, to) =>
            setState(() => draft.rules.insert(to, draft.rules.removeAt(from))),
        header: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            if (draft.error != null)
              Padding(
                padding: const EdgeInsets.only(bottom: 12),
                child: IronCard(
                  tinted: t.danger,
                  radius: 12,
                  padding: const EdgeInsets.all(12),
                  child: Row(
                    children: [
                      Icon(Icons.error_outline, size: 18, color: t.danger),
                      const SizedBox(width: 8),
                      Expanded(
                        child: Text(draft.error!,
                            style: TextStyle(color: t.danger)),
                      ),
                    ],
                  ),
                ),
              ),
            ConstrainedBox(
              constraints: const BoxConstraints(maxWidth: 360),
              child: TextFormField(
                initialValue: draft.name,
                decoration:
                    const InputDecoration(isDense: true, labelText: 'Name'),
                onChanged: (v) => draft.name = v,
              ),
            ),
            const SizedBox(height: 20),
            Text('Rules (${draft.rules.length})',
                style: Theme.of(context).textTheme.titleMedium),
            const SizedBox(height: 2),
            Text(
              'Each rule routes traffic matching ALL its conditions to its '
              'target; rules apply top-down — drag ⠿ to reorder.',
              style: Theme.of(context)
                  .textTheme
                  .bodySmall
                  ?.copyWith(color: t.dim),
            ),
            const SizedBox(height: 8),
          ],
        ),
        footer: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            const SizedBox(height: 6),
            IronButton(label: 'Add rule', icon: Icons.add, onPressed: _addRule),
            const SizedBox(height: 10),
            IronCard(
              radius: 12,
              padding: const EdgeInsets.fromLTRB(16, 8, 12, 8),
              child: Row(
                children: [
                  Text('Everything else',
                      style: Theme.of(context).textTheme.titleSmall),
                  const SizedBox(width: 10),
                  Icon(Icons.arrow_forward, size: 16, color: t.dim),
                  const SizedBox(width: 10),
                  _targetPill(draft.defaultTarget, editable: true),
                ],
              ),
            ),
            const SizedBox(height: 24),
            GestureDetector(
              onTap: () => setState(() => _ruleSetsOpen = !_ruleSetsOpen),
              child: Row(
                children: [
                  Text('Rule-sets (${draft.ruleSets.length})',
                      style: Theme.of(context).textTheme.titleMedium),
                  Icon(_ruleSetsOpen ? Icons.expand_less : Icons.expand_more,
                      color: t.dim),
                ],
              ),
            ),
            if (_ruleSetsOpen) ...[
              for (final rs in draft.ruleSets) _ruleSetCard(rs),
              const SizedBox(height: 6),
              IronButton(
                label: 'Add rule-set',
                icon: Icons.add,
                onPressed: () =>
                    setState(() => draft.ruleSets.add(RuleSetDraft())),
              ),
            ],
          ],
        ),
        children: [
          for (var i = 0; i < draft.rules.length; i++) _ruleItem(i),
        ],
      ),
    );
  }

  /// One rule: a one-line header (drag handle, number, condition summary,
  /// target, expand chevron, delete) over the form when expanded.
  Widget _ruleItem(int i) {
    final rule = _draft.rules[i];
    final open = _expanded.contains(rule);
    final t = context.iron;
    return Padding(
      key: ObjectKey(rule),
      padding: const EdgeInsets.symmetric(vertical: 3),
      child: Builder(builder: (itemContext) {
        if (identical(rule, _reveal)) {
          _reveal = null;
          WidgetsBinding.instance.addPostFrameCallback((_) {
            if (!itemContext.mounted) return;
            Scrollable.ensureVisible(itemContext,
                duration: const Duration(milliseconds: 200),
                alignmentPolicy:
                    ScrollPositionAlignmentPolicy.keepVisibleAtEnd);
          });
        }
        return IronCard(
          radius: 12,
          padding: EdgeInsets.zero,
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              GestureDetector(
                behavior: HitTestBehavior.opaque,
                onTap: () => setState(
                    () => open ? _expanded.remove(rule) : _expanded.add(rule)),
                child: Row(
                  children: [
                    ReorderableDragStartListener(
                      index: i,
                      child: MouseRegion(
                        cursor: SystemMouseCursors.grab,
                        child: Padding(
                          padding: const EdgeInsets.fromLTRB(8, 10, 4, 10),
                          child: Icon(Icons.drag_indicator,
                              size: 18, color: t.faint),
                        ),
                      ),
                    ),
                    SizedBox(
                      width: 28,
                      child: Text('${i + 1}',
                          style: context.mono(12.5, color: t.dim)),
                    ),
                    Expanded(child: RuleSummary(rule)),
                    const SizedBox(width: 8),
                    if (rule is FlatRule)
                      _targetPill(rule.target, editable: open),
                    const SizedBox(width: 4),
                    Icon(open ? Icons.expand_less : Icons.expand_more,
                        color: t.dim),
                    IconButton(
                      tooltip: 'Remove rule',
                      iconSize: 18,
                      color: t.dim,
                      onPressed: () => setState(() {
                        _draft.rules.remove(rule);
                        _expanded.remove(rule);
                      }),
                      icon: const Icon(Icons.delete_outline),
                    ),
                  ],
                ),
              ),
              if (open)
                Padding(
                  padding: const EdgeInsets.fromLTRB(16, 0, 8, 10),
                  child: switch (rule) {
                    final FlatRule r => Column(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          for (final cond in r.conds) _condRow(r, cond),
                          _addConditionMenu(r),
                        ],
                      ),
                    OpaqueRule(:final raw) => SelectableText(
                        const JsonEncoder.withIndent('  ').convert(raw),
                        style: context.mono(12, color: t.text),
                      ),
                  },
                ),
            ],
          ),
        );
      }),
    );
  }

  /// A target as a coloured pill labelled with the schema token verbatim (a
  /// Node target shows the node's name). [editable] makes it a menu over
  /// `schema.targets` in which the Node token expands into the profile's
  /// nodes and auto groups. An [OpaqueTarget] is a read-only preserved label.
  /// Width-capped and ellipsized: a long node name must not push the rule's
  /// chevron and delete button off the row.
  Widget _targetPill(TargetDraft target, {required bool editable}) {
    final pill = RouteTargetPill(target, nodes: widget.nodes);
    if (!editable || target is! RouteTarget) return pill;
    final t = context.iron;
    Color color(String kind) => routeTargetColor(context, kind);
    // A kind the schema does not list (a future daemon) must still render.
    final kinds = [
      if (!widget.schema.targets.contains(target.kind)) target.kind,
      ...widget.schema.targets,
    ];
    return ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: 240),
        child: PopupMenuButton<(String, String?)>(
          tooltip: 'Change target',
          onSelected: (v) => setState(() {
            target.kind = v.$1;
            target.node = v.$2;
          }),
          itemBuilder: (context) => [
            for (final k in kinds)
              if (k != nodeToken)
                PopupMenuItem(
                    value: (k, null), child: IronTag(k, color: color(k)))
              else ...[
                PopupMenuItem(
                  enabled: false,
                  height: 28,
                  child: Text(
                      widget.nodes.isEmpty
                          ? '$nodeToken (no nodes)'
                          : nodeToken,
                      style: context.mono(11.5, color: t.faint)),
                ),
                for (final n in widget.nodes)
                  PopupMenuItem(
                    value: (nodeToken, n.id),
                    child: Row(
                      mainAxisSize: MainAxisSize.min,
                      children: [
                        Flexible(
                            child: IronTag(n.name, color: color(nodeToken))),
                        if (n.isGroup) ...[
                          const SizedBox(width: 6),
                          const IronKindChip('auto', accent: true),
                        ],
                      ],
                    ),
                  ),
              ],
          ],
          child: Row(
            mainAxisSize: MainAxisSize.min,
            children: [
              Flexible(child: pill),
              Icon(Icons.arrow_drop_down, size: 18, color: t.dim),
            ],
          ),
        ));
  }

  // No `supported` check here on purpose: an existing condition renders even
  // when it cannot match on this daemon's platform — only the add menu
  // filters.
  Widget _condRow(FlatRule rule, CondDraft cond) {
    final t = context.iron;
    final browsable = widget.pickFile != null &&
        (cond.key == 'process_path' || cond.key == 'process_name');
    final input = switch (cond.kind) {
      'bool' => Checkbox(
          value: cond.boolValue,
          onChanged: (v) => setState(() => cond.boolValue = v ?? false),
        ),
      'string' || 'number' => ConstrainedBox(
          constraints: const BoxConstraints(maxWidth: 220),
          child: TextFormField(
            initialValue: cond.value,
            decoration:
                InputDecoration(isDense: true, hintText: _hintFor(cond.key)),
            onChanged: (v) => setState(() => cond.value = v),
          ),
        ),
      // Array kinds ("strings" / "ports" / "numbers" / unknown).
      _ => _ChipsInput(
          cond: cond,
          hint: _hintFor(cond.key),
          onChanged: () => setState(() {}),
          onBrowse: browsable ? () => _browse(cond) : null,
        ),
    };
    return Padding(
      key: ObjectKey(cond),
      padding: const EdgeInsets.symmetric(vertical: 2),
      child: Row(
        children: [
          SizedBox(
            width: _keyWidth,
            child: Text(cond.key,
                overflow: TextOverflow.ellipsis,
                style: context.mono(12, color: t.dim)),
          ),
          const SizedBox(width: 8),
          Expanded(
            child: Align(alignment: Alignment.centerLeft, child: input),
          ),
          IconButton(
            tooltip: 'Remove condition',
            iconSize: 18,
            color: t.dim,
            onPressed: () => setState(() => rule.conds.remove(cond)),
            icon: const Icon(Icons.close),
          ),
        ],
      ),
    );
  }

  /// Picks a file into a new chip: the basename for `process_name` (the
  /// daemon matches bare executable names), the full path otherwise.
  Future<void> _browse(CondDraft cond) async {
    final path = await widget.pickFile!();
    if (path == null || !mounted) return;
    setState(() => cond.items.add(cond.key == 'process_name'
        ? path.split(RegExp(r'[/\\]')).last
        : path));
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
    final accent = context.iron.accentStrong;
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
        padding: const EdgeInsets.symmetric(vertical: 6),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            Icon(Icons.add, size: 18, color: accent),
            const SizedBox(width: 4),
            Text('Add condition', style: TextStyle(color: accent)),
          ],
        ),
      ),
    );
  }

  Widget _ruleSetCard(RuleSetDraft rs) {
    return Padding(
      key: ObjectKey(rs),
      padding: const EdgeInsets.symmetric(vertical: 4),
      child: IronCard(
        radius: 12,
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

/// An array condition's editor: one chip per item plus ONE inline input.
/// Enter or losing focus commits the trimmed input as a chip; tapping a chip
/// moves it back into the input (and, committed, back to its place); ×
/// removes it. The input mirrors into [CondDraft.pending] / `pendingAt`, so
/// uncommitted text still saves, in order.
class _ChipsInput extends StatefulWidget {
  const _ChipsInput({
    required this.cond,
    required this.hint,
    required this.onChanged,
    this.onBrowse,
  });

  final CondDraft cond;
  final String hint;

  /// Called after the chip list changed (the rule summary re-renders).
  final VoidCallback onChanged;
  final VoidCallback? onBrowse;

  @override
  State<_ChipsInput> createState() => _ChipsInputState();
}

class _ChipsInputState extends State<_ChipsInput> {
  late final _text = TextEditingController(text: widget.cond.pending);
  final _focus = FocusNode();

  @override
  void initState() {
    super.initState();
    _focus.addListener(() {
      if (mounted && !_focus.hasFocus) _commit();
    });
  }

  @override
  void dispose() {
    _focus.dispose();
    _text.dispose();
    super.dispose();
  }

  /// Commits the input as a chip; returns the index it went in at (null =
  /// nothing to commit).
  int? _commit() {
    final value = _text.text.trim();
    if (value.isEmpty) return null;
    final cond = widget.cond;
    final at = cond.pendingIndex;
    cond.items.insert(at, value);
    cond.pending = '';
    cond.pendingAt = null;
    _text.clear();
    widget.onChanged();
    return at;
  }

  void _edit(int i) {
    final at = _commit();
    if (at != null && at <= i) i++;
    widget.cond.pendingAt = i;
    final value = widget.cond.items.removeAt(i);
    widget.cond.pending = _text.text = value;
    _focus.requestFocus();
    widget.onChanged();
  }

  @override
  Widget build(BuildContext context) {
    final t = context.iron;
    final items = widget.cond.items;
    // One tap region with the input: clicking a chip or its × is not a "tap
    // outside" that would unfocus (and commit) under the chip's stale index.
    return TextFieldTapRegion(
      child: Wrap(
        spacing: 6,
        runSpacing: 4,
        crossAxisAlignment: WrapCrossAlignment.center,
        children: [
          for (var i = 0; i < items.length; i++)
            InputChip(
              label: Text(items[i],
                  maxLines: 1, overflow: TextOverflow.ellipsis),
              labelStyle: TextStyle(color: t.text, fontSize: 12.5),
              backgroundColor: t.raised,
              side: BorderSide(color: t.border),
              visualDensity: VisualDensity.compact,
              materialTapTargetSize: MaterialTapTargetSize.shrinkWrap,
              onPressed: () => _edit(i),
              deleteIconColor: t.dim,
              deleteButtonTooltipMessage: 'Remove value',
              onDeleted: () {
                items.removeAt(i);
                final at = widget.cond.pendingAt;
                if (at != null && i < at) widget.cond.pendingAt = at - 1;
                widget.onChanged();
              },
            ),
          SizedBox(
            width: 220,
            child: TextField(
              controller: _text,
              focusNode: _focus,
              decoration: InputDecoration(isDense: true, hintText: widget.hint),
              onChanged: (v) => widget.cond.pending = v,
              // Keep focus after Enter so several values go in in a row.
              onEditingComplete: () {},
              onSubmitted: (_) => _commit(),
            ),
          ),
          if (widget.onBrowse != null)
            IconButton(
              tooltip: 'Browse…',
              iconSize: 18,
              color: t.dim,
              onPressed: widget.onBrowse,
              icon: const Icon(Icons.folder_open),
            ),
        ],
      ),
    );
  }
}
