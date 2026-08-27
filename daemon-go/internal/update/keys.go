package update

// Compiled-in minisign public keys for update-manifest verification.
// Two keys ship from day one: "current" signs routine releases; "recovery"
// exists only to rotate away from a lost or compromised current key (its
// secret half lives on separate offline media). A recovery-signed manifest
// is that rotation: every daemon that accepts one persists the current
// key's id as revoked (Check) and refuses current-signed manifests until a
// build with new keys is installed — never sign a routine release with it.
//
// The fixture signature in testdata/ is bound to a development key, not to
// the production keys below, so TestFixtureVerifiesWithCompiledKeys skips
// rather than pinning the chain. To pin it, re-sign the fixture with the
// production current secret key —
//
//	minisign -S -H -s <current.key> -x testdata/update.json.minisig \
//	  -t "version=v1.2.3 seq=7" -m testdata/update.json
const (
	// key id 30A28671EBF1FF62
	publicKeyCurrent = "RWRi//HrcYaiMEDXrHFnerX+PqNRGTp67NOU+8wNEXCg6ihJkSEQbvTp"
	// key id E19837F74716BA24
	publicKeyRecovery = "RWQkuhZH9zeY4RCxVOqDHDAfc4EYfAXcoc26lNAfD0tRM+BZkRNc2R9t"
)
