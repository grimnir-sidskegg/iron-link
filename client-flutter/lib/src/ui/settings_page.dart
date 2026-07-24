import 'package:flutter/material.dart';

import '../app/session.dart';
import '../app/theme_controller.dart';
import '../ipc/client.dart';
import '../wire/wire.dart';
import 'app.dart';
import 'theme/app_colors.dart';
import 'theme/app_theme.dart';
import 'theme/app_typography.dart';
import 'widgets/iron_widgets.dart';

/// The daemon-global settings editor. Loads the document from the daemon,
/// edits a working copy, sends the WHOLE document back (no patch
/// semantics); validation is the daemon's job — its error comes back
/// verbatim. Engine-relevant changes apply at the next activation (the
/// daemon flags that in the reply).
class SettingsPage extends StatefulWidget {
  const SettingsPage({
    super.key,
    required this.client,
    required this.session,
    required this.themeController,
  });

  final DaemonClient client;
  final DaemonSession session;
  final ThemeController themeController;

  @override
  State<SettingsPage> createState() => _SettingsPageState();
}

class _SettingsPageState extends State<SettingsPage> {
  Settings? _doc;
  String? _error;
  bool _saving = false;
  bool _dirty = false;
  bool _dnsClearing = false;

  // Free-text fields keep controllers; everything else edits _doc directly.
  final _socksPort = TextEditingController();
  final _mtu = TextEditingController();
  final _userAgent = TextEditingController();
  final _probeUrl = TextEditingController();
  final _probeBudget = TextEditingController();
  final _newCidr = TextEditingController();

  @override
  void initState() {
    super.initState();
    _load();
  }

  @override
  void dispose() {
    for (final c in [_socksPort, _mtu, _userAgent, _probeUrl, _probeBudget, _newCidr]) {
      c.dispose();
    }
    super.dispose();
  }

  Future<void> _load() async {
    try {
      final doc = await widget.client.getSettings();
      if (!mounted) return;
      setState(() {
        _doc = doc;
        _error = null;
        _dirty = false;
        _socksPort.text = '${doc.socksPort}';
        _mtu.text = '${doc.tun.mtu}';
        _userAgent.text = doc.subscriptionUserAgent;
        _probeUrl.text = doc.latencyProbe.url;
        _probeBudget.text = '${doc.latencyProbe.budgetSecs}';
      });
    } on ClientException catch (e) {
      if (!mounted) return;
      setState(() => _error = e.message);
    }
  }

  /// "Clear DNS cache". The pinned sing-box exposes no runtime flush, so the
  /// only real flush is rebuilding the box — i.e. re-activating the session.
  /// When connected that briefly interrupts traffic, so confirm first; when
  /// idle there is no live cache, so it is a no-op with a note.
  Future<void> _clearDnsCache() async {
    if (!widget.session.isRunning) {
      showSnack(context, 'No active session — DNS cache is already clear.');
      return;
    }
    final ok = await confirm(
        context,
        'Clear DNS cache',
        'sing-box can only flush DNS by rebuilding — this reconnects the '
            'current session and briefly interrupts traffic. Continue?');
    if (!ok || !mounted) return;
    setState(() => _dnsClearing = true);
    final tun = widget.session.active?.tun ?? true;
    final reactivated =
        await guardOk(context, () => widget.client.activate(tun: tun));
    if (reactivated) await widget.session.refreshStatus();
    if (!mounted) return;
    setState(() => _dnsClearing = false);
    if (reactivated) {
      showSnack(context, 'DNS cache flushed (session reconnected).');
    }
  }

  void _edit(void Function() apply) {
    setState(() {
      apply();
      _dirty = true;
    });
  }

  Future<void> _save() async {
    final doc = _doc;
    if (doc == null) return;
    // Pull the free-text fields into the document; non-numbers become
    // invalid values the daemon rejects with a readable message.
    doc.socksPort = int.tryParse(_socksPort.text.trim()) ?? -1;
    doc.tun.mtu = int.tryParse(_mtu.text.trim()) ?? -1;
    doc.subscriptionUserAgent = _userAgent.text.trim();
    doc.latencyProbe.url = _probeUrl.text.trim();
    doc.latencyProbe.budgetSecs = int.tryParse(_probeBudget.text.trim()) ?? -1;

    setState(() => _saving = true);
    try {
      final reply = await widget.client.setSettings(doc);
      if (!mounted) return;
      setState(() {
        _saving = false;
        _dirty = false;
      });
      showSnack(
          context,
          reply.needsReactivation
              ? 'Saved — re-activate the session to apply the change'
              : 'Settings saved');
    } on ClientException catch (e) {
      if (!mounted) return;
      setState(() => _saving = false);
      showSnack(context, e.message, error: true);
    }
  }

  @override
  Widget build(BuildContext context) {
    final t = context.iron;
    final doc = _doc;
    return Scaffold(
      backgroundColor: t.panel,
      floatingActionButton: doc == null
          ? null
          : FloatingActionButton.extended(
              onPressed: _saving ? null : _save,
              icon: _saving
                  ? const SizedBox(
                      width: 16,
                      height: 16,
                      child: CircularProgressIndicator(strokeWidth: 2))
                  : const Icon(Icons.save),
              label: Text(_dirty ? 'Save changes' : 'Save'),
            ),
      body: ListView(
        padding: const EdgeInsets.fromLTRB(24, 16, 24, 88),
        children: [
          // ---- Screen-title row (hosted below the shell's title bar) ----
          Padding(
            padding: const EdgeInsets.only(bottom: 4),
            child: Row(
              children: [
                Text('Settings', style: context.screenTitle),
                const Spacer(),
                IronIconButton(
                  icon: Icons.refresh,
                  onPressed: _load,
                  tooltip: 'Reload from the daemon',
                ),
              ],
            ),
          ),
          if (_error != null)
            Padding(
              padding: const EdgeInsets.only(top: 8),
              child: Text(_error!, style: TextStyle(color: t.danger)),
            ),
          const SizedBox(height: 16),
          // Appearance is client-side, so it stays reachable even when the
          // daemon (and its settings document) is down.
          _appearanceSection(),
          if (doc == null)
            const Padding(
              padding: EdgeInsets.only(top: 48),
              child: Center(child: CircularProgressIndicator()),
            )
          else ...[
            const SizedBox(height: 20),
            _generalSection(doc),
            const SizedBox(height: 20),
            _connectionSection(doc),
            const SizedBox(height: 20),
            _dnsSection(doc),
            const SizedBox(height: 20),
            _lanBypassSection(doc),
            const SizedBox(height: 20),
            _subscriptionsSection(),
            const SizedBox(height: 20),
            _diagnosticsSection(doc),
            const SizedBox(height: 24),
            _footer(),
          ],
        ],
      ),
    );
  }

  // ---------------------------------------------------------------------------
  // Daemon-backed sections.
  // ---------------------------------------------------------------------------

  Widget _generalSection(Settings doc) {
    return IronSectionGroup(
      title: 'General',
      rows: [
        IronSettingRow(
          title: 'Restore session on daemon start',
          trailing: IronSwitch(
            value: doc.restoreOnStart,
            onChanged: (v) => _edit(() => doc.restoreOnStart = v),
          ),
        ),
      ],
    );
  }

  Widget _connectionSection(Settings doc) {
    return IronSectionGroup(
      title: 'Connection',
      rows: [
        IronSettingRow(
          title: 'IP version',
          subtitle: doc.ipVersion == 'both'
              ? 'Which address families the tunnel carries. Both is recommended.'
              : doc.ipVersion == 'v4'
                  ? 'IPv4 only — IPv6 is disabled end-to-end, including DNS. '
                      "IPv6-only sites won't load."
                  : 'IPv6 only — IPv4 is disabled end-to-end, including DNS. '
                      "IPv4-only sites won't load.",
          trailing: IronSegmented<String>(
            segments: const [
              ('v4', 'IPv4'),
              ('v6', 'IPv6'),
              ('both', 'Both'),
            ],
            selected: doc.ipVersion,
            onChanged: (v) => _edit(() => doc.ipVersion = v),
          ),
        ),
        _numFieldRow('SOCKS port', 'proxy-only mode', _socksPort),
        _numFieldRow('TUN MTU', null, _mtu),
        IronSettingRow(
          title: 'TUN stack',
          trailing: DropdownButton<String>(
            value: doc.tun.stack,
            items: const [
              DropdownMenuItem(value: 'system', child: Text('system')),
              DropdownMenuItem(value: 'gvisor', child: Text('gvisor')),
              DropdownMenuItem(value: 'mixed', child: Text('mixed')),
            ],
            onChanged: (v) => _edit(() => doc.tun.stack = v ?? 'mixed'),
          ),
        ),
        IronSettingRow(
          title: 'Strict route',
          subtitle:
              'Tighter leak protection; disable only for compatibility',
          trailing: IronSwitch(
            value: doc.tun.strictRoute,
            onChanged: (v) => _edit(() => doc.tun.strictRoute = v),
          ),
        ),
      ],
    );
  }

  Widget _dnsSection(Settings doc) {
    // A single-family IP version PINS the DNS strategy: a v4/v6-only tunnel
    // can't carry the other family, so the daemon forces ipv4_only / ipv6_only
    // regardless of this control. Reflect that here — show the pinned value,
    // disable the control, and explain — WITHOUT overwriting the stored
    // preference, so switching IP version back to Both restores the choice.
    final pinned = doc.ipVersion != 'both';
    final pinnedFamily = doc.ipVersion == 'v4' ? 'IPv4' : 'IPv6';
    final effectiveStrategy = doc.ipVersion == 'v4'
        ? 'ipv4_only'
        : doc.ipVersion == 'v6'
            ? 'ipv6_only'
            : doc.dns.strategy;
    return IronSectionGroup(
      title: 'DNS',
      rows: [
        IronSettingRow(
          title: 'DNS resolution',
          subtitle: pinned
              ? 'Locked to $pinnedFamily only by IP version = $pinnedFamily — a '
                  "single-family tunnel can't carry the other family. Set IP "
                  'version to Both to choose.'
              : 'Which address family DNS resolves to. Pin IPv4/IPv6 to '
                  'stabilize a flapping dual-stack network.',
          trailing: DropdownButton<String>(
            value: effectiveStrategy,
            items: const [
              DropdownMenuItem(
                  value: 'prefer_ipv4',
                  child: Text('Prefer IPv4',
                      style: TextStyle(fontWeight: FontWeight.bold))),
              DropdownMenuItem(
                  value: 'prefer_ipv6', child: Text('Prefer IPv6')),
              DropdownMenuItem(value: 'ipv4_only', child: Text('IPv4 only')),
              DropdownMenuItem(value: 'ipv6_only', child: Text('IPv6 only')),
            ],
            onChanged: pinned
                ? null
                : (v) => _edit(() => doc.dns.strategy = v ?? 'prefer_ipv4'),
          ),
        ),
        for (var i = 0; i < doc.dns.servers.length; i++) _dnsRow(doc, i),
        // "Add server" lives on its own row inside the group.
        Padding(
          padding: const EdgeInsets.fromLTRB(12, 8, 16, 8),
          child: Align(
            alignment: Alignment.centerLeft,
            child: IronButton(
              label: 'Add server',
              icon: Icons.add,
              onPressed: () => _edit(() =>
                  doc.dns.servers.add(DnsServer(type: 'udp', address: ''))),
            ),
          ),
        ),
        IronSettingRow(
          title: 'Resolve through the tunnel',
          subtitle: 'Queries detour through the proxy; node-domain bootstrap '
              'always stays direct',
          trailing: IronSwitch(
            value: doc.dns.viaTunnel,
            onChanged: (v) => _edit(() => doc.dns.viaTunnel = v),
          ),
        ),
        IronSettingRow(
          title: 'Clear DNS cache',
          subtitle: 'sing-box has no live flush — when connected, clearing '
              'reconnects the session to rebuild its resolver',
          trailing: IronButton(
            label: 'Clear',
            icon: Icons.cleaning_services_outlined,
            onPressed: _dnsClearing ? null : _clearDnsCache,
          ),
        ),
      ],
    );
  }

  Widget _lanBypassSection(Settings doc) {
    return IronSectionGroup(
      title: 'LAN bypass',
      rows: [
        IronSettingRow(
          title: 'Keep local networks out of the tunnel',
          trailing: IronSwitch(
            value: doc.lanBypass.enabled,
            onChanged: (v) => _edit(() => doc.lanBypass.enabled = v),
          ),
        ),
        if (doc.lanBypass.enabled) _cidrEditor(doc),
      ],
    );
  }

  /// The CIDR chip list + add field, folded into a single group row.
  Widget _cidrEditor(Settings doc) {
    final t = context.iron;
    return Padding(
      padding: const EdgeInsets.fromLTRB(16, 12, 16, 12),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          if (doc.lanBypass.cidrs.isNotEmpty)
            Padding(
              padding: const EdgeInsets.only(bottom: 10),
              child: Wrap(
                spacing: 6,
                runSpacing: 6,
                children: [
                  for (final cidr in doc.lanBypass.cidrs)
                    InputChip(
                      label: Text(cidr),
                      onDeleted: () =>
                          _edit(() => doc.lanBypass.cidrs.remove(cidr)),
                    ),
                ],
              ),
            ),
          Row(
            children: [
              Expanded(
                child: TextField(
                  controller: _newCidr,
                  style: TextStyle(color: t.text),
                  decoration: const InputDecoration(
                    isDense: true,
                    labelText: 'Add CIDR',
                    hintText: '100.64.0.0/10',
                  ),
                  onSubmitted: (_) => _addCidr(doc),
                ),
              ),
              const SizedBox(width: 8),
              IronIconButton(
                icon: Icons.add,
                onPressed: () => _addCidr(doc),
                tooltip: 'Add CIDR',
              ),
            ],
          ),
        ],
      ),
    );
  }

  Widget _subscriptionsSection() {
    return IronSectionGroup(
      title: 'Subscriptions',
      rows: [
        _textFieldRow('Fetch User-Agent', _userAgent),
      ],
    );
  }

  Widget _diagnosticsSection(Settings doc) {
    return IronSectionGroup(
      title: 'Diagnostics',
      rows: [
        IronSettingRow(
          title: 'Log level',
          trailing: DropdownButton<String>(
            value: doc.logLevel,
            items: const [
              DropdownMenuItem(value: 'error', child: Text('error')),
              DropdownMenuItem(value: 'warn', child: Text('warn')),
              DropdownMenuItem(value: 'info', child: Text('info')),
              DropdownMenuItem(value: 'debug', child: Text('debug')),
            ],
            onChanged: (v) => _edit(() => doc.logLevel = v ?? 'info'),
          ),
        ),
        _textFieldRow('Latency probe URL', _probeUrl),
        _numFieldRow('Probe budget', 'seconds', _probeBudget),
      ],
    );
  }

  /// A static footer line — the app name plus the active profile when one is
  /// connected (the daemon doc carries no version string).
  Widget _footer() {
    final t = context.iron;
    final profile = widget.session.active?.profile;
    final label = profile == null ? 'iron-link' : 'iron-link · $profile';
    return Center(
      child: Text(
        label,
        style: context.mono(11.5, color: t.faint),
      ),
    );
  }

  void _addCidr(Settings doc) {
    final cidr = _newCidr.text.trim();
    if (cidr.isEmpty) return;
    _edit(() {
      if (!doc.lanBypass.cidrs.contains(cidr)) doc.lanBypass.cidrs.add(cidr);
      _newCidr.clear();
    });
  }

  /// One DNS-server row: a type dropdown, an editable address field, and a
  /// remove button (disabled when it is the daemon-required last server).
  Widget _dnsRow(Settings doc, int i) {
    final t = context.iron;
    final server = doc.dns.servers[i];
    return Padding(
      padding: const EdgeInsets.fromLTRB(16, 10, 8, 10),
      child: Row(
        children: [
          DropdownButton<String>(
            value: server.type,
            items: const [
              DropdownMenuItem(value: 'udp', child: Text('UDP')),
              DropdownMenuItem(value: 'tls', child: Text('DoT')),
              DropdownMenuItem(value: 'https', child: Text('DoH')),
            ],
            onChanged: (v) => _edit(() => server.type = v ?? 'udp'),
          ),
          const SizedBox(width: 12),
          Expanded(
            child: TextFormField(
              initialValue: server.address,
              style: TextStyle(color: t.text),
              decoration: InputDecoration(
                isDense: true,
                labelText: i == 0 ? 'Server IP (primary)' : 'Server IP',
              ),
              onChanged: (v) {
                server.address = v.trim();
                if (!_dirty) setState(() => _dirty = true);
              },
            ),
          ),
          IconButton(
            tooltip: 'Remove',
            onPressed: doc.dns.servers.length == 1
                ? null // the daemon requires at least one server
                : () => _edit(() => doc.dns.servers.removeAt(i)),
            icon: const Icon(Icons.delete_outline),
          ),
        ],
      ),
    );
  }

  // ---------------------------------------------------------------------------
  // Appearance (client-side, daemon-independent).
  // ---------------------------------------------------------------------------

  /// The Appearance group: the theme picker (each item previews its palette) and
  /// the reduce-motion toggle. Both apply (and persist) immediately, independent
  /// of the daemon Save flow, so they work even when the daemon is unreachable.
  Widget _appearanceSection() {
    final controller = widget.themeController;
    // The swatch previews the variant the current mode resolves to, so the
    // dropdown shows what you'll actually get.
    final brightness = _effectiveBrightness();
    return IronSectionGroup(
      title: 'Appearance',
      rows: [
        IronSettingRow(
          title: 'Color scheme',
          trailing: DropdownButton<String>(
            value: controller.familyId,
            onChanged: (id) {
              if (id != null) controller.selectFamily(id);
            },
            items: [
              for (final family in themeFamilies)
                DropdownMenuItem(
                  value: family.id,
                  child: Row(
                    mainAxisSize: MainAxisSize.min,
                    children: [
                      _swatches(family.iron(brightness)),
                      const SizedBox(width: 12),
                      Text(family.label),
                    ],
                  ),
                ),
            ],
          ),
        ),
        IronSettingRow(
          title: 'Mode',
          subtitle: 'Light, dark, or follow the system',
          trailing: SegmentedButton<ThemeMode>(
            showSelectedIcon: false,
            segments: const [
              ButtonSegment(
                value: ThemeMode.light,
                icon: Icon(Icons.light_mode_outlined, size: 18),
                tooltip: 'Light',
              ),
              ButtonSegment(
                value: ThemeMode.system,
                icon: Icon(Icons.brightness_auto_outlined, size: 18),
                tooltip: 'Follow system',
              ),
              ButtonSegment(
                value: ThemeMode.dark,
                icon: Icon(Icons.dark_mode_outlined, size: 18),
                tooltip: 'Dark',
              ),
            ],
            selected: {controller.mode},
            onSelectionChanged: (s) => controller.setMode(s.first),
          ),
        ),
        IronSettingRow(
          title: 'Reduce motion',
          subtitle:
              'Disable the looping pipeline & power-orb animations',
          trailing: IronSwitch(
            value: controller.reduceMotion,
            onChanged: controller.setReduceMotion,
          ),
        ),
      ],
    );
  }

  /// The brightness the current mode resolves to right now — used to preview
  /// the right variant in the scheme swatches.
  Brightness _effectiveBrightness() => switch (widget.themeController.mode) {
        ThemeMode.light => Brightness.light,
        ThemeMode.dark => Brightness.dark,
        ThemeMode.system => MediaQuery.platformBrightnessOf(context),
      };

  /// A compact swatch chip previewing a palette: its surface behind the accent,
  /// the primary text and the danger/blocked dot, so the colours are visible
  /// before the user commits. Reads the variant's own [IronTheme] tokens.
  Widget _swatches(IronTheme iron) {
    Widget dot(Color c) => Container(
          width: 12,
          height: 12,
          decoration: BoxDecoration(color: c, shape: BoxShape.circle),
        );
    return Container(
      padding: const EdgeInsets.all(6),
      decoration: BoxDecoration(
        color: iron.panel,
        borderRadius: BorderRadius.circular(8),
        border: Border.all(color: iron.border),
      ),
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          dot(iron.accent),
          const SizedBox(width: 4),
          dot(iron.text),
          const SizedBox(width: 4),
          dot(iron.danger),
        ],
      ),
    );
  }

  // ---------------------------------------------------------------------------
  // Free-text field rows.
  // ---------------------------------------------------------------------------

  /// A free-text setting row: a label (+ optional subtitle) and a themed text
  /// field as the trailing control. Marks the document dirty on first edit.
  Widget _textFieldRow(String title, TextEditingController controller,
      {String? subtitle}) {
    final t = context.iron;
    return IronSettingRow(
      title: title,
      subtitle: subtitle,
      trailing: SizedBox(
        width: 200,
        child: TextField(
          controller: controller,
          textAlign: TextAlign.end,
          style: TextStyle(color: t.text),
          decoration: const InputDecoration(isDense: true),
          onChanged: (_) {
            if (!_dirty) setState(() => _dirty = true);
          },
        ),
      ),
    );
  }

  /// A numeric setting row — like [_textFieldRow] but with the number keyboard.
  Widget _numFieldRow(
      String title, String? subtitle, TextEditingController controller) {
    final t = context.iron;
    return IronSettingRow(
      title: title,
      subtitle: subtitle,
      trailing: SizedBox(
        width: 120,
        child: TextField(
          controller: controller,
          keyboardType: TextInputType.number,
          textAlign: TextAlign.end,
          style: context.mono(14, color: t.text),
          decoration: const InputDecoration(isDense: true),
          onChanged: (_) {
            if (!_dirty) setState(() => _dirty = true);
          },
        ),
      ),
    );
  }
}
