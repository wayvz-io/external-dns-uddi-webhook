# Changelog

## [0.2.0](https://github.com/wayvz-io/external-dns-uddi-webhook/compare/v0.1.2...v0.2.0) (2026-09-16)


### Bug Fixes

* **webhook:** stop TestRunServesUntilCancelled racing Go's Shutdown grace period ([1fe71d8](https://github.com/wayvz-io/external-dns-uddi-webhook/commit/1fe71d8fcd16a4c750520b61b62cf0b21be57907))
* **webhook:** stop TestRunServesUntilCancelled racing Go's Shutdown grace period ([4c21243](https://github.com/wayvz-io/external-dns-uddi-webhook/commit/4c212437f72ce463c459a372244605917bfa6d4e))


### Miscellaneous Chores

* mark v0.2.0 as the pre-public-release milestone ([b1893d8](https://github.com/wayvz-io/external-dns-uddi-webhook/commit/b1893d8ba1b99b9be1353dfd6b7101f31eb03cf4))

## [0.1.2](https://github.com/wayvz-io/external-dns-uddi-webhook/compare/v0.1.1...v0.1.2) (2026-09-15)


### Features

* **config:** implement UDDI_ZONE_FILTER ([a8466c1](https://github.com/wayvz-io/external-dns-uddi-webhook/commit/a8466c14457cede799b3a57dca7fb07a7c8f253d))
* **config:** implement UDDI_ZONE_FILTER ([b208ddf](https://github.com/wayvz-io/external-dns-uddi-webhook/commit/b208ddf3ed1244918673399304404499b9972584))

## [0.1.1](https://github.com/wayvz-io/external-dns-uddi-webhook/compare/v0.1.0...v0.1.1) (2026-09-14)


### Bug Fixes

* **provider:** match TXT records regardless of quoting ([23cd172](https://github.com/wayvz-io/external-dns-uddi-webhook/commit/23cd172f69ac712687a020678f471b112ade12f4))
* **uddi:** override TTL inheritance so record TTLs are served ([60afe56](https://github.com/wayvz-io/external-dns-uddi-webhook/commit/60afe5651a5c7a8a26d97678366f7c02fd3ae0f0))

## 0.1.0 (2026-09-14)


### Features

* ExternalDNS webhook provider for Infoblox Universal DDI ([bf3d408](https://github.com/wayvz-io/external-dns-uddi-webhook/commit/bf3d408427605d3865fbadb9d134db0f51344e13))
