# Routing

Routing rules decide whether traffic should go through the proxy, go direct, or
be blocked.

## Rule Types

| Type | Example | Notes |
| --- | --- | --- |
| Domain | `example.com` | Matches normalized hostnames. |
| IP/CIDR | `203.0.113.0/24` | Use for fixed IP ranges. |
| Process | `chrome.exe` | Requires process detection support in `sing-box`. |
| Geosite | `geosite:youtube` | Uses downloaded `geosite-*.bin` rule sets. |

## Actions

| Action | Meaning |
| --- | --- |
| Proxy | Send matching traffic through the selected server. |
| Direct | Bypass the proxy. |
| Block | Reject matching traffic. |

## DNS

Remote DNS is sent through the proxy tunnel. Direct DNS is used for local inbound
traffic. The generated `sing-box` config enables persistent DNS cache through
`data/dns_cache.db`.

## Examples

Proxy a service:

```text
Type: domain
Value: api.example.com
Action: proxy
```

Bypass a local network:

```text
Type: ip
Value: 192.168.0.0/16
Action: direct
```

Block ads:

```text
Type: geosite
Value: geosite:category-ads-all
Action: block
```

## Safety

- Localhost and private networks are protected by default route exclusions.
- DNS hijack rules are placed before normal routing rules.
- Invalid rule types are sanitized when routing config is loaded.
