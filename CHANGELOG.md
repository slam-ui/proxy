# Changelog

All notable changes to this project are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project uses semantic versioning once public releases begin.

## [Unreleased]

### Added

- Debug-gated `/debug/pprof/` routes.
- Reproducible performance benchmarks for throughput, connections, memory, and CPU streaming.
- Shared HTTP transport helpers and reused background HTTP clients.
- Battery-aware background behavior for keepalive, geo updates, and crash uploads.
- Persistent DNS cache identity for generated `sing-box` configs.
- Per-server MTU cache infrastructure for WireGuard defaults.
- User and developer documentation sections.

### Changed

- Subscription manager loading now happens after API route setup.
- Traffic stats reuse cached persisted totals when the stats file is unchanged.
- The Go runtime GC target defaults to a more conservative value unless `GOGC` is set.

### Fixed

- Avoided repeated transport creation in update, telemetry, leak-test, GeoIP, and geosite flows.
- Added regression coverage for subscription availability while the manager loads.

### Security

- Crash report uploads remain opt-in and sanitized.
- Battery mode defers crash uploads to avoid background network activity while unplugged.
