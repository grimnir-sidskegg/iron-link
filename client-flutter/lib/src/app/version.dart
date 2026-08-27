/// The client's own build version, stamped at build time with
/// `--dart-define=IRON_LINK_VERSION=vX.Y.Z` (CI and the PKGBUILD both pass
/// it). A plain `flutter run`/`build` leaves "dev". A compile-time constant on
/// purpose — no package_info_plus, no runtime asset read.
library;

const clientVersion =
    String.fromEnvironment('IRON_LINK_VERSION', defaultValue: 'dev');
