/// Platform-neutral tray menu model. Both backends consume this: the Linux
/// backend serves it as a `com.canonical.dbusmenu` tree; the Windows backend
/// serializes it across a method channel to build a native popup menu.
library;

/// One entry in the tray menu. Items may nest (a submenu carries [children])
/// and may be radio/checkbox toggles ([toggleType] + live [toggleState]).
class TrayMenuItem {
  TrayMenuItem({
    required this.id,
    this.label = '',
    this.labelFn,
    this.isSeparator = false,
    this.enabled,
    this.onClick,
    this.children = const [],
    this.toggleType,
    this.toggleState,
  });

  final int id;
  final String label;

  /// A live label, evaluated each draw (e.g. a node row whose `●`/`○` marker
  /// tracks the current selection). Wins over [label] when set.
  final String Function()? labelFn;
  final bool isSeparator;

  /// null ⇒ always enabled. Evaluated live each time the menu is drawn.
  final bool Function()? enabled;
  final void Function()? onClick;

  /// Submenu entries (empty for a leaf).
  final List<TrayMenuItem> children;

  /// 'radio' | 'checkmark' | null (not a toggle).
  final String? toggleType;

  /// Live toggle state: 1 = on, 0 = off. Evaluated each time the menu is drawn.
  final int Function()? toggleState;

  String get displayLabel => labelFn?.call() ?? label;
  bool get isSubmenu => children.isNotEmpty;
  bool get isEnabled => !isSeparator && (enabled?.call() ?? true);
}
