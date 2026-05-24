# Profiles

Profiles group settings that you may want to switch together: selected server,
routing rules, DNS settings, and UI preferences.

## Typical Profiles

| Profile | Example settings |
| --- | --- |
| Work | Stable server, strict routing, no extra safety toggles. |
| Media | Server with better throughput, streaming geosite rules. |
| Gaming | Low-latency server, conservative keepalive, fewer background probes. |
| Travel | Leak protection enabled, diagnostics visible, manual connect. |

## Switching Profiles

Use the UI or tray menu to activate a profile. SafeSky persists the selection and
applies the profile to runtime state. If the change affects `sing-box`, the app
queues a config apply or restart.

## Empty State

On first run, SafeSky creates default profile presets. You can start from the
default profile and add more only when you need different behavior.

## Notes

- Profiles are local files under `data/profiles/`.
- Do not share profile files if they reference private server settings.
- Profile switching is separate from subscription refresh; imported servers can
  be used by any profile.
