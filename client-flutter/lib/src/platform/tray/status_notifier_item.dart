/// The tray icon itself, served as an `org.kde.StatusNotifierItem` object.
///
/// We expose the icon as an `IconPixmap` (built by [TrayIconRenderer]) rather
/// than a themed `IconName`, and point `Menu` at our dbusmenu object so the
/// host shows the context menu on right-click. `ItemIsMenu` is false, so the
/// host calls `Activate` on left-click — we use that to toggle the window.
library;

import 'package:dbus/dbus.dart';

class StatusNotifierItem extends DBusObject {
  StatusNotifierItem({
    required this.menuPath,
    required DBusArray iconPixmap,
    this.onActivate,
    this.onSecondaryActivate,
  })  :
        // Named params can't be private, and the public param is `iconPixmap`
        // while the field is `_iconPixmap`, so an initializing formal can't apply.
        // ignore: prefer_initializing_formals
        _iconPixmap = iconPixmap,
        super(DBusObjectPath('/StatusNotifierItem'));

  static const _interface = 'org.kde.StatusNotifierItem';

  final DBusObjectPath menuPath;
  final void Function()? onActivate;
  final void Function()? onSecondaryActivate;

  DBusArray _iconPixmap;
  // Tooltip description line (under the bold title). Empty by default so the
  // hover shows "iron-link" ONCE (title), not twice (title + description).
  String _tooltip = '';

  /// Swap the icon (e.g. after a state change) and tell the host to re-read it.
  Future<void> setIcon(DBusArray pixmap) async {
    _iconPixmap = pixmap;
    await emitSignal(_interface, 'NewIcon');
  }

  /// Update the hover tooltip text and notify the host.
  Future<void> setTooltip(String text) async {
    _tooltip = text;
    await emitSignal(_interface, 'NewToolTip');
  }

  DBusValue _tooltipValue() => DBusStruct([
        const DBusString(''), // icon name
        DBusArray(DBusSignature('(iiay)'), []), // icon pixmap
        const DBusString('iron-link'), // title
        DBusString(_tooltip), // descriptive text
      ]);

  Map<String, DBusValue> _props() => {
        'Category': const DBusString('ApplicationStatus'),
        'Id': const DBusString('iron-link'),
        'Title': const DBusString('iron-link'),
        'Status': const DBusString('Active'),
        'WindowId': const DBusInt32(0),
        'IconName': const DBusString(''),
        'IconPixmap': _iconPixmap,
        'OverlayIconName': const DBusString(''),
        'AttentionIconName': const DBusString(''),
        'AttentionMovieName': const DBusString(''),
        'ToolTip': _tooltipValue(),
        'ItemIsMenu': const DBusBoolean(false),
        'Menu': menuPath,
      };

  @override
  Future<DBusMethodResponse> getProperty(String interface, String name) async {
    if (interface != _interface) {
      return DBusMethodErrorResponse.unknownInterface();
    }
    final v = _props()[name];
    if (v == null) return DBusMethodErrorResponse.unknownProperty();
    return DBusGetPropertyResponse(v);
  }

  @override
  Future<DBusMethodResponse> getAllProperties(String interface) async {
    // An empty interface means "all properties of this object" (legal in the
    // DBus spec, used by some importers) — match it too, not just our name.
    if (interface.isNotEmpty && interface != _interface) {
      return DBusGetAllPropertiesResponse({});
    }
    return DBusGetAllPropertiesResponse(_props());
  }

  @override
  List<DBusIntrospectInterface> introspect() {
    DBusIntrospectArgument inArg(String sig, String name) => DBusIntrospectArgument(
        DBusSignature(sig), DBusArgumentDirection.in_, name: name);
    DBusIntrospectProperty prop(String name, String sig) => DBusIntrospectProperty(
        name, DBusSignature(sig), access: DBusPropertyAccess.read);
    DBusIntrospectSignal sig(String name, [List<DBusIntrospectArgument> args = const []]) =>
        DBusIntrospectSignal(name, args: args);
    return [
      DBusIntrospectInterface(_interface, methods: [
        DBusIntrospectMethod('ContextMenu', args: [inArg('i', 'x'), inArg('i', 'y')]),
        DBusIntrospectMethod('Activate', args: [inArg('i', 'x'), inArg('i', 'y')]),
        DBusIntrospectMethod('SecondaryActivate',
            args: [inArg('i', 'x'), inArg('i', 'y')]),
        DBusIntrospectMethod('Scroll',
            args: [inArg('i', 'delta'), inArg('s', 'orientation')]),
      ], signals: [
        sig('NewTitle'),
        sig('NewIcon'),
        sig('NewAttentionIcon'),
        sig('NewOverlayIcon'),
        sig('NewToolTip'),
        sig('NewStatus', [
          DBusIntrospectArgument(
              DBusSignature('s'), DBusArgumentDirection.out, name: 'status'),
        ]),
      ], properties: [
        prop('Category', 's'),
        prop('Id', 's'),
        prop('Title', 's'),
        prop('Status', 's'),
        prop('WindowId', 'i'),
        prop('IconName', 's'),
        prop('IconPixmap', 'a(iiay)'),
        prop('OverlayIconName', 's'),
        prop('OverlayIconPixmap', 'a(iiay)'),
        prop('AttentionIconName', 's'),
        prop('AttentionIconPixmap', 'a(iiay)'),
        prop('AttentionMovieName', 's'),
        prop('ToolTip', '(sa(iiay)ss)'),
        prop('ItemIsMenu', 'b'),
        prop('IconThemePath', 's'),
        prop('Menu', 'o'),
      ]),
    ];
  }

  @override
  Future<DBusMethodResponse> handleMethodCall(DBusMethodCall methodCall) async {
    if (methodCall.interface != _interface) {
      return DBusMethodErrorResponse.unknownInterface();
    }
    switch (methodCall.name) {
      case 'Activate':
        onActivate?.call();
        return DBusMethodSuccessResponse([]);
      case 'SecondaryActivate':
        onSecondaryActivate?.call();
        return DBusMethodSuccessResponse([]);
      case 'ContextMenu': // the host draws the dbusmenu itself
      case 'Scroll':
        return DBusMethodSuccessResponse([]);
      default:
        return DBusMethodErrorResponse.unknownMethod();
    }
  }
}
