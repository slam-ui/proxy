# Adding A Protocol

Protocol parsing lives in `internal/config/protocols.go`. The parser returns a
`ParsedServer` with an `SBOutbound` that can be embedded into generated
`sing-box` config.

## Checklist

1. Add URL detection in `ParseServerContent`.
2. Parse and validate credentials.
3. Build an `SBOutbound` using fields supported by the pinned `sing-box`
   version.
4. Reject unsupported legacy or ambiguous settings.
5. Add unit tests for valid and invalid inputs.
6. Add a generated config test or extend `TestSingBoxCheck_VLESSTransports`
   when the protocol can be checked locally.
7. Update [../user/protocols.md](../user/protocols.md).

## Example Shape

```go
func parseExampleURL(raw string) (*ParsedServer, error) {
    u, err := url.Parse(raw)
    if err != nil {
        return nil, fmt.Errorf("example parse: %w", err)
    }
    host, portText, err := net.SplitHostPort(u.Host)
    if err != nil {
        return nil, fmt.Errorf("example endpoint must be host:port")
    }
    port, err := strconv.Atoi(portText)
    if err != nil {
        return nil, fmt.Errorf("example port: %w", err)
    }
    out := SBOutbound{Type: "example", Tag: "proxy-out", Server: host, ServerPort: port}
    return &ParsedServer{Proto: "example", DisplayName: host, Address: host, Port: port, Outbound: out}, nil
}
```

## Compatibility

Avoid adding `sing-box` fields by memory. Check the local binary with
`sing-box check` or the official schema for the target version. Unknown fields
are fatal at runtime.
