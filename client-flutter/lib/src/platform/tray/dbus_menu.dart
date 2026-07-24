/// The tray's right-click menu, served as a `com.canonical.dbusmenu` object.
///
/// The StatusNotifierItem points its `Menu` property at this object's path;
/// the host (waybar, Plasma, …) calls `GetLayout`/`GetGroupProperties` to draw
/// the menu and `Event(id,"clicked",…)` when an item is chosen. We serve the
/// live model directly, so each open reflects the current enabled/toggle state;
/// on a structural change (e.g. the node list) we bump the revision and emit
/// `LayoutUpdated` so the host re-fetches.
library;

import 'package:dbus/dbus.dart';

import 'tray_menu_item.dart';

class TrayMenu extends DBusObject {
  TrayMenu(List<TrayMenuItem> items, {DBusObjectPath? path})
      : _root = TrayMenuItem(id: 0, children: items),
        super(path ?? DBusObjectPath('/MenuBar'));

  static const _interface = 'com.canonical.dbusmenu';
  TrayMenuItem _root;
  int _revision = 1;

  /// Replace the whole menu (e.g. after the node list changed) and tell the
  /// host to re-read it.
  Future<void> setItems(List<TrayMenuItem> items) async {
    _root = TrayMenuItem(id: 0, children: items);
    await notifyChanged();
  }

  TrayMenuItem? _item(int id) {
    TrayMenuItem? walk(TrayMenuItem it) {
      if (it.id == id) return it;
      for (final c in it.children) {
        final found = walk(c);
        if (found != null) return found;
      }
      return null;
    }

    return walk(_root);
  }

  Iterable<TrayMenuItem> _all() sync* {
    Iterable<TrayMenuItem> walk(TrayMenuItem it) sync* {
      yield it;
      for (final c in it.children) {
        yield* walk(c);
      }
    }

    yield* walk(_root);
  }

  /// The dbusmenu properties for one item, optionally filtered by [names]
  /// (empty ⇒ all). Values are returned unwrapped — `_propsDict` wraps them.
  Map<String, DBusValue> _properties(TrayMenuItem item, List<String> names) {
    final all = <String, DBusValue>{};
    if (item.isSeparator) {
      all['type'] = const DBusString('separator');
    } else {
      if (item.id != 0) {
        all['label'] = DBusString(item.displayLabel);
        all['enabled'] = DBusBoolean(item.isEnabled);
        all['visible'] = const DBusBoolean(true);
        if (item.toggleType != null) {
          all['toggle-type'] = DBusString(item.toggleType!);
          all['toggle-state'] = DBusInt32(item.toggleState?.call() ?? 0);
        }
      }
      if (item.isSubmenu) {
        all['children-display'] = const DBusString('submenu');
      }
    }
    if (names.isNotEmpty) all.removeWhere((k, _) => !names.contains(k));
    return all;
  }

  // DBusDict.stringVariant wraps each value in a variant ITSELF, so pass the
  // raw values — pre-wrapping would make `a{sv}` hold variant<variant<…>>,
  // which libdbusmenu can't unwrap (renders every item as "Empty Label").
  DBusValue _propsDict(TrayMenuItem item, List<String> names) =>
      DBusDict.stringVariant(_properties(item, names));

  /// The recursive `(ia{sv}av)` layout for [item], descending into submenus
  /// while [depth] allows (-1 = all, 0 = none, n = n levels).
  DBusStruct _layout(TrayMenuItem item, List<String> names, int depth) {
    final children = <DBusValue>[];
    if (item.isSubmenu && depth != 0) {
      for (final c in item.children) {
        children.add(DBusVariant(_layout(c, names, depth < 0 ? -1 : depth - 1)));
      }
    }
    return DBusStruct([
      DBusInt32(item.id),
      _propsDict(item, names),
      DBusArray(DBusSignature('v'), children),
    ]);
  }

  @override
  Future<DBusMethodResponse> getProperty(String interface, String name) async {
    if (interface != _interface) {
      return DBusMethodErrorResponse.unknownInterface();
    }
    switch (name) {
      case 'Version':
        return DBusGetPropertyResponse(const DBusUint32(3));
      case 'Status':
        return DBusGetPropertyResponse(const DBusString('normal'));
      case 'TextDirection':
        return DBusGetPropertyResponse(const DBusString('ltr'));
      case 'IconThemePath':
        return DBusGetPropertyResponse(DBusArray.string([]));
      default:
        return DBusMethodErrorResponse.unknownProperty();
    }
  }

  @override
  Future<DBusMethodResponse> getAllProperties(String interface) async {
    // Empty interface = "all properties of this object" (legal; used by some
    // importers) — match it too, not just our name.
    if (interface.isNotEmpty && interface != _interface) {
      return DBusGetAllPropertiesResponse({});
    }
    return DBusGetAllPropertiesResponse({
      'Version': const DBusUint32(3),
      'Status': const DBusString('normal'),
      'TextDirection': const DBusString('ltr'),
      'IconThemePath': DBusArray.string([]),
    });
  }

  @override
  Future<DBusMethodResponse> handleMethodCall(DBusMethodCall methodCall) async {
    if (methodCall.interface != _interface) {
      return DBusMethodErrorResponse.unknownInterface();
    }
    final args = methodCall.values;
    switch (methodCall.name) {
      case 'GetLayout':
        final parentId = args[0].asInt32();
        final depth = args[1].asInt32();
        final names = args[2].asStringArray().toList();
        final root = _item(parentId);
        if (root == null) return DBusMethodErrorResponse.invalidArgs();
        return DBusMethodSuccessResponse(
            [DBusUint32(_revision), _layout(root, names, depth)]);

      case 'GetGroupProperties':
        final ids = args[0].asInt32Array().toList();
        final names = args[1].asStringArray().toList();
        final out = <DBusValue>[];
        for (final it in _all()) {
          if (ids.isEmpty || ids.contains(it.id)) {
            out.add(DBusStruct([DBusInt32(it.id), _propsDict(it, names)]));
          }
        }
        return DBusMethodSuccessResponse(
            [DBusArray(DBusSignature('(ia{sv})'), out)]);

      case 'GetProperty':
        final it = _item(args[0].asInt32());
        final name = args[1].asString();
        if (it == null) return DBusMethodErrorResponse.invalidArgs();
        final v = _properties(it, [name])[name];
        return DBusMethodSuccessResponse([DBusVariant(v ?? const DBusString(''))]);

      case 'Event':
        _fire(args[0].asInt32(), args[1].asString());
        return DBusMethodSuccessResponse([]);

      case 'EventGroup':
        for (final e in args[0].asArray()) {
          final s = (e as DBusStruct).children;
          _fire(s[0].asInt32(), s[1].asString());
        }
        return DBusMethodSuccessResponse([DBusArray.int32([])]);

      case 'AboutToShow':
        return DBusMethodSuccessResponse([const DBusBoolean(false)]);

      case 'AboutToShowGroup':
        return DBusMethodSuccessResponse(
            [DBusArray.int32([]), DBusArray.int32([])]);

      default:
        return DBusMethodErrorResponse.unknownMethod();
    }
  }

  void _fire(int id, String eventId) {
    if (eventId != 'clicked') return;
    final it = _item(id);
    if (it != null && it.isEnabled && !it.isSubmenu) it.onClick?.call();
  }

  @override
  List<DBusIntrospectInterface> introspect() {
    DBusIntrospectArgument a(String sig, DBusArgumentDirection dir, String name) =>
        DBusIntrospectArgument(DBusSignature(sig), dir, name: name);
    const in_ = DBusArgumentDirection.in_;
    const out = DBusArgumentDirection.out;
    DBusIntrospectProperty prop(String name, String sig) => DBusIntrospectProperty(
        name, DBusSignature(sig), access: DBusPropertyAccess.read);
    return [
      DBusIntrospectInterface(_interface, methods: [
        DBusIntrospectMethod('GetLayout', args: [
          a('i', in_, 'parentId'),
          a('i', in_, 'recursionDepth'),
          a('as', in_, 'propertyNames'),
          a('u', out, 'revision'),
          a('(ia{sv}av)', out, 'layout'),
        ]),
        DBusIntrospectMethod('GetGroupProperties', args: [
          a('ai', in_, 'ids'),
          a('as', in_, 'propertyNames'),
          a('a(ia{sv})', out, 'properties'),
        ]),
        DBusIntrospectMethod('GetProperty',
            args: [a('i', in_, 'id'), a('s', in_, 'name'), a('v', out, 'value')]),
        DBusIntrospectMethod('Event', args: [
          a('i', in_, 'id'),
          a('s', in_, 'eventId'),
          a('v', in_, 'data'),
          a('u', in_, 'timestamp'),
        ]),
        DBusIntrospectMethod('EventGroup',
            args: [a('a(isvu)', in_, 'events'), a('ai', out, 'idErrors')]),
        DBusIntrospectMethod('AboutToShow',
            args: [a('i', in_, 'id'), a('b', out, 'needUpdate')]),
        DBusIntrospectMethod('AboutToShowGroup', args: [
          a('ai', in_, 'ids'),
          a('ai', out, 'updatesNeeded'),
          a('ai', out, 'idErrors'),
        ]),
      ], signals: [
        DBusIntrospectSignal('ItemsPropertiesUpdated', args: [
          a('a(ia{sv})', out, 'updatedProps'),
          a('a(ias)', out, 'removedProps'),
        ]),
        DBusIntrospectSignal('LayoutUpdated',
            args: [a('u', out, 'revision'), a('i', out, 'parent')]),
        DBusIntrospectSignal('ItemActivationRequested',
            args: [a('i', out, 'id'), a('u', out, 'timestamp')]),
      ], properties: [
        prop('Version', 'u'),
        prop('TextDirection', 's'),
        prop('Status', 's'),
        prop('IconThemePath', 'as'),
      ]),
    ];
  }

  /// Tell the host the menu changed so it re-reads the layout.
  Future<void> notifyChanged() async {
    _revision++;
    await emitSignal(
        _interface, 'LayoutUpdated', [DBusUint32(_revision), const DBusInt32(0)]);
  }
}
