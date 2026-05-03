# Supported Protocols

SafeSky accepts server links and config snippets, then converts them into a
`sing-box` outbound.

## VLESS

Best supported protocol in the client. Supported transports:

- TCP with TLS or Reality.
- TCP with HTTP obfuscation.
- WebSocket.
- gRPC.
- HTTP/2.
- HTTPUpgrade.

Reality requires `pbk` and usually `sid`/`fp` parameters. XTLS Vision is allowed,
but multiplexing is disabled for Vision because the modes are incompatible.

## Trojan

Trojan links are supported with TLS transports such as WebSocket. Reality is not
accepted for Trojan links because it is not a Trojan mode in this builder.

## Shadowsocks

SIP002 links are supported. Legacy or unsafe ciphers are rejected by the parser.

## Hysteria2

Hysteria2 links are supported with TLS, bandwidth parameters, obfuscation, and
ALPN where provided by the URL.

## TUIC

TUIC links are supported with UUID/password credentials, congestion control, and
UDP relay mode. Invalid congestion-control values are rejected.

## WireGuard

WireGuard links and `.conf`-style snippets are supported. SafeSky uses a bounded
MTU range and can reuse cached per-server MTU values when a key does not specify
`mtu`.

## VMess

VMess links are supported for modern configurations. Legacy `alterId` settings
are rejected.

## Import Rules

- URLs must contain enough credentials to build a `sing-box` outbound.
- Unsupported or ambiguous parameters are rejected early.
- Server names and secrets are masked in logs and diagnostics.
- When in doubt, import one server first and run diagnostics before adding rules.
