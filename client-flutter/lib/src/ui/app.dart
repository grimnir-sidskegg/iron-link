import 'dart:io';

import 'package:flutter/material.dart';

import '../app/session.dart';
import '../app/theme_controller.dart';
import '../ipc/client.dart';
import 'doctor_page.dart';
import 'home_page.dart';
import 'iron_scope.dart';
import 'logs_page.dart';
import 'profile_menu.dart';
import 'routing_page.dart';
import 'settings_page.dart';
import 'theme/app_colors.dart';
import 'traffic_page.dart';
import 'widgets/motion_gate.dart';

/// Root widget: theme + [IronScope] (so deep widgets read the controller) + the
/// redesigned single-window shell. The [MaterialApp] is rebuilt when
/// [themeController] notifies (an Appearance change), restyling the whole tree
/// while the element tree — and so all page state — is preserved.
class IronLinkApp extends StatelessWidget {
  const IronLinkApp({
    super.key,
    required this.client,
    required this.session,
    required this.themeController,
  });

  final DaemonClient client;
  final DaemonSession session;
  final ThemeController themeController;

  @override
  Widget build(BuildContext context) {
    return ListenableBuilder(
      listenable: themeController,
      builder: (context, _) => IronScope(
        controller: themeController,
        child: MaterialApp(
          title: 'iron-link',
          debugShowCheckedModeBanner: false,
          theme: themeController.lightTheme,
          darkTheme: themeController.darkTheme,
          themeMode: themeController.themeMode,
          // MotionGate freezes the looping ambient animations (motion.dart)
          // while the window is unfocused/occluded or the lifecycle has left
          // `resumed`, so they stop contending for the GPU with whatever now
          // has focus (e.g. a foreground game).
          home: MotionGate(child: _Shell(client: client, session: session)),
        ),
      ),
    );
  }
}

/// The six screens, in nav order.
enum _Tab { home, routing, logs, traffic, doctor, settings }

class _Shell extends StatefulWidget {
  const _Shell({required this.client, required this.session});

  final DaemonClient client;
  final DaemonSession session;

  @override
  State<_Shell> createState() => _ShellState();
}

class _ShellState extends State<_Shell> {
  int _page = _initialPage();

  // The nav only depends on (connected, running); rebuilding it on every
  // session notify would mean a rebuild per traffic tick — listen manually and
  // setState only when those two bits actually change.
  late bool _connected = widget.session.connected;
  late bool _running = widget.session.isRunning;

  /// Measurement/test hook: IRON_LINK_START_PAGE pins the initial page. Legacy
  /// page names merged into Home still resolve to 0.
  static int _initialPage() {
    const pages = ['home', 'routing', 'logs', 'traffic', 'doctor', 'settings'];
    final index =
        pages.indexOf(Platform.environment['IRON_LINK_START_PAGE'] ?? '');
    return index < 0 ? 0 : index;
  }

  @override
  void initState() {
    super.initState();
    widget.session.addListener(_onSession);
  }

  @override
  void dispose() {
    widget.session.removeListener(_onSession);
    super.dispose();
  }

  void _onSession() {
    final connected = widget.session.connected;
    final running = widget.session.isRunning;
    if (connected != _connected || running != _running) {
      setState(() {
        _connected = connected;
        _running = running;
      });
    }
  }

  @override
  Widget build(BuildContext context) {
    final t = context.iron;
    final body = switch (_Tab.values[_page]) {
      _Tab.home => HomePage(client: widget.client, session: widget.session),
      _Tab.routing =>
        RoutingPage(client: widget.client, session: widget.session),
      _Tab.logs => LogsPage(session: widget.session),
      _Tab.traffic =>
        TrafficPage(client: widget.client, session: widget.session),
      _Tab.doctor => DoctorPage(client: widget.client),
      _Tab.settings => SettingsPage(
          client: widget.client,
          session: widget.session,
          themeController: IronScope.of(context)),
    };
    return Scaffold(
      backgroundColor: t.panel,
      body: Column(
        // Stretch so the title bar and nav span the full width — without this
        // the Column centres them at their content width (the title bar would
        // shrink to the wordmark and the close dot would land on the text).
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          _NavBar(
            current: _page,
            connected: _connected,
            running: _running,
            onSelect: (i) => setState(() => _page = i),
            profile:
                ProfilePill(client: widget.client, session: widget.session),
          ),
          Expanded(child: body),
        ],
      ),
    );
  }
}

/// The top navigation bar (h70), seated directly under the title bar: six
/// destinations + the profile pill. The **Home** item turns `accentStrong`
/// whenever connected — the live connection indicator — independent of which
/// tab is active.
class _NavBar extends StatelessWidget {
  const _NavBar({
    required this.current,
    required this.connected,
    required this.running,
    required this.onSelect,
    required this.profile,
  });

  final int current;
  final bool connected;
  final bool running;
  final ValueChanged<int> onSelect;
  final Widget profile;

  static const _items = [
    (Icons.power_settings_new, 'Home'),
    (Icons.alt_route, 'Routing'),
    (Icons.article_outlined, 'Logs'),
    (Icons.insights, 'Traffic'),
    (Icons.health_and_safety_outlined, 'Doctor'),
    (Icons.settings, 'Settings'),
  ];

  @override
  Widget build(BuildContext context) {
    final t = context.iron;
    return Container(
      height: 70,
      decoration: BoxDecoration(
        color: t.titlebar,
        border: Border(bottom: BorderSide(color: t.border)),
      ),
      padding: const EdgeInsets.symmetric(horizontal: 14),
      child: Row(
        children: [
          for (var i = 0; i < _items.length; i++)
            _NavItem(
              icon: _items[i].$1,
              label: _items[i].$2,
              active: current == i,
              // Home is the live indicator: accent-green whenever connected.
              homeConnected: i == 0 && connected,
              onTap: () => onSelect(i),
            ),
          const Spacer(),
          profile,
        ],
      ),
    );
  }
}

class _NavItem extends StatelessWidget {
  const _NavItem({
    required this.icon,
    required this.label,
    required this.active,
    required this.homeConnected,
    required this.onTap,
  });

  final IconData icon;
  final String label;
  final bool active;
  final bool homeConnected;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    final t = context.iron;
    // Home-when-connected wins the colour even if another tab is active.
    final color = homeConnected
        ? t.accentStrong
        : active
            ? t.text
            : t.dim;
    return Padding(
      padding: const EdgeInsets.symmetric(horizontal: 2),
      child: Material(
        type: MaterialType.transparency,
        child: InkWell(
          onTap: onTap,
          borderRadius: BorderRadius.circular(12),
          child: AnimatedContainer(
            duration: const Duration(milliseconds: 200),
            width: 62,
            height: 54,
            decoration: BoxDecoration(
              color: active ? t.raised : Colors.transparent,
              borderRadius: BorderRadius.circular(12),
            ),
            child: Column(
              mainAxisAlignment: MainAxisAlignment.center,
              children: [
                Icon(icon, size: 20, color: color),
                const SizedBox(height: 4),
                Text(
                  label,
                  style: TextStyle(
                    fontSize: 10.5,
                    fontWeight: FontWeight.w600,
                    color: color,
                  ),
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }
}

// ---------------------------------------------------------------------------
// Shared helpers used by the pages (unchanged contract).
// ---------------------------------------------------------------------------

void showSnack(BuildContext context, String message, {bool error = false}) {
  final messenger = ScaffoldMessenger.maybeOf(context);
  messenger?.showSnackBar(SnackBar(
    content: Text(message),
    backgroundColor: error ? Theme.of(context).colorScheme.error : null,
  ));
}

/// Runs a daemon call, reporting failure as a snackbar. Returns null on
/// failure.
Future<T?> guard<T>(BuildContext context, Future<T> Function() action) async {
  try {
    return await action();
  } on ClientException catch (e) {
    if (context.mounted) showSnack(context, e.message, error: true);
    return null;
  }
}

/// [guard] for void daemon calls: true on success, false (after the
/// snackbar) on failure.
Future<bool> guardOk(BuildContext context, Future<void> Function() action) async {
  try {
    await action();
    return true;
  } on ClientException catch (e) {
    if (context.mounted) showSnack(context, e.message, error: true);
    return false;
  }
}

/// A confirm dialog for destructive actions; resolves to true on confirm.
Future<bool> confirm(BuildContext context, String title, String message) async {
  final answer = await showDialog<bool>(
    context: context,
    builder: (context) => AlertDialog(
      title: Text(title),
      content: Text(message),
      actions: [
        TextButton(
            onPressed: () => Navigator.pop(context, false),
            child: const Text('Cancel')),
        FilledButton(
            onPressed: () => Navigator.pop(context, true),
            child: const Text('Confirm')),
      ],
    ),
  );
  return answer ?? false;
}
