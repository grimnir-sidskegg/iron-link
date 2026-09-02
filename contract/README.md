# Wire-contract fixtures

The canonical JSON frames of the iron-link control protocol (framed JSON over
IPC: 4-byte big-endian length prefix + body). They are the cross-language
contract between the Go daemon and the Flutter client, enforced by TWO test
suites that must both stay green:

- Go: `daemon-go/internal/api/contract_test.go`
  (`cd daemon-go && go test ./internal/api/`)
- Dart: `client-flutter/test/contract_test.dart`
  (`cd client-flutter && flutter test test/contract_test.dart`)

Each side asserts the direction it actually speaks on the wire:

| Directory    | Emitter        | Decoder      | Emission pinned by   | Decode pinned by |
|--------------|----------------|--------------|----------------------|------------------|
| `requests/`  | Dart client    | Go daemon    | Dart test (exact)    | Go test          |
| `responses/` | Go daemon      | Dart client  | Go test (exact)      | Dart test        |
| `events/`    | Go daemon      | Dart client  | Go test (exact)      | Dart test        |

"Exact" means the language's serializer output must equal the fixture modulo
key order — including which fields are omitted vs `null`. The decode side is
deliberately lenient (serde defaults / ignored unknown fields), and the tests
pin that leniency too (e.g. `responses/ok_add_node.json`,
`events/state_idle.json`).

Both tests enforce coverage in both directions: every fixture file needs a
case in the language's table and vice versa. So the workflow for any protocol
change is: edit the fixture(s) + update BOTH tables. Neither side can drift
without the other side's suite failing.

(The `go/**` split — verbs only the Flutter client spoke — was dissolved at
P0 when the store/diagnose/settings verbs were promoted into the shared set;
every verb is now part of the one shared contract.)
