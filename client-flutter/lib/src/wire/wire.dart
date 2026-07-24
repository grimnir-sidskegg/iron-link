/// Dart mirror of the iron-link control-plane wire protocol.
///
/// Sources of truth: `contract/fixtures/` (the canonical frames — if code
/// and fixtures disagree, the fixtures win) and `daemon-go/internal/api`
/// (the Go emitter).
///
/// Discipline, same as the Go side: EMIT exactly — a request's
/// `toJson()` must equal the fixture as a JSON value (explicit nulls
/// included where the fixtures carry them); DECODE leniently — missing
/// fields take defaults, unknown fields are ignored, and an unknown
/// `status`/`event` tag maps to an Unknown* variant instead of an error.
///
/// Pure Dart (no Flutter imports) so the wire layer is usable from plain
/// `dart` tooling and tests.
library;

part 'types.dart';
part 'requests.dart';
part 'responses.dart';
part 'events.dart';
