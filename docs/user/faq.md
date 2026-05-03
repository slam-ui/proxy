# FAQ

## Does SafeSky send telemetry?

Only after explicit opt-in. Usage events and crash reports are separate toggles.
Privacy export and delete actions are available through the local API/UI.

## Where are my keys stored?

The active server key is stored in `secret.key`. Subscription URLs are encrypted
before being stored under `data/subscriptions/`.

## Can I use SafeSky without TUN?

The app can control the Windows system proxy, but full leak protection and
process/routing behavior depend on the `sing-box` configuration and TUN support.

## Why does the app need administrator rights?

Administrator rights are required for Wintun cleanup, TUN startup, kill switch
rules, and some Windows proxy/firewall operations.

## Why does startup sometimes take longer?

Startup waits for stale Wintun adapters to be released after crashes or dirty
shutdowns. Clean shutdowns skip the heavy cleanup path when probes show no stale
adapter state.

## Why are crash reports left on disk?

Crash reports are saved locally first. Upload happens only if crash telemetry is
enabled and the machine is not on battery power.

## Can I edit `config.singbox.json` manually?

Manual mode exists, but generated mode is recommended. If manual mode is enabled
and the file is missing, SafeSky falls back to generated config.
