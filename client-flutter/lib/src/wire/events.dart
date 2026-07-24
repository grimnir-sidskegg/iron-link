part of 'wire.dart';

/// A daemon → client push frame on a `subscribe` connection, tagged by
/// `event`. The stream is lossy by design (per-subscriber buffer of 64,
/// laggards drop events) — treat every event as a hint, not a ledger.
sealed class Event {
  const Event();

  static Event fromJson(Map<String, Object?> json) => switch (json['event']) {
        'state' => StateEvent.fromJson(json),
        'traffic' => TrafficEvent.fromJson(json),
        'log' => LogEvent.fromJson(json),
        'subscription_updated' => SubscriptionUpdatedEvent.fromJson(json),
        'core_error' => CoreErrorEvent.fromJson(json),
        _ => UnknownEvent(json),
      };
}

/// `state` — a full session snapshot: the first frame on every subscribe
/// connection and one per transition. Idle is the BARE `{"event":"state"}`
/// — both fields default. Idempotent by design.
final class StateEvent extends Event {
  const StateEvent({this.entries = const [], this.active});

  StateEvent.fromJson(Map<String, Object?> json)
      : entries = _objList(json['entries'])
            .map(CoreEntry.fromJson)
            .toList(growable: false),
        active = json['active'] is Map<String, Object?>
            ? PersistedEntry.fromJson(json['active'] as Map<String, Object?>)
            : null;

  final List<CoreEntry> entries;
  final PersistedEntry? active;

  bool get isRunning => entries.any((e) => e.isRunning);
}

/// `traffic` — bytes moved in the LAST second (deltas, not cumulative).
final class TrafficEvent extends Event {
  const TrafficEvent({this.up = 0, this.down = 0});

  TrafficEvent.fromJson(Map<String, Object?> json)
      : up = _int(json['up']),
        down = _int(json['down']);

  final int up;
  final int down;
}

/// `log` — one sing-box log line via the in-process tap.
final class LogEvent extends Event {
  const LogEvent({this.level = '', this.message = ''});

  LogEvent.fromJson(Map<String, Object?> json)
      : level = _str(json['level']),
        message = _str(json['message']);

  final String level;
  final String message;
}

/// `subscription_updated` — a refresh landed; zero counts may be omitted.
final class SubscriptionUpdatedEvent extends Event {
  const SubscriptionUpdatedEvent({
    this.subId = '',
    this.added = 0,
    this.removed = 0,
    this.total = 0,
  });

  SubscriptionUpdatedEvent.fromJson(Map<String, Object?> json)
      : subId = _str(json['sub_id']),
        added = _int(json['added']),
        removed = _int(json['removed']),
        total = _int(json['total']);

  final String subId;
  final int added;
  final int removed;
  final int total;
}

/// `core_error` — declared on the wire as the G5 foundation; the daemon
/// has no emitter yet (ROADMAP).
final class CoreErrorEvent extends Event {
  const CoreErrorEvent({this.role, this.stage = '', this.message = ''});

  CoreErrorEvent.fromJson(Map<String, Object?> json)
      : role = _strOpt(json['role']),
        stage = _str(json['stage']),
        message = _str(json['message']);

  final String? role;
  final String stage;
  final String message;
}

/// An event with a tag this client does not know — skip, don't fail.
final class UnknownEvent extends Event {
  const UnknownEvent(this.raw);

  final Map<String, Object?> raw;
}
