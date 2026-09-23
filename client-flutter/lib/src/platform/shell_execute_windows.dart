/// One-shot elevation on Windows: `ShellExecuteW` with the `runas` verb
/// launches a program through UAC, so a single consent prompt covers the
/// service-manager command the unprivileged client cannot run itself
/// (`sc.exe start iron-link`). Hand-written shell32 FFI — just
/// `package:ffi` for `Utf16`/`calloc`; no `package:win32` (owner decision
/// 2026-06-13, the same call as the named-pipe transport).
///
/// Importing this file executes nothing: `shell32.dll` is opened lazily
/// inside [_Shell32], and the first use only ever sits behind the Windows
/// branch of the caller — `flutter analyze` and `flutter test` on
/// Linux/macOS never touch it. Tests inject a [RunAs] stub instead.
library;

import 'dart:async';
import 'dart:ffi';
import 'dart:isolate';

import 'package:ffi/ffi.dart';

// ---- Win32 constants (winuser.h) ----

/// `SW_HIDE`: no window for the launched program. Whether this also
/// suppresses the console a console-subsystem target (sc.exe) creates for
/// itself is unverified — a brief console flash is possible.
const int _swHide = 0;

/// An elevated launcher: [shellExecuteRunAs] in production, a stub in
/// tests (the real one would pop a UAC prompt under `flutter test`). The
/// result may be synchronous so a stub can just return the code.
typedef RunAs = FutureOr<int> Function(String executable, String arguments);

/// The shell32 binding, resolved once per isolate.
///
/// Not `isLeaf`: the call blocks for as long as the consent prompt is up,
/// and a leaf call would hold off safepoints for the whole isolate group
/// meanwhile. There is no `GetLastError` to race with either — the return
/// value itself carries the failure code.
final class _Shell32 {
  _Shell32._(DynamicLibrary lib)
      : shellExecuteW = lib.lookupFunction<
            IntPtr Function(IntPtr, Pointer<Utf16>, Pointer<Utf16>,
                Pointer<Utf16>, Pointer<Utf16>, Int32),
            int Function(int, Pointer<Utf16>, Pointer<Utf16>, Pointer<Utf16>,
                Pointer<Utf16>, int)>('ShellExecuteW');

  static final _Shell32 instance =
      _Shell32._(DynamicLibrary.open('shell32.dll'));

  final int Function(int, Pointer<Utf16>, Pointer<Utf16>, Pointer<Utf16>,
      Pointer<Utf16>, int) shellExecuteW;
}

/// Launches [executable] with [arguments] elevated —
/// `ShellExecuteW(NULL, L"runas", executable, arguments, NULL, SW_HIDE)` —
/// and returns the raw result. The call blocks until the prompt is answered,
/// so it runs on a helper isolate: the UI isolate keeps pumping messages
/// meanwhile (a blocked window is ghosted as "Not Responding" after a few
/// seconds, which matters when the prompt is not on the secure desktop or
/// is slow to appear). The helper opens its own `shell32.dll` handle, the
/// same way the named-pipe transport does with kernel32.
///
/// `runas` is the standard way for an unprivileged process to request
/// elevation: the shell hands the launch to the AppInfo service, which
/// shows one UAC consent prompt and starts the target as administrator.
/// The client itself stays unprivileged.
///
/// Return value (the HINSTANCE as an int — a code, not a usable handle):
/// greater than 32 means the target was started; 5 (`SE_ERR_ACCESSDENIED`)
/// means the user declined the prompt; any other value at or below 32 is a
/// launch failure (`SE_ERR_FNF` 2 — file not found — and the rest of the
/// shellapi.h table). No COM initialisation is needed for a plain
/// executable target.
Future<int> shellExecuteRunAs(String executable, String arguments) =>
    Isolate.run(() => _shellExecute(executable, arguments));

int _shellExecute(String executable, String arguments) {
  final verb = 'runas'.toNativeUtf16();
  final file = executable.toNativeUtf16();
  final params = arguments.toNativeUtf16();
  try {
    return _Shell32.instance
        .shellExecuteW(0, verb, file, params, nullptr, _swHide);
  } finally {
    calloc.free(verb);
    calloc.free(file);
    calloc.free(params);
  }
}
