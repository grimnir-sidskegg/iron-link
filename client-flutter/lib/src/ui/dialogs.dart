/// Shared prompt/report dialogs — moved unchanged (bar visibility) from
/// the former Nodes / Subscriptions / Profiles pages when they merged into
/// HomePage; the profile prompt is also used by the rail's profile menu.
library;

import 'package:flutter/material.dart';

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

/// The per-stage `diagnose` verdict report.
class DiagnosisDialog extends StatelessWidget {
  const DiagnosisDialog({super.key, required this.verdict});

  final DiagnosisInfo verdict;

  @override
  Widget build(BuildContext context) {
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

/// The editable fields [promptEditSubscription] returns. (Auto-refresh
/// interval will join these once the daemon actually auto-refreshes.)
class SubscriptionEdit {
  SubscriptionEdit({
    required this.name,
    required this.url,
    required this.enabled,
    required this.allowInvalidCerts,
  });

  final String name;
  final String url;
  final bool enabled;
  final bool allowInvalidCerts;
}

/// Prompts to edit a subscription's metadata, pre-filled from [sub]; resolves
/// to null on cancel.
Future<SubscriptionEdit?> promptEditSubscription(
    BuildContext context, SubscriptionInfo sub) {
  final nameController = TextEditingController(text: sub.name);
  final urlController = TextEditingController(text: sub.url);
  var enabled = sub.enabled;
  var allowInvalidCerts = sub.allowInvalidCerts;
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
                      allowInvalidCerts: allowInvalidCerts));
            },
            child: const Text('Save'),
          ),
        ],
      ),
    ),
  );
}
