import 'dart:convert';

import 'package:file_selector/file_selector.dart';
import 'package:flutter/material.dart';

import '../app/session.dart';
import '../ipc/client.dart';
import '../model/routing_draft.dart';
import '../wire/wire.dart';
import 'app.dart';
import 'routing_editor_page.dart';
import 'theme/app_colors.dart';
import 'theme/app_typography.dart';
import 'widgets/iron_widgets.dart';

/// Routing configs: list / select / remove, plus the schema-driven form
/// editor ([RoutingEditorPage]) over the daemon's `routing_schema` +
/// `get_routing`. Raw JSON stays as the secondary path — per-row
/// "Edit as JSON" and a header "New from JSON" — for documents the form
/// only preserves opaquely.
class RoutingPage extends StatefulWidget {
  const RoutingPage({super.key, required this.client, required this.session});

  final DaemonClient client;
  final DaemonSession session;

  @override
  State<RoutingPage> createState() => _RoutingPageState();
}

class _RoutingPageState extends State<RoutingPage> {
  List<RoutingInfo>? _routing;
  String? _error;
  String? _selecting; // id of the routing being selected (restart in flight)

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    try {
      final routing = await widget.client.listRouting();
      if (!mounted) return;
      setState(() {
        _routing = routing;
        _error = null;
      });
    } on ClientException catch (e) {
      if (!mounted) return;
      setState(() => _error = e.message);
    }
  }

  /// Fetches what the editor needs (the schema, the node list for the Node
  /// target picker, and — when editing — the full document), then pushes the
  /// form editor. A `true` pop result means a save happened.
  Future<void> _openEditor([RoutingInfo? info]) async {
    final loaded = await guard(context, () async {
      final schema = await widget.client.routingSchema();
      final nodes = await widget.client.listNodes();
      final doc = info == null ? null : await widget.client.getRouting(info.id);
      return (schema: schema, nodes: nodes, doc: doc);
    });
    if (loaded == null || !mounted) return;
    final draft = loaded.doc == null
        ? RoutingDraft.empty()
        : RoutingDraft.fromValue(loaded.doc, loaded.schema);
    final saved = await Navigator.of(context).push<bool>(MaterialPageRoute(
      builder: (_) => RoutingEditorPage(
        draft: draft,
        schema: loaded.schema,
        nodes: [for (final n in loaded.nodes) (id: n.id, name: n.name)],
        onSave: (doc) =>
            guardOk(context, () => widget.client.upsertRouting(doc)),
        // The picker only yields a path STRING for the config; the daemon —
        // not the client — is what (never) reads the file.
        pickFile: () async => (await openFile())?.path,
      ),
    ));
    if (saved == true && mounted) {
      showSnack(context, 'Routing config saved');
      _load();
    }
  }

  Future<void> _newFromJson() async {
    final config = await _promptRoutingJson(context);
    if (config == null || !mounted) return;
    await _upsert(config);
  }

  Future<void> _editAsJson(RoutingInfo info) async {
    final doc = await guard(context, () => widget.client.getRouting(info.id));
    if (doc == null || !mounted) return;
    final config = await _promptRoutingJson(context,
        initial: const JsonEncoder.withIndent('  ').convert(doc));
    if (config == null || !mounted) return;
    await _upsert(config);
  }

  Future<void> _upsert(Map<String, Object?> config) async {
    if (await guardOk(context, () => widget.client.upsertRouting(config))) {
      if (mounted) showSnack(context, 'Routing config saved');
      _load();
    }
  }

  /// Selects [info] as the active routing. Idle: just persist the default.
  /// Running: `select_routing` only persists server-side, so changing routing
  /// needs a reactivation — warn, then restart the session onto the new
  /// routing immediately (bare `activate` re-applies the persisted context).
  Future<void> _select(RoutingInfo info) async {
    if (_selecting != null || info.active) return;
    if (!widget.session.isRunning) {
      if (await guardOk(context, () => widget.client.selectRouting(info.id))) {
        _load();
      }
      return;
    }
    if (!await confirm(
        context,
        'Switch routing',
        'Switching to "${info.name}" will restart the connection onto the '
            'new routing. Continue?')) {
      return;
    }
    if (!mounted) return;
    setState(() => _selecting = info.id);
    final tun = widget.session.active?.tun ?? true;
    if (await guardOk(context, () => widget.client.selectRouting(info.id)) &&
        mounted &&
        await guardOk(context, () => widget.client.activate(tun: tun))) {
      await widget.session.refreshStatus();
      if (mounted) showSnack(context, 'Switched to "${info.name}"');
    }
    if (mounted) setState(() => _selecting = null);
    _load();
  }

  @override
  Widget build(BuildContext context) {
    final t = context.iron;
    final routing = _routing;
    return Scaffold(
      backgroundColor: t.panel,
      floatingActionButton: FloatingActionButton.extended(
        onPressed: () => _openEditor(),
        icon: const Icon(Icons.add),
        label: const Text('New routing'),
      ),
      body: ListView(
        padding: const EdgeInsets.fromLTRB(24, 16, 24, 88),
        children: [
          // Title row: the screen title + one-line intent, with the two
          // header actions (New from JSON / Reload) as round icon buttons.
          Row(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text('Routing', style: context.screenTitle),
                    const SizedBox(height: 2),
                    Text('Decide what goes through the tunnel.',
                        style: Theme.of(context)
                            .textTheme
                            .bodyMedium
                            ?.copyWith(color: t.dim)),
                  ],
                ),
              ),
              const SizedBox(width: 8),
              IronIconButton(
                  icon: Icons.data_object,
                  onPressed: _newFromJson,
                  tooltip: 'New from JSON'),
              const SizedBox(width: 8),
              IronIconButton(
                  icon: Icons.refresh,
                  onPressed: _load,
                  tooltip: 'Reload'),
            ],
          ),
          if (_error != null)
            Padding(
              padding: const EdgeInsets.only(top: 16),
              child: IronCard(
                tinted: t.danger,
                child: Text(_error!, style: TextStyle(color: t.danger)),
              ),
            ),
          const SizedBox(height: 16),
          if (routing == null)
            const Padding(
              padding: EdgeInsets.only(top: 48),
              child: Center(child: CircularProgressIndicator()),
            )
          else if (routing.isEmpty)
            Padding(
              padding: const EdgeInsets.only(top: 48),
              child: Center(
                  child: Text('No routing configs.',
                      style: TextStyle(color: t.dim))),
            )
          else
            for (var i = 0; i < routing.length; i++) ...[
              if (i > 0) const SizedBox(height: 10),
              _row(routing[i]),
            ],
        ],
      ),
    );
  }

  Widget _row(RoutingInfo info) {
    final t = context.iron;
    final selecting = _selecting == info.id;
    return IronCard(
      // Tap the card to select — idle persists the default, a running session
      // restarts onto the new routing (see _select). The active config (or any
      // row while a selection is in flight) is not tappable.
      onTap: info.active || _selecting != null ? null : () => _select(info),
      child: Row(
        children: [
          // Leading affordance: a spinner while this row's select is in
          // flight, otherwise the route tile (accent when active, dim when not).
          selecting
              ? const SizedBox(
                  width: 38,
                  height: 38,
                  child: Padding(
                    padding: EdgeInsets.all(7),
                    child: CircularProgressIndicator(strokeWidth: 2),
                  ),
                )
              : IronIconTile(Icons.alt_route,
                  color: info.active ? t.accent : t.dim),
          const SizedBox(width: 12),
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              mainAxisSize: MainAxisSize.min,
              children: [
                Row(
                  children: [
                    Flexible(
                      child: Text(info.name,
                          overflow: TextOverflow.ellipsis,
                          style: Theme.of(context).textTheme.titleMedium),
                    ),
                    if (info.active) ...[
                      const SizedBox(width: 8),
                      IronTag('Active', color: t.accent),
                    ],
                  ],
                ),
                const SizedBox(height: 3),
                Text('${info.rules} rules · ${info.ruleSets} rule sets',
                    style: context.monoBodySmall?.copyWith(color: t.dim)),
              ],
            ),
          ),
          const SizedBox(width: 8),
          IronIconButton(
            icon: Icons.edit_outlined,
            tooltip: 'Edit',
            onPressed: () => _openEditor(info),
          ),
          const SizedBox(width: 6),
          IronIconButton(
            icon: Icons.data_object,
            tooltip: 'Edit as JSON',
            onPressed: () => _editAsJson(info),
          ),
          const SizedBox(width: 6),
          IronIconButton(
            icon: Icons.delete_outline,
            tooltip: 'Remove',
            color: t.danger,
            onPressed: () async {
              if (await confirm(context, 'Remove routing config',
                  'Remove "${info.name}"?')) {
                if (!mounted) return;
                if (await guardOk(
                    context, () => widget.client.removeRouting(info.id))) {
                  _load();
                }
              }
            },
          ),
        ],
      ),
    );
  }
}

Future<Map<String, Object?>?> _promptRoutingJson(BuildContext context,
    {String? initial}) {
  final controller = TextEditingController(text: initial);
  String? parseError;
  return showDialog<Map<String, Object?>>(
    context: context,
    builder: (context) => StatefulBuilder(
      builder: (context, setState) => AlertDialog(
        title: Text(
            initial == null ? 'New routing from JSON' : 'Edit routing as JSON'),
        content: SizedBox(
          width: 560,
          child: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              TextField(
                controller: controller,
                autofocus: true,
                maxLines: 14,
                style: const TextStyle(fontFamily: 'monospace', fontSize: 13),
                decoration: const InputDecoration(
                  border: OutlineInputBorder(),
                  hintText: '{"name": "basic", "rule_sets": [], "rules": [], '
                      '"default_target": "DefaultProxy"}',
                ),
              ),
              if (parseError != null)
                Padding(
                  padding: const EdgeInsets.only(top: 8),
                  child: Text(parseError!,
                      style: TextStyle(
                          color: Theme.of(context).colorScheme.error)),
                ),
            ],
          ),
        ),
        actions: [
          TextButton(
              onPressed: () => Navigator.pop(context),
              child: const Text('Cancel')),
          FilledButton(
            onPressed: () {
              try {
                final decoded = jsonDecode(controller.text);
                if (decoded is! Map<String, Object?>) {
                  setState(() => parseError = 'Not a JSON object');
                  return;
                }
                Navigator.pop(context, decoded);
              } on FormatException catch (e) {
                setState(() => parseError = e.message);
              }
            },
            child: const Text('Save'),
          ),
        ],
      ),
    ),
  );
}
