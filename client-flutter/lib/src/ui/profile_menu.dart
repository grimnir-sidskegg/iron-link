import 'package:flutter/material.dart';

import '../app/session.dart';
import '../ipc/client.dart';
import '../wire/wire.dart';
import 'app.dart';
import 'dialogs.dart';
import 'theme/app_colors.dart';

/// The profile pill pinned to the far right of the nav bar: a monogram chip
/// + the active profile name + "default profile", and a tap menu with the
/// profile list plus "New profile…" / "Manage profiles…".
///
/// Deliberately NOT a session listener — the nav's perf budget (the _ShellState
/// comment in app.dart: no rebuild per traffic tick) applies here too. The menu
/// fetches a fresh profile list on every open, so the menu itself is always
/// current; the label can lag a CLI-side change until the next open —
/// acceptable.
class ProfilePill extends StatefulWidget {
  const ProfilePill({super.key, required this.client, required this.session});

  final DaemonClient client;
  final DaemonSession session;

  @override
  State<ProfilePill> createState() => _ProfilePillState();
}

class _ProfilePillState extends State<ProfilePill> {
  String? _active;

  @override
  void initState() {
    super.initState();
    _fetchLabel();
  }

  /// One quiet fetch for the label; on error the placeholder stands (the
  /// rail must render with the daemon down — the menu path reports).
  Future<void> _fetchLabel() async {
    try {
      final profiles = await widget.client.listProfiles();
      if (mounted) setState(() => _active = profiles.activeProfile);
    } on ClientException {
      // Quiet by design.
    }
  }

  Future<void> _openMenu() async {
    final profiles = await guard(context, widget.client.listProfiles);
    if (profiles == null || !mounted) return;
    final box = context.findRenderObject()! as RenderBox;
    final overlay =
        Overlay.of(context).context.findRenderObject()! as RenderBox;
    final position = RelativeRect.fromRect(
      Rect.fromPoints(
        box.localToGlobal(Offset.zero, ancestor: overlay),
        box.localToGlobal(box.size.bottomRight(Offset.zero), ancestor: overlay),
      ),
      Offset.zero & overlay.size,
    );
    // showMenu, not PopupMenuButton: its itemBuilder is synchronous, and
    // the menu must be built from a fetch done on open.
    final choice = await showMenu<(String, String)>(
      context: context,
      position: position,
      items: [
        for (final name in profiles.profiles)
          CheckedPopupMenuItem(
            value: ('select', name),
            checked: name == profiles.activeProfile,
            child: Text(name, overflow: TextOverflow.ellipsis),
          ),
        const PopupMenuDivider(),
        const PopupMenuItem(value: ('new', ''), child: Text('New profile…')),
        const PopupMenuItem(
            value: ('manage', ''), child: Text('Manage profiles…')),
      ],
    );
    if (choice == null || !mounted) return;
    switch (choice) {
      case ('select', final name):
        if (name != profiles.activeProfile) await _select(name);
      case ('new', _):
        await _create();
      case ('manage', _):
        await _manage();
    }
  }

  Future<void> _select(String name) async {
    if (!await guardOk(context, () => widget.client.setActiveProfile(name))) {
      return;
    }
    if (!mounted) return;
    setState(() => _active = name);
    // No daemon event covers this — hint the node list to reload.
    widget.session.noteStoreChanged();
    if (widget.session.isRunning) {
      // The running session keeps serving the old profile; say so.
      showSnack(context, 'Profile set — restart the session to apply');
    }
  }

  Future<void> _create() async {
    final name = await promptProfileName(context);
    if (name == null || name.isEmpty || !mounted) return;
    await guardOk(context, () => widget.client.createProfile(name));
  }

  Future<void> _manage() async {
    var deleted = false;
    await showDialog<void>(
      context: context,
      builder: (context) => _ManageProfilesDialog(
        client: widget.client,
        onDeleted: () => deleted = true,
      ),
    );
    if (!deleted || !mounted) return;
    await _fetchLabel();
    widget.session.noteStoreChanged();
  }

  @override
  Widget build(BuildContext context) {
    final t = context.iron;
    final label = _active ?? 'profile';
    final mono = label.isEmpty ? '?' : label.characters.first.toUpperCase();
    return Tooltip(
      message: label,
      child: Material(
        color: t.raised,
        borderRadius: BorderRadius.circular(12),
        child: InkWell(
          onTap: _openMenu,
          borderRadius: BorderRadius.circular(12),
          child: Container(
            decoration: BoxDecoration(
              borderRadius: BorderRadius.circular(12),
              border: Border.all(color: t.border),
            ),
            padding: const EdgeInsets.fromLTRB(6, 6, 12, 6),
            child: Row(
              mainAxisSize: MainAxisSize.min,
              children: [
                Container(
                  width: 30,
                  height: 30,
                  alignment: Alignment.center,
                  decoration: BoxDecoration(
                    color: t.accentSoft,
                    borderRadius: BorderRadius.circular(9),
                  ),
                  child: Text(mono,
                      style: TextStyle(
                          color: t.accentStrong,
                          fontWeight: FontWeight.w700,
                          fontSize: 13)),
                ),
                const SizedBox(width: 9),
                Column(
                  mainAxisSize: MainAxisSize.min,
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    ConstrainedBox(
                      constraints: const BoxConstraints(maxWidth: 96),
                      child: Text(
                        label,
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: TextStyle(
                            color: t.text,
                            fontSize: 12.5,
                            fontWeight: FontWeight.w600),
                      ),
                    ),
                    Text('default profile',
                        style: TextStyle(color: t.faint, fontSize: 10.5)),
                  ],
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }
}

/// The CRUD dialog behind "Manage profiles…": the list with delete. The
/// active profile is not deletable (the daemon would refuse anyway);
/// [onDeleted] fires per successful delete so the opener knows to refetch
/// its label and hint the store change.
class _ManageProfilesDialog extends StatefulWidget {
  const _ManageProfilesDialog(
      {required this.client, required this.onDeleted});

  final DaemonClient client;
  final VoidCallback onDeleted;

  @override
  State<_ManageProfilesDialog> createState() => _ManageProfilesDialogState();
}

class _ManageProfilesDialogState extends State<_ManageProfilesDialog> {
  ProfilesResponse? _profiles;
  String? _error;

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    try {
      final profiles = await widget.client.listProfiles();
      if (!mounted) return;
      setState(() {
        _profiles = profiles;
        _error = null;
      });
    } on ClientException catch (e) {
      if (!mounted) return;
      setState(() => _error = e.message);
    }
  }

  Future<void> _delete(String name) async {
    if (!await confirm(context, 'Delete profile',
        'Delete "$name" with all its nodes and routing?')) {
      return;
    }
    if (!mounted) return;
    if (await guardOk(context, () => widget.client.deleteProfile(name))) {
      widget.onDeleted();
      _load();
    }
  }

  @override
  Widget build(BuildContext context) {
    final profiles = _profiles;
    return AlertDialog(
      title: const Text('Manage profiles'),
      content: SizedBox(
        width: 360,
        child: profiles == null
            ? SizedBox(
                height: 64,
                child: Center(
                    child: _error == null
                        ? const CircularProgressIndicator()
                        : Text(_error!,
                            style: TextStyle(
                                color: Theme.of(context).colorScheme.error))),
              )
            : SingleChildScrollView(
                child: Column(
                  mainAxisSize: MainAxisSize.min,
                  children: [
                    if (_error != null)
                      Text(_error!,
                          style: TextStyle(
                              color: Theme.of(context).colorScheme.error)),
                    for (final name in profiles.profiles)
                      ListTile(
                        contentPadding: EdgeInsets.zero,
                        leading: Icon(
                          name == profiles.activeProfile
                              ? Icons.folder
                              : Icons.folder_outlined,
                          color: name == profiles.activeProfile
                              ? Theme.of(context).colorScheme.primary
                              : null,
                        ),
                        title: Text(name, overflow: TextOverflow.ellipsis),
                        subtitle: name == profiles.activeProfile
                            ? const Text('active')
                            : null,
                        trailing: name == profiles.activeProfile
                            ? null
                            : IconButton(
                                tooltip: 'Delete',
                                onPressed: () => _delete(name),
                                icon: const Icon(Icons.delete_outline),
                              ),
                      ),
                  ],
                ),
              ),
      ),
      actions: [
        FilledButton(
            onPressed: () => Navigator.pop(context),
            child: const Text('Close')),
      ],
    );
  }
}
