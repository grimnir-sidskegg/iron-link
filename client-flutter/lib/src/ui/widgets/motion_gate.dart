/// [MotionGate] pauses every descendant [Ticker] whenever the app is not the
/// user's foreground concern — the window has lost focus/visibility, or the app
/// lifecycle has left [AppLifecycleState.resumed] — and resumes them on return.
///
/// WHY: the ambient animations in `motion.dart` (the power-orb pulse/breathe and
/// the pipeline thread's scrolling dashes + blurred glow dot) are driven by
/// `AnimationController.repeat()`, which never idles on its own. On the desktop
/// embedders a mere focus loss — a foreground fullscreen game, another window —
/// does NOT move the lifecycle off `resumed`, so without this gate Flutter keeps
/// rasterising those frames at the display refresh while the window sits
/// unfocused or occluded. That is continuous GPU work (the pipeline glow repaints
/// a `MaskFilter.blur` every frame) contending with whatever now has focus.
///
/// Wrapping the tree in a [TickerMode] whose `enabled` tracks lifecycle + focus
/// freezes all of those controllers in one place — the same mechanism Flutter
/// uses to pause offstage routes — with no per-controller plumbing, and lets them
/// resume seamlessly when the window comes back.
library;

import 'package:flutter/material.dart';
import 'package:window_manager/window_manager.dart';

class MotionGate extends StatefulWidget {
  const MotionGate({super.key, required this.child});

  final Widget child;

  @override
  State<MotionGate> createState() => _MotionGateState();
}

class _MotionGateState extends State<MotionGate>
    with WidgetsBindingObserver, WindowListener {
  // Assume active until the first event says otherwise: a freshly launched,
  // foreground window should animate from its first frame.
  bool _focused = true;
  bool _resumed = true;
  bool _windowHooked = false;

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    // window_manager is only ensureInitialized()'d where a tray backend set it
    // up (Linux/Windows, in main.dart). Hooking its focus events is best-effort:
    // where the plugin has no native window the lifecycle observer still gates.
    try {
      windowManager.addListener(this);
      _windowHooked = true;
    } catch (_) {}
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    if (_windowHooked) windowManager.removeListener(this);
    super.dispose();
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    final resumed = state == AppLifecycleState.resumed;
    if (resumed != _resumed) setState(() => _resumed = resumed);
  }

  @override
  void onWindowFocus() {
    if (!_focused) setState(() => _focused = true);
  }

  @override
  void onWindowBlur() {
    if (_focused) setState(() => _focused = false);
  }

  @override
  Widget build(BuildContext context) =>
      TickerMode(enabled: _resumed && _focused, child: widget.child);
}
