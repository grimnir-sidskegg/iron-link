/// The transport seam of the IPC layer: the minimal connection surface
/// [DaemonClient] needs (exactly the slice of `Socket` it used), so the
/// unix-socket and Windows named-pipe transports are interchangeable
/// behind one interface. Internal to `src/ipc/`.
library;

import 'dart:async';
import 'dart:io';

/// One byte-oriented connection to the daemon. Mirrors the `Socket`
/// surface the client consumes: [add] buffers outgoing bytes, [flush]
/// sends them, [incoming] is the raw inbound byte stream (it closes when
/// the peer does), and [destroy] tears the connection down.
abstract interface class IpcConnection {
  /// Buffers [bytes] for the next [flush].
  void add(List<int> bytes);

  /// Sends everything buffered so far.
  Future<void> flush();

  /// The inbound byte stream. Single-subscription; closes on EOF.
  Stream<List<int>> get incoming;

  /// Tears the connection down (both directions, idempotent).
  void destroy();
}

/// [IpcConnection] over a connected `dart:io` [Socket] — pure delegation;
/// the unix-socket transport on Linux/macOS.
final class SocketConnection implements IpcConnection {
  SocketConnection(this._socket);

  final Socket _socket;

  @override
  void add(List<int> bytes) => _socket.add(bytes);

  @override
  Future<void> flush() => _socket.flush();

  @override
  Stream<List<int>> get incoming => _socket;

  @override
  void destroy() => _socket.destroy();
}
