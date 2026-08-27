package update

// Compiled-in minisign public keys for update-manifest verification.
// Two keys ship from day one: "current" signs routine releases; "recovery"
// exists only to rotate away from a lost or compromised current key (its
// secret half lives on separate offline media).
//
// DEV PLACEHOLDERS — both values below belong to development keypairs used
// by the test suite. They must be replaced with owner-generated production
// public keys (minisign -G, secret keys kept offline) before the first
// signed release.
const (
	publicKeyCurrent  = "RWR20TNV+XbNGRSwWuy1EFlGhpTVO1QQWdBDiE5ILYVX9j07lu2G8Vjk"
	publicKeyRecovery = "RWTXl9R+z58vGaGjSCoILmdzrv7xLZFcheIBSTN6Pzba3i7XltP08YuE"
)
