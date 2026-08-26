/// Shared prompt/report dialogs — moved unchanged (bar visibility) from
/// the former Nodes / Subscriptions / Profiles pages when they merged into
/// HomePage; the profile prompt is also used by the rail's profile menu.
library;

import 'package:flutter/material.dart';

import '../ipc/client.dart';
import '../wire/wire.dart';
import 'theme/app_colors.dart';

/// Prompts for one thing to add — a node share link OR a subscription URL.
/// Resolves to the trimmed string, or null on cancel. The caller routes by
/// scheme: http(s):// is fetched as a subscription, anything else is parsed
/// as a single node.
Future<String?> promptAdd(BuildContext context) {
  final controller = TextEditingController();
  return showDialog<String>(
    context: context,
    builder: (context) => AlertDialog(
      title: const Text('Add'),
      content: SizedBox(
        width: 480,
        child: TextField(
          controller: controller,
          autofocus: true,
          decoration: const InputDecoration(
            labelText: 'Share link or subscription URL',
            hintText: 'vless://…   or   https://…',
            helperText: 'http(s):// is fetched as a subscription; '
                'anything else is parsed as a single node',
          ),
          onSubmitted: (v) => Navigator.pop(context, v.trim()),
        ),
      ),
      actions: [
        TextButton(
            onPressed: () => Navigator.pop(context), child: const Text('Cancel')),
        FilledButton(
            onPressed: () => Navigator.pop(context, controller.text.trim()),
            child: const Text('Add')),
      ],
    ),
  );
}

/// The per-stage `diagnose` verdict report. Opens IMMEDIATELY on a spinner and
/// swaps in the verdict (or the failure) when the probe settles — a diagnose
/// runs every stage to its own budget, so a hung/timing-out node would
/// otherwise leave the screen blank for the whole run before any window shows.
class DiagnosisDialog extends StatelessWidget {
  const DiagnosisDialog({super.key, required this.node, required this.pending});

  /// The node under probe — titles the window while it is still running.
  final String node;

  /// The in-flight `diagnose` call; the dialog is built around it.
  final Future<DiagnosisInfo> pending;

  @override
  Widget build(BuildContext context) {
    return FutureBuilder<DiagnosisInfo>(
      future: pending,
      builder: (context, snap) {
        if (snap.connectionState != ConnectionState.done) {
          return _shell(
            context,
            leading: const SizedBox(
                width: 22,
                height: 22,
                child: CircularProgressIndicator(strokeWidth: 2)),
            title: 'Diagnosing $node',
            content: const Padding(
              padding: EdgeInsets.symmetric(vertical: 4),
              child: Text('Probing each stage…'),
            ),
          );
        }
        if (snap.hasError) {
          final err = snap.error;
          final msg = err is ClientException ? err.message : '$err';
          return _shell(
            context,
            leading:
                Icon(Icons.error, color: Theme.of(context).colorScheme.error),
            title: 'Diagnosis: $node',
            content: Text(msg,
                style: TextStyle(color: Theme.of(context).colorScheme.error)),
          );
        }
        return _verdict(context, snap.data!);
      },
    );
  }

  /// Bare title/content/Close frame, shared by the running and error states.
  Widget _shell(
    BuildContext context, {
    required Widget leading,
    required String title,
    required Widget content,
  }) {
    return AlertDialog(
      title: Row(
        children: [
          leading,
          const SizedBox(width: 8),
          Expanded(child: Text(title)),
        ],
      ),
      content: content,
      actions: [
        FilledButton(
            onPressed: () => Navigator.pop(context), child: const Text('Close')),
      ],
    );
  }

  Widget _verdict(BuildContext context, DiagnosisInfo verdict) {
    return AlertDialog(
      title: Row(
        children: [
          Icon(verdict.ok ? Icons.check_circle : Icons.error,
              color: verdict.ok
                  ? context.appColors.statusRunning
                  : Theme.of(context).colorScheme.error),
          const SizedBox(width: 8),
          Expanded(child: Text('Diagnosis: ${verdict.node}')),
        ],
      ),
      content: Column(
        mainAxisSize: MainAxisSize.min,
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          if (!verdict.ok && verdict.failedStage != null)
            Padding(
              padding: const EdgeInsets.only(bottom: 8),
              child: Text('Failed at: ${verdict.failedStage}',
                  style:
                      TextStyle(color: Theme.of(context).colorScheme.error)),
            ),
          for (final stage in verdict.stages)
            ListTile(
              dense: true,
              contentPadding: EdgeInsets.zero,
              leading: Icon(stage.ok ? Icons.check : Icons.close,
                  color: stage.ok
                      ? context.appColors.statusRunning
                      : Theme.of(context).colorScheme.error,
                  size: 18),
              title: Text('${stage.stage} · ${stage.durationMs} ms'),
              subtitle: stage.error == null ? null : Text(stage.error!),
            ),
        ],
      ),
      actions: [
        FilledButton(
            onPressed: () => Navigator.pop(context), child: const Text('Close')),
      ],
    );
  }
}

/// Prompts for a new profile name; resolves to the trimmed name or null.
Future<String?> promptProfileName(BuildContext context) {
  final controller = TextEditingController();
  return showDialog<String>(
    context: context,
    builder: (context) => AlertDialog(
      title: const Text('New profile'),
      content: SizedBox(
        width: 360,
        child: TextField(
          controller: controller,
          autofocus: true,
          decoration: const InputDecoration(
            labelText: 'Name',
            helperText: 'Letters, digits, dot, dash, underscore (max 64)',
          ),
          onSubmitted: (v) => Navigator.pop(context, v.trim()),
        ),
      ),
      actions: [
        TextButton(
            onPressed: () => Navigator.pop(context), child: const Text('Cancel')),
        FilledButton(
            onPressed: () => Navigator.pop(context, controller.text.trim()),
            child: const Text('Create')),
      ],
    ),
  );
}

/// The membership mode of a user group: an explicit hand-picked set of
/// dialable nodes, or every dialable node of one subscription (all_of_sub).
enum GroupMode { nodes, subscription }

/// A group's current spec, for pre-filling [promptGroupEditor] on edit. Built
/// from the `get_group` reply (which carries the true membership mode and probe
/// — the resolved `list_nodes` row does not).
class GroupInitial {
  const GroupInitial({
    required this.name,
    required this.mode,
    this.members = const {},
    this.subId,
    this.intervalSec,
    this.probeUrl,
  });

  /// Decodes a `get_group` spec map into the editor's initial state. An
  /// `all_of_sub` present ⇒ subscription mode; otherwise the explicit-members
  /// mode. Probe fields are optional.
  factory GroupInitial.fromSpec(Map<String, Object?> spec) {
    final probe = spec['probe'];
    int? interval;
    String? url;
    if (probe is Map) {
      final iv = probe['interval_sec'];
      if (iv is num) interval = iv.toInt();
      final u = probe['url'];
      if (u is String && u.isNotEmpty) url = u;
    }
    final name = spec['name'] is String ? spec['name'] as String : '';
    final allOfSub = spec['all_of_sub'];
    if (allOfSub is String && allOfSub.isNotEmpty) {
      return GroupInitial(
          name: name,
          mode: GroupMode.subscription,
          subId: allOfSub,
          intervalSec: interval,
          probeUrl: url);
    }
    final members = <String>{
      if (spec['members'] is List)
        for (final m in spec['members'] as List)
          if (m is String) m,
    };
    return GroupInitial(
        name: name,
        mode: GroupMode.nodes,
        members: members,
        intervalSec: interval,
        probeUrl: url);
  }

  final String name;
  final GroupMode mode;
  final Set<String> members;
  final String? subId;
  final int? intervalSec;
  final String? probeUrl;
}

/// Prompts to build or edit a user group (an "Auto" node): a name, a membership
/// mode (a hand-picked set of nodes OR a whole subscription), and optional probe
/// tuning (re-rank interval / probe URL). Resolves to the `upsert_group` spec map
/// ({name, members|all_of_sub, probe?}) — the caller injects the id on edit — or
/// null on cancel.
///
/// [dialable] are the profile's dialable nodes (groups already excluded) and
/// [subs] its subscriptions; the caller guarantees at least one is non-empty
/// (and, on edit, that [initial]'s subscription is present in [subs]). A mode
/// whose source is empty is hidden, and Save stays disabled until the name is
/// set and the active mode has a valid selection. [initial] pre-fills the form
/// for an edit; null is a fresh create.
Future<Map<String, Object?>?> promptGroupEditor(
  BuildContext context, {
  required List<NodeInfo> dialable,
  required List<SubscriptionInfo> subs,
  GroupInitial? initial,
}) {
  final editing = initial != null;
  final nameController = TextEditingController(text: initial?.name ?? '');
  final intervalController = TextEditingController(
      text: initial?.intervalSec != null ? '${initial!.intervalSec}' : '');
  final urlController = TextEditingController(text: initial?.probeUrl ?? '');
  final dialableIds = {for (final n in dialable) n.id};
  // Stale stored members (a churned node) have no checkbox — drop them so an
  // edit never re-submits a member id the daemon would reject.
  final selected = <String>{
    if (initial != null) ...initial.members.where(dialableIds.contains),
  };
  final canNodes = dialable.isNotEmpty;
  final canSub = subs.isNotEmpty;
  var mode = initial?.mode ??
      (canNodes ? GroupMode.nodes : GroupMode.subscription);
  // A mode with no source (e.g. an all_of_sub edit whose sub is gone) falls back
  // to the one that has content.
  if (mode == GroupMode.nodes && !canNodes) mode = GroupMode.subscription;
  if (mode == GroupMode.subscription && !canSub) mode = GroupMode.nodes;
  var subId = initial?.subId ?? (subs.isNotEmpty ? subs.first.id : null);
  if (subId != null && !subs.any((s) => s.id == subId)) {
    subId = subs.isNotEmpty ? subs.first.id : null;
  }
  final subName = {for (final s in subs) s.id: s.name};

  return showDialog<Map<String, Object?>>(
    context: context,
    builder: (context) => StatefulBuilder(
      builder: (context, setState) {
        final name = nameController.text.trim();
        // The daemon stores interval_sec as a uint32; an empty field means
        // "use the sing-box default", anything else must be a positive uint32
        // — validate it client-side so an out-of-range value disables Create
        // rather than sailing past to an opaque daemon unmarshal error.
        final intervalText = intervalController.text.trim();
        final intervalNum = int.tryParse(intervalText);
        final intervalOk = intervalText.isEmpty ||
            (intervalNum != null && intervalNum > 0 && intervalNum <= 0xFFFFFFFF);
        final membershipOk =
            mode == GroupMode.nodes ? selected.isNotEmpty : subId != null;
        final valid = name.isNotEmpty && membershipOk && intervalOk;

        Map<String, Object?> spec() {
          final group = <String, Object?>{'name': name};
          if (mode == GroupMode.nodes) {
            group['members'] = selected.toList();
          } else {
            group['all_of_sub'] = subId;
          }
          final probe = <String, Object?>{};
          if (intervalNum != null) probe['interval_sec'] = intervalNum;
          final url = urlController.text.trim();
          if (url.isNotEmpty) probe['url'] = url;
          if (probe.isNotEmpty) group['probe'] = probe;
          return group;
        }

        return AlertDialog(
          title: Text(editing ? 'Edit auto group' : 'New auto group'),
          content: SizedBox(
            width: 520,
            child: SingleChildScrollView(
              child: Column(
                mainAxisSize: MainAxisSize.min,
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  TextField(
                    controller: nameController,
                    autofocus: true,
                    decoration: const InputDecoration(
                      labelText: 'Name',
                      hintText: '⚡ Auto',
                      helperText: 'The group auto-picks the fastest member '
                          'by latency',
                    ),
                    onChanged: (_) => setState(() {}),
                  ),
                  const SizedBox(height: 16),
                  if (canNodes && canSub) ...[
                    SegmentedButton<GroupMode>(
                      segments: const [
                        ButtonSegment(
                            value: GroupMode.nodes, label: Text('Pick nodes')),
                        ButtonSegment(
                            value: GroupMode.subscription,
                            label: Text('Whole subscription')),
                      ],
                      selected: {mode},
                      onSelectionChanged: (s) => setState(() => mode = s.first),
                    ),
                    const SizedBox(height: 12),
                  ],
                  if (mode == GroupMode.nodes)
                    _NodePicker(
                      dialable: dialable,
                      selected: selected,
                      subName: subName,
                      onChanged: () => setState(() {}),
                    )
                  else
                    DropdownButtonFormField<String>(
                      initialValue: subId,
                      decoration:
                          const InputDecoration(labelText: 'Subscription'),
                      items: [
                        for (final s in subs)
                          DropdownMenuItem(
                              value: s.id,
                              child: Text(s.name.isNotEmpty ? s.name : s.url)),
                      ],
                      onChanged: (v) => setState(() => subId = v),
                    ),
                  const SizedBox(height: 16),
                  TextField(
                    controller: intervalController,
                    keyboardType: TextInputType.number,
                    decoration: InputDecoration(
                      labelText: 'Re-rank interval (seconds)',
                      helperText: 'optional — default 180',
                      errorText: intervalOk ? null : 'must be 1…4294967295',
                    ),
                    onChanged: (_) => setState(() {}),
                  ),
                  const SizedBox(height: 12),
                  TextField(
                    controller: urlController,
                    decoration: const InputDecoration(
                      labelText: 'Probe URL',
                      helperText: 'optional — default: the built-in 204 check',
                    ),
                  ),
                ],
              ),
            ),
          ),
          actions: [
            TextButton(
                onPressed: () => Navigator.pop(context),
                child: const Text('Cancel')),
            FilledButton(
              onPressed: valid ? () => Navigator.pop(context, spec()) : null,
              child: Text(editing ? 'Save' : 'Create'),
            ),
          ],
        );
      },
    ),
  );
}

/// The bounded, scrollable multi-select of dialable nodes inside
/// [promptNewGroup] — each row a checkbox with the node name and, when the
/// node belongs to a subscription, that subscription's name as a subtitle.
class _NodePicker extends StatelessWidget {
  const _NodePicker({
    required this.dialable,
    required this.selected,
    required this.subName,
    required this.onChanged,
  });

  final List<NodeInfo> dialable;
  final Set<String> selected;
  final Map<String, String> subName;
  final VoidCallback onChanged;

  @override
  Widget build(BuildContext context) {
    final t = Theme.of(context);
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Padding(
          padding: const EdgeInsets.only(left: 4, bottom: 4),
          child: Text('${selected.length} of ${dialable.length} selected',
              style: t.textTheme.bodySmall),
        ),
        DecoratedBox(
          decoration: BoxDecoration(
            border: Border.all(color: t.dividerColor),
            borderRadius: BorderRadius.circular(8),
          ),
          child: SizedBox(
            height: 220,
            // No explicit Scrollbar: the desktop MaterialScrollBehavior already
            // wraps the ListView in a controller-backed one — adding a second,
            // controllerless Scrollbar stacks a non-interactive thumb over it.
            child: ListView(
              shrinkWrap: true,
              children: [
                for (final n in dialable)
                  CheckboxListTile(
                    dense: true,
                    controlAffinity: ListTileControlAffinity.leading,
                    value: selected.contains(n.id),
                    title: Text(n.name, overflow: TextOverflow.ellipsis),
                    subtitle: n.subId != null && subName[n.subId] != null
                        ? Text(subName[n.subId]!,
                            overflow: TextOverflow.ellipsis)
                        : null,
                    onChanged: (v) {
                      v == true ? selected.add(n.id) : selected.remove(n.id);
                      onChanged();
                    },
                  ),
              ],
            ),
          ),
        ),
      ],
    );
  }
}

/// The subscription parse formats the daemon accepts, in dropdown order.
const _subscriptionFormats = [
  'auto', 'links', 'xray', 'sing-box', 'clash', 'sip008',
];

/// The editable fields [promptEditSubscription] returns. (Auto-refresh
/// interval will join these once the daemon actually auto-refreshes.)
class SubscriptionEdit {
  SubscriptionEdit({
    required this.name,
    required this.url,
    required this.enabled,
    required this.allowInvalidCerts,
    this.format,
  });

  final String name;
  final String url;
  final bool enabled;
  final bool allowInvalidCerts;

  /// The newly chosen parse format, or null when unchanged (the
  /// `update_subscription` verb leaves an absent field alone).
  final String? format;
}

/// Prompts to edit a subscription's metadata, pre-filled from [sub]; resolves
/// to null on cancel.
Future<SubscriptionEdit?> promptEditSubscription(
    BuildContext context, SubscriptionInfo sub) {
  final nameController = TextEditingController(text: sub.name);
  final urlController = TextEditingController(text: sub.url);
  var enabled = sub.enabled;
  var allowInvalidCerts = sub.allowInvalidCerts;
  var format = sub.format;
  // A format this client does not know (a newer daemon) still preselects —
  // it joins the list rather than tripping the dropdown's value assert.
  final formats = _subscriptionFormats.contains(sub.format)
      ? _subscriptionFormats
      : [sub.format, ..._subscriptionFormats];
  return showDialog<SubscriptionEdit>(
    context: context,
    builder: (context) => StatefulBuilder(
      builder: (context, setState) => AlertDialog(
        title: const Text('Edit subscription'),
        content: SizedBox(
          width: 480,
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              TextField(
                controller: nameController,
                autofocus: true,
                decoration: const InputDecoration(labelText: 'Name'),
              ),
              const SizedBox(height: 12),
              TextField(
                controller: urlController,
                decoration: const InputDecoration(labelText: 'URL'),
              ),
              const SizedBox(height: 12),
              DropdownButtonFormField<String>(
                initialValue: format,
                decoration: const InputDecoration(labelText: 'Format'),
                items: [
                  for (final f in formats)
                    DropdownMenuItem(
                        value: f, child: Text(f == 'auto' ? 'auto (detect)' : f)),
                ],
                onChanged: (v) => setState(() => format = v ?? format),
              ),
              const SizedBox(height: 8),
              SwitchListTile(
                contentPadding: EdgeInsets.zero,
                title: const Text('Enabled'),
                subtitle:
                    const Text('Disabled subscriptions are skipped on refresh'),
                value: enabled,
                onChanged: (v) => setState(() => enabled = v),
              ),
              CheckboxListTile(
                contentPadding: EdgeInsets.zero,
                title: const Text('Allow invalid TLS certificates'),
                value: allowInvalidCerts,
                onChanged: (v) => setState(() => allowInvalidCerts = v ?? false),
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
              final name = nameController.text.trim();
              final url = urlController.text.trim();
              if (name.isEmpty || url.isEmpty) return;
              Navigator.pop(
                  context,
                  SubscriptionEdit(
                      name: name,
                      url: url,
                      enabled: enabled,
                      allowInvalidCerts: allowInvalidCerts,
                      // Only a CHANGED format goes on the wire; null keeps
                      // the daemon's stored pin untouched.
                      format: format == sub.format ? null : format));
            },
            child: const Text('Save'),
          ),
        ],
      ),
    ),
  );
}
