/// Exposes the app-wide [ThemeController] to any descendant via the element
/// tree, so deep widgets (the title-bar scheme dropdown, the Settings
/// Appearance card, the Home pipeline's reduce-motion check) read the active
/// theme + reduce-motion without threading the controller through constructors.
///
/// It's an [InheritedNotifier], so a widget that depends on it (via
/// [IronScope.of]) rebuilds when the controller notifies — exactly the reach we
/// want for `reduceMotion` and the scheme picker.
library;

import 'package:flutter/material.dart';

import '../app/theme_controller.dart';

class IronScope extends InheritedNotifier<ThemeController> {
  const IronScope({
    super.key,
    required ThemeController controller,
    required super.child,
  }) : super(notifier: controller);

  ThemeController get controller => notifier!;

  /// The controller, subscribing the caller to its changes. Use in `build`.
  static ThemeController of(BuildContext context) {
    final scope =
        context.dependOnInheritedWidgetOfExactType<IronScope>();
    assert(scope != null, 'IronScope.of() called with no IronScope ancestor');
    return scope!.controller;
  }

  /// The controller WITHOUT subscribing — for one-shot reads in callbacks.
  static ThemeController read(BuildContext context) {
    final scope =
        context.getInheritedWidgetOfExactType<IronScope>();
    assert(scope != null, 'IronScope.read() called with no IronScope ancestor');
    return scope!.controller;
  }

  /// Reduce-motion preference, safe to call where no [IronScope] is installed
  /// (e.g. a widget test that mounts a single page under a bare `MaterialApp`):
  /// returns `false` rather than asserting. Subscribes when a scope IS present,
  /// so a live toggle repaints the animated surfaces.
  static bool reduceMotionOf(BuildContext context) =>
      context
          .dependOnInheritedWidgetOfExactType<IronScope>()
          ?.controller
          .reduceMotion ??
      false;

  @override
  bool updateShouldNotify(IronScope oldWidget) =>
      controller != oldWidget.controller;
}
