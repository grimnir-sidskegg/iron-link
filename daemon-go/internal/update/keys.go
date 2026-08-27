package update

// Compiled-in minisign public keys for update-manifest verification.
// Two keys ship from day one: "current" signs routine releases; "recovery"
// exists only to rotate away from a lost or compromised current key (its
// secret half lives on separate offline media). A recovery-signed manifest
// is that rotation: every daemon that accepts one persists the current
// key's id as revoked (Check) and refuses current-signed manifests until a
// build with new keys is installed — never sign a routine release with it.
//
// DEV PLACEHOLDERS — both values below belong to development keypairs used
// by the test suite. They must be replaced with owner-generated production
// public keys (minisign -G, secret keys kept offline) before the first
// signed release; the tag build in CI refuses to release while this marker
// is present. The fixture signature in testdata/ is bound to the dev
// current key: after replacing the keys, re-sign the fixture with the new
// current secret key —
//
//	minisign -S -H -s <current.key> -x testdata/update.json.minisig \
//	  -t "version=v1.2.3 seq=7" -m testdata/update.json
//
// — until then TestFixtureVerifiesWithCompiledKeys skips instead of
// pinning the chain.
const (
	publicKeyCurrent  = "RWR20TNV+XbNGRSwWuy1EFlGhpTVO1QQWdBDiE5ILYVX9j07lu2G8Vjk"
	publicKeyRecovery = "RWTXl9R+z58vGaGjSCoILmdzrv7xLZFcheIBSTN6Pzba3i7XltP08YuE"
)
