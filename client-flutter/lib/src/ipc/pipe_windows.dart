/// The Windows named-pipe transport: `dart:io` has no named-pipe client,
/// so this is hand-written kernel32 FFI (just `package:ffi` for
/// `Utf16`/`calloc`; no `package:win32` — owner decision 2026-06-13).
///
/// Shape: the pipe opens with `FILE_FLAG_OVERLAPPED`; a dedicated reader
/// isolate blocks in `ReadFile` + `WaitForMultipleObjects` and ships
/// chunks back over a port; [WindowsPipeConnection.destroy] signals a
/// cancel event — safe from any isolate, deterministic wake — and the
/// handle is closed ONLY after the reader's terminal message, so a
/// handle-reuse race is excluded structurally. Writes run a short-lived
/// `Isolate.run` per flush (the protocol sends exactly one flush per
/// request).
///
/// Importing this file executes nothing: `kernel32.dll` is opened lazily
/// inside [_Kernel32], and the first use only ever sits behind the
/// `Platform.isWindows` branch in `client.dart` — `flutter analyze` and
/// `flutter test` on Linux/macOS never touch it.
library;

import 'dart:async';
import 'dart:ffi';
import 'dart:isolate';
import 'dart:typed_data';

import 'package:ffi/ffi.dart';

import 'transport.dart';

// ---- Win32 constants (winnt.h / winerror.h) ----

const int _genericRead = 0x80000000;
const int _genericWrite = 0x40000000;
const int _openExisting = 3;
const int _fileFlagOverlapped = 0x40000000;

const int _errorFileNotFound = 2; // no pipe instance exists (daemon down)
const int _errorBrokenPipe = 109; // the server end closed
const int _errorSemTimeout = 121; // WaitNamedPipeW timed out
const int _errorPipeBusy = 231; // all instances busy; wait and retry
const int _errorPipeNotConnected = 233; // server disconnected the instance
const int _errorOperationAborted = 995; // CancelIoEx reaped the read
const int _errorIoPending = 997; // overlapped operation is in flight

const int _waitObject0 = 0;
const int _infinite = 0xFFFFFFFF;
const int _invalidHandleValue = -1;

// A write to a LOCAL pipe completes in milliseconds; this bound only trips if
// the daemon stopped reading. It exists so a never-queued (synchronously
// failed) overlapped write surfaces as an error instead of blocking forever on
// GetOverlappedResult — see _writeSync.
const int _writeTimeoutMs = 30000;

/// Read-buffer size per `ReadFile` call. Frames larger than this arrive
/// as several chunks; `FrameDecoder` is chunk-agnostic by design.
const int _chunkBytes = 64 * 1024;

/// A Win32 failure as it crosses isolate boundaries: the failing
/// operation plus the `GetLastError` code (sendable: String + int).
final class PipeError implements Exception {
  const PipeError(this.op, this.code);

  /// The pipe operation that failed (`connect`, `read`, `write`, ...).
  final String op;

  /// The Win32 error code, captured immediately after the failing call.
  final int code;

  @override
  String toString() => 'PipeError($op: ${_describeCode(code)})';
}

String _describeCode(int code) => switch (code) {
      _errorFileNotFound => 'ERROR_FILE_NOT_FOUND ($code)',
      _errorBrokenPipe => 'ERROR_BROKEN_PIPE ($code)',
      _errorSemTimeout => 'ERROR_SEM_TIMEOUT ($code)',
      _errorPipeBusy => 'ERROR_PIPE_BUSY ($code)',
      _errorPipeNotConnected => 'ERROR_PIPE_NOT_CONNECTED ($code)',
      _errorOperationAborted => 'ERROR_OPERATION_ABORTED ($code)',
      _ => 'Win32 error $code',
    };

/// `OVERLAPPED` (minwinbase.h). The `Offset`/`OffsetHigh` pair mirrors
/// the C union's layout; pipes ignore offsets but the fields must exist.
final class _Overlapped extends Struct {
  @IntPtr()
  external int internal;

  @IntPtr()
  external int internalHigh;

  @Uint32()
  external int offset;

  @Uint32()
  external int offsetHigh;

  @IntPtr()
  external int hEvent;
}

/// The kernel32 bindings, resolved once per isolate.
///
/// `GetLastError` discipline: it is the ONLY `isLeaf` binding (a leaf
/// call cannot trigger a safepoint between the fallible call and the
/// error fetch; the blocking calls — ReadFile/WriteFile/Wait*/
/// GetOverlappedResult/CreateFileW — must NOT be leaf or they would
/// block safepoints for the whole isolate group). Callers fetch the
/// error immediately after the fallible call in the same isolate, and
/// perform every pointer allocation BEFORE the fallible call.
///
/// CAVEAT: even with that discipline, GetLastError is NOT reliable right
/// after a non-leaf call (ReadFile/WriteFile) — the VM's native→Dart
/// return transition can run a GC whose own Win32 calls reset the
/// thread's last-error to 0 before our getLastError() runs. So the
/// overlapped read/write paths do NOT trust GetLastError to tell
/// "pending" (ERROR_IO_PENDING) from "failed"; they treat a reported 0 as
/// "pending" and let the operation's EVENT + GetOverlappedResult be the
/// real adjudicator (bounded, so a genuine sync failure cannot hang).
final class _Kernel32 {
  _Kernel32._(DynamicLibrary lib)
      : createFileW = lib.lookupFunction<
            IntPtr Function(Pointer<Utf16>, Uint32, Uint32, Pointer<Void>,
                Uint32, Uint32, IntPtr),
            int Function(Pointer<Utf16>, int, int, Pointer<Void>, int, int,
                int)>('CreateFileW'),
        waitNamedPipeW = lib.lookupFunction<
            Int32 Function(Pointer<Utf16>, Uint32),
            int Function(Pointer<Utf16>, int)>('WaitNamedPipeW'),
        readFile = lib.lookupFunction<
            Int32 Function(IntPtr, Pointer<Uint8>, Uint32, Pointer<Uint32>,
                Pointer<_Overlapped>),
            int Function(int, Pointer<Uint8>, int, Pointer<Uint32>,
                Pointer<_Overlapped>)>('ReadFile'),
        writeFile = lib.lookupFunction<
            Int32 Function(IntPtr, Pointer<Uint8>, Uint32, Pointer<Uint32>,
                Pointer<_Overlapped>),
            int Function(int, Pointer<Uint8>, int, Pointer<Uint32>,
                Pointer<_Overlapped>)>('WriteFile'),
        getOverlappedResult = lib.lookupFunction<
            Int32 Function(IntPtr, Pointer<_Overlapped>, Pointer<Uint32>,
                Int32),
            int Function(int, Pointer<_Overlapped>, Pointer<Uint32>,
                int)>('GetOverlappedResult'),
        cancelIoEx = lib.lookupFunction<
            Int32 Function(IntPtr, Pointer<_Overlapped>),
            int Function(int, Pointer<_Overlapped>)>('CancelIoEx'),
        createEventW = lib.lookupFunction<
            IntPtr Function(Pointer<Void>, Int32, Int32, Pointer<Utf16>),
            int Function(Pointer<Void>, int, int,
                Pointer<Utf16>)>('CreateEventW'),
        setEvent = lib
            .lookupFunction<Int32 Function(IntPtr), int Function(int)>(
                'SetEvent'),
        resetEvent = lib
            .lookupFunction<Int32 Function(IntPtr), int Function(int)>(
                'ResetEvent'),
        waitForMultipleObjects = lib.lookupFunction<
            Uint32 Function(Uint32, Pointer<IntPtr>, Int32, Uint32),
            int Function(
                int, Pointer<IntPtr>, int, int)>('WaitForMultipleObjects'),
        waitForSingleObject = lib.lookupFunction<
            Uint32 Function(IntPtr, Uint32),
            int Function(int, int)>('WaitForSingleObject'),
        closeHandle = lib
            .lookupFunction<Int32 Function(IntPtr), int Function(int)>(
                'CloseHandle'),
        getLastError =
            lib.lookupFunction<Uint32 Function(), int Function()>(
                'GetLastError',
                isLeaf: true);

  /// Lazy per-isolate singleton — `kernel32.dll` opens on first access,
  /// which only ever happens on Windows.
  static final _Kernel32 instance =
      _Kernel32._(DynamicLibrary.open('kernel32.dll'));

  final int Function(Pointer<Utf16>, int, int, Pointer<Void>, int, int, int)
      createFileW;
  final int Function(Pointer<Utf16>, int) waitNamedPipeW;
  final int Function(
          int, Pointer<Uint8>, int, Pointer<Uint32>, Pointer<_Overlapped>)
      readFile;
  final int Function(
          int, Pointer<Uint8>, int, Pointer<Uint32>, Pointer<_Overlapped>)
      writeFile;
  final int Function(int, Pointer<_Overlapped>, Pointer<Uint32>, int)
      getOverlappedResult;
  final int Function(int, Pointer<_Overlapped>) cancelIoEx;
  final int Function(Pointer<Void>, int, int, Pointer<Utf16>) createEventW;
  final int Function(int) setEvent;
  final int Function(int) resetEvent;
  final int Function(int, Pointer<IntPtr>, int, int) waitForMultipleObjects;
  final int Function(int, int) waitForSingleObject;
  final int Function(int) closeHandle;
  final int Function() getLastError;
}

/// [IpcConnection] over a Windows named pipe (the daemon's
/// `\\.\pipe\iron-link`, byte mode — go-winio's default; no
/// `SetNamedPipeHandleState` needed or wanted).
///
/// Backpressure is deliberately absent, matching `dart:io`'s `Socket`:
/// this is a local channel with at most one 16-MiB frame in flight.
final class WindowsPipeConnection implements IpcConnection {
  WindowsPipeConnection._(this._handle, this._cancelEvent, this._port) {
    // The connection itself consumes the reader port, so the terminal
    // message is processed (and the handles released) even if nobody
    // ever listens to [incoming].
    _port.listen(_onReaderMessage);
  }

  final int _handle;
  final int _cancelEvent;
  final ReceivePort _port;
  final BytesBuilder _outgoing = BytesBuilder(copy: true);
  final StreamController<Uint8List> _incoming = StreamController<Uint8List>();

  /// Set by [destroy]; guards add/flush with a [StateError] (Socket
  /// parity).
  bool _destroyed = false;

  /// Set when the reader's terminal message has been processed — the
  /// pipe handle and cancel event are closed from that point on.
  bool _closed = false;

  /// Dials [name] (e.g. `\\.\pipe\iron-link`). The busy-wait/retry loop
  /// and the deadline live in [_connectSync], off the main isolate.
  /// Failures surface as [PipeError] — `client.dart` maps them to
  /// `DaemonUnreachable`, exactly like a unix `SocketException`.
  static Future<WindowsPipeConnection> connect(String name,
      {required Duration timeout}) async {
    final timeoutMs = timeout.inMilliseconds;
    final handle = await Isolate.run(() => _connectSync(name, timeoutMs));
    final k32 = _Kernel32.instance;
    // Manual-reset, initially non-signalled: once destroy() sets it, it
    // stays signalled — the reader observes it whatever it is doing.
    final cancelEvent = k32.createEventW(nullptr, 1, 0, nullptr);
    if (cancelEvent == 0) {
      final err = k32.getLastError();
      k32.closeHandle(handle);
      throw PipeError('create_event', err);
    }
    final port = ReceivePort();
    final connection = WindowsPipeConnection._(handle, cancelEvent, port);
    await Isolate.spawn(
        _readLoop, _ReaderArgs(handle, cancelEvent, _chunkBytes, port.sendPort));
    return connection;
  }

  @override
  void add(List<int> bytes) {
    if (_destroyed) {
      throw StateError('add() on a destroyed pipe connection');
    }
    _outgoing.add(bytes);
  }

  @override
  Future<void> flush() async {
    if (_destroyed) {
      throw StateError('flush() on a destroyed pipe connection');
    }
    final bytes = _outgoing.takeBytes();
    // If the reader already terminated, the handle is closed: discard
    // the write (Socket parity — writing after a REMOTE close is not an
    // error; the caller sees EOF on [incoming] instead).
    if (_closed || bytes.isEmpty) return;
    final handle = _handle;
    await Isolate.run(() => _writeSync(handle, bytes));
  }

  @override
  Stream<List<int>> get incoming => _incoming.stream;

  @override
  void destroy() {
    if (_destroyed) return;
    _destroyed = true;
    // The reader may have terminated first (the daemon closes the
    // connection after each non-subscribe reply): everything is already
    // released, and the cancel event no longer exists to signal.
    if (_closed) return;
    // SetEvent is safe from any isolate and deterministically wakes the
    // reader, which cancels the in-flight read and sends the terminal
    // message; teardown completes in [_onReaderMessage].
    _Kernel32.instance.setEvent(_cancelEvent);
  }

  void _onReaderMessage(Object? message) {
    if (message is Uint8List) {
      _incoming.add(message);
      return;
    }
    // The terminal int: the reader is done with the handle. Release
    // everything exactly once — the handle is never closed before this
    // point, so a handle-reuse race cannot happen.
    final code = message as int;
    _closed = true;
    final k32 = _Kernel32.instance;
    k32.closeHandle(_handle);
    k32.closeHandle(_cancelEvent);
    // Without this, the live ReceivePort keeps the VM (and
    // `flutter test`) from ever exiting.
    _port.close();
    if (code != 0) {
      _incoming.addError(PipeError('read', code));
    }
    _incoming.close();
  }
}

/// Opens the pipe, retrying while every instance is busy. Runs inside
/// `Isolate.run`; the whole loop observes one deadline ([timeoutMs]).
int _connectSync(String name, int timeoutMs) {
  final k32 = _Kernel32.instance;
  final elapsed = Stopwatch()..start();
  // Allocated before any fallible call (GetLastError discipline).
  final namePtr = name.toNativeUtf16();
  try {
    while (true) {
      final handle = k32.createFileW(namePtr, _genericRead | _genericWrite,
          0, nullptr, _openExisting, _fileFlagOverlapped, 0);
      if (handle != _invalidHandleValue) return handle;
      final err = k32.getLastError();
      if (err != _errorPipeBusy) {
        // ERROR_FILE_NOT_FOUND (no daemon) and everything else.
        throw PipeError('connect', err);
      }
      final remaining = timeoutMs - elapsed.elapsedMilliseconds;
      if (remaining <= 0) throw const PipeError('connect', _errorSemTimeout);
      if (k32.waitNamedPipeW(namePtr, remaining) == 0) {
        throw PipeError('connect', k32.getLastError());
      }
      // A successful wait does NOT reserve an instance — another client
      // may grab it first; loop back to CreateFileW.
    }
  } finally {
    calloc.free(namePtr);
  }
}

/// Writes [bytes] fully. Runs inside `Isolate.run` — one short-lived
/// isolate per flush (the protocol sends exactly one flush per request).
void _writeSync(int handle, Uint8List bytes) {
  final k32 = _Kernel32.instance;
  // Every allocation happens up front: nothing may run between a
  // fallible call and its GetLastError.
  final buf = calloc<Uint8>(bytes.length);
  final overlapped = calloc<_Overlapped>();
  final written = calloc<Uint32>();
  final event = k32.createEventW(nullptr, 1, 0, nullptr);
  if (event == 0) {
    final err = k32.getLastError();
    calloc.free(buf);
    calloc.free(overlapped);
    calloc.free(written);
    throw PipeError('create_event', err);
  }
  try {
    buf.asTypedList(bytes.length).setAll(0, bytes);
    var offset = 0;
    while (offset < bytes.length) {
      overlapped.ref
        ..internal = 0
        ..internalHigh = 0
        ..offset = 0
        ..offsetHigh = 0
        ..hEvent = event;
      k32.resetEvent(event);
      final ok = k32.writeFile(
          handle,
          Pointer<Uint8>.fromAddress(buf.address + offset),
          bytes.length - offset,
          nullptr,
          overlapped);
      if (ok == 0) {
        // FALSE is the NORMAL overlapped path (ERROR_IO_PENDING). We do NOT
        // read GetLastError to confirm that: a GC at WriteFile's FFI return
        // transition can reset the thread's last-error to 0, surfacing a
        // spurious PipeError(write, 0). Wait on the operation's own event
        // instead — the kernel signals it on completion. A genuine synchronous
        // failure never queues the op and never signals, so the bounded wait
        // turns that into a clear error rather than trusting a clobbered code
        // (or blocking forever in GetOverlappedResult below).
        final waited = k32.waitForSingleObject(event, _writeTimeoutMs);
        if (waited != _waitObject0) {
          k32.cancelIoEx(handle, overlapped);
          // Reap the cancelled op so the kernel is done with buf/overlapped
          // before `finally` frees them.
          k32.getOverlappedResult(handle, overlapped, written, 1);
          throw const PipeError('write', _errorSemTimeout);
        }
      }
      // The write is complete (synchronous success, or the event fired):
      // GetOverlappedResult returns at once with the byte count.
      if (k32.getOverlappedResult(handle, overlapped, written, 1) == 0) {
        throw PipeError('write', k32.getLastError());
      }
      if (written.value == 0) {
        // Cannot happen on a pipe write that "succeeded", but a silent
        // infinite loop would be worse than a surfaced error.
        throw const PipeError('write', 0);
      }
      offset += written.value;
    }
  } finally {
    k32.closeHandle(event);
    calloc.free(buf);
    calloc.free(overlapped);
    calloc.free(written);
  }
}

/// `Isolate.spawn` argument for [_readLoop] — sendable: ints + SendPort.
final class _ReaderArgs {
  const _ReaderArgs(this.handle, this.cancelEvent, this.chunkBytes, this.port);

  final int handle;
  final int cancelEvent;
  final int chunkBytes;
  final SendPort port;
}

/// The reader isolate: overlapped `ReadFile`, then
/// `WaitForMultipleObjects([cancelEvent, readEvent])`. Port protocol:
/// zero or more `Uint8List` chunks, then EXACTLY ONE terminal `int` —
/// 0 for a clean close (EOF after the daemon's `conn.Close()`, or
/// cancellation via destroy()), otherwise the Win32 error code.
void _readLoop(_ReaderArgs args) {
  final k32 = _Kernel32.instance;
  // All allocations before the loop (GetLastError discipline).
  final buf = calloc<Uint8>(args.chunkBytes);
  final overlapped = calloc<_Overlapped>();
  final bytesRead = calloc<Uint32>();
  final waitHandles = calloc<IntPtr>(2);
  final readEvent = k32.createEventW(nullptr, 1, 0, nullptr);
  if (readEvent == 0) {
    final err = k32.getLastError();
    calloc.free(buf);
    calloc.free(overlapped);
    calloc.free(bytesRead);
    calloc.free(waitHandles);
    args.port.send(err);
    return;
  }
  waitHandles[0] = args.cancelEvent;
  waitHandles[1] = readEvent;
  var terminal = 0;
  try {
    while (true) {
      overlapped.ref
        ..internal = 0
        ..internalHigh = 0
        ..offset = 0
        ..offsetHigh = 0
        ..hEvent = readEvent;
      k32.resetEvent(readEvent);
      final ok =
          k32.readFile(args.handle, buf, args.chunkBytes, nullptr, overlapped);
      if (ok == 0) {
        final err = k32.getLastError();
        // err != 0 guard: like the write path, GetLastError can be clobbered
        // to 0 by a GC at ReadFile's FFI return transition. A spurious 0 would
        // otherwise break with terminal=0 (a false clean EOF mid-stream).
        // Treat 0 as "pending" and let the wait + GetOverlappedResult decide;
        // the cancelEvent is always there to release a never-queued read.
        if (err != _errorIoPending && err != 0) {
          terminal = err;
          break;
        }
      }
      // The read is in flight — or already complete, which also signals
      // readEvent. cancelEvent sits at index 0, so it wins when both are
      // signalled: a destroy() cannot be starved by a busy pipe.
      final wait = k32.waitForMultipleObjects(2, waitHandles, 0, _infinite);
      if (wait == _waitObject0) {
        // destroy(): abort the pending read, then reap it — the kernel
        // owns the buffer and the OVERLAPPED until the operation ends.
        k32.cancelIoEx(args.handle, overlapped);
        k32.getOverlappedResult(args.handle, overlapped, bytesRead, 1);
        terminal = _errorOperationAborted;
        break;
      }
      if (k32.getOverlappedResult(args.handle, overlapped, bytesRead, 1) ==
          0) {
        terminal = k32.getLastError();
        break;
      }
      final n = bytesRead.value;
      if (n > 0) {
        // Copy out: a Pointer-backed view is not sendable across
        // isolates.
        args.port.send(Uint8List.fromList(buf.asTypedList(n)));
      }
    }
  } finally {
    k32.closeHandle(readEvent);
    calloc.free(buf);
    calloc.free(overlapped);
    calloc.free(bytesRead);
    calloc.free(waitHandles);
    args.port.send(_isCleanClose(terminal) ? 0 : terminal);
  }
}

/// Codes that mean "the conversation is over", not "something broke":
/// the server end closed (broken pipe / not connected) or our own
/// destroy() aborted the read.
bool _isCleanClose(int code) =>
    code == 0 ||
    code == _errorBrokenPipe ||
    code == _errorPipeNotConnected ||
    code == _errorOperationAborted;
