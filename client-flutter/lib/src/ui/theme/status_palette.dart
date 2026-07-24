/// The connection-status colours, as plain `dart:ui` constants so the two
/// system-tray renderers (which draw their status dot imperatively, without a
/// BuildContext or a Theme) and the in-app [AppColors] theme extension share a
/// single source of truth. Keeping this `dart:ui`-only means the tray backends
/// don't pull in `package:flutter/material`.
library;

import 'dart:ui' show Color;

/// Off / idle (daemon down or no session): a neutral grey.
/// Connecting / in-flight: amber. Connected / running: green.
abstract final class StatusPalette {
  static const Color off = Color(0xFF9E9E9E);
  static const Color connecting = Color(0xFFFFB300);
  static const Color connected = Color(0xFF00E676);
}
