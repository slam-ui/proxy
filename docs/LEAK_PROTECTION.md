# Leak Protection

SafeSky exposes leak checks under `/api/leaktest/*`.

## DNS Leak Test

The client generates a random test id, resolves several names under the configured leak-test domain, then asks the configured report endpoint which resolvers observed the queries.

Settings live in `data/settings.json`:

```json
{
  "leak_test": {
    "enabled": true,
    "domain": "dnsleak.example.com",
    "report_url": "https://example.com/api/dnsleak/check",
    "expected_resolvers": ["1.1.1.1"],
    "check_interval_min": 30
  }
}
```

Run the minimal self-hosted server:

```powershell
go run ./tools/dnsleak-server -dns :53 -http :8088 -domain dnsleak.example.com
```

The DNS name must delegate wildcard queries for `*.dnsleak.example.com` to that server.

## IPv6 Leak Test

`POST /api/leaktest/ipv6` calls `https://api6.ipify.org`. If it returns an IPv6 address while SafeSky is expected to be IPv4-only, UI reports a risk.

IPv6 mitigation state is stored in `data/network_state.json`. On Windows, mitigation uses:

```powershell
netsh interface ipv6 set interface <iface> disabled
netsh interface ipv6 set interface <iface> enabled
```

## WebRTC

`GET /api/leaktest/webrtc` serves a local browser test page. WebRTC leaks are browser behavior; SafeSky detects and explains them, but browser configuration is still required to fully prevent local IP exposure.
