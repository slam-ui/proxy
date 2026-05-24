# Endpoint review needed

Server endpoints below are not directly called by the split WebView UI in `internal/api/static/js/`.
They were not removed because they may be used by diagnostics, external scripts, future UI flows, or compatibility clients.

## R-001: Server-only API endpoints
- **Coordinates:** `internal/api/*.go`
- **Hypothesis:** These routes are intentional backend capabilities without a current direct UI fetch.
- **Why not fixed:** Removing them would change public/local API behavior without a product decision.
- **Suggested fix:** Product review should classify each endpoint as keep, hide from docs, expose in UI, or deprecate.
- **Confidence:** Suspect

Endpoints:
- `/api/apps/launch`
- `/api/apps/match`
- `/api/apps/processes/{pid:[0-9]+}`
- `/api/apps/rules`
- `/api/apps/rules/{id}`
- `/api/apps/rules/{id}/disable`
- `/api/apps/rules/{id}/enable`
- `/api/backup/export`
- `/api/backup/import`
- `/api/connections/inspect`
- `/api/debug/stats`
- `/api/diagnostics/ports`
- `/api/health`
- `/api/profiles/builtins`
- `/api/profiles/{name}`
- `/api/profiles/{name}/apply`
- `/api/proxy/toggle`
- `/api/security/dns-guard`
- `/api/security/network`
- `/api/servers/failover/settings`
- `/api/servers/fetch-url`
- `/api/servers/import-clipboard`
- `/api/servers/{id}`
- `/api/servers/{id}/connect`
- `/api/servers/{id}/latency-history`
- `/api/servers/{id}/ping`
- `/api/servers/{id}/qr`
- `/api/servers/{id}/real-ping`
- `/api/servers/{id}/refresh`
- `/api/settings/dns`
- `/api/settings/geosite-update`
- `/api/settings/proxy-guard`
- `/api/temporary-rules`
- `/api/temporary-rules/{value:.+}`
- `/api/traffic/by-process`
- `/api/tun/export`
- `/api/tun/rules/{value:.+}`

## R-002: UI route diff false positives
- **Coordinates:** `internal/api/static/js/30-rules-processes.js`, `internal/api/static/js/10-servers.js`
- **Hypothesis:** Raw text extraction reports `/api/profiles/`, `/api/servers/`, and `/api/tun/rules/` as missing, but each is a dynamic route prefix with an encoded path segment.
- **Why not fixed:** The actual server routes exist as `/api/profiles/{name}`, `/api/servers/{id}`, and `/api/tun/rules/{value:.+}`.
- **Suggested fix:** Keep the extraction artifact in `.audit/cleanup/endpoint_diff.txt`; no code change required.
- **Confidence:** Confirmed
