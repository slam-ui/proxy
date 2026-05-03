# Kill Switch

Kill switch blocks traffic when the proxy tunnel is not healthy. It is designed
as a fail-close feature: if SafeSky or `sing-box` crashes, blocking rules can
remain active until the app or user explicitly clears them.

## When To Use It

Enable kill switch when leaking traffic is worse than temporarily losing network
access. Do not enable it if you need uninterrupted direct connectivity during
experiments.

## Behavior

- On connect, SafeSky prepares rules that allow tunnel traffic and required local
  control traffic.
- On disconnect, SafeSky removes rules.
- On crash, rules can remain active to avoid leaks.
- On next startup, SafeSky attempts cleanup according to the policy.

See [../KILLSWITCH_POLICY.md](../KILLSWITCH_POLICY.md) for the detailed policy.

## Recovery

If networking is blocked:

1. Start SafeSky as administrator.
2. Disable kill switch from the UI.
3. Disconnect/reconnect the active server.
4. If that fails, restart Windows and start SafeSky again as administrator.

Do not delete firewall rules manually unless you know which rules belong to
SafeSky.
