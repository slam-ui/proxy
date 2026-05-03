# Telemetry

Telemetry is opt-in. Usage events and crash reports are controlled separately.

## What Is Collected

Usage event payloads can include:

- anonymous id
- client version
- OS identifier
- locale
- event type
- protocol/transport class
- coarse duration and byte counters

Crash report uploads use `CrashReport.SanitizedForUpload()` and must not contain
server names, domains, IP addresses, passwords, subscription URLs, or raw config.

## What Is Not Collected

- full server URLs
- UUIDs, private keys, passwords
- subscription URLs
- hostname/domain/IP from crash context
- local file contents

## Local State

- `data/telemetry_id`
- local buffered events in memory
- `data/crash-*.json` until upload succeeds or the user deletes them

Crash uploads are deferred while the machine is on battery power.

## Privacy Operations

The API supports privacy export and delete. Delete clears local telemetry state
and asks the telemetry endpoint to delete server-side state for the anonymous id.

## Adding Events

1. Keep the event name stable and low-cardinality.
2. Do not include identifiers, domains, IP addresses, or free-form user input.
3. Add a test that verifies sensitive strings cannot appear in the serialized
   payload.
