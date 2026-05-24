# Quickstart

SafeSky is a Windows client for running `sing-box` with a local WebView2 UI.
The app listens only on `127.0.0.1` and controls the Windows system proxy.

## Requirements

- Windows 10/11 x64.
- `sing-box.exe` and `wintun.dll` next to `proxy-client.exe`. If `sing-box.exe`
  is missing, SafeSky downloads and verifies it automatically.

## First Run

1. Start `proxy-client.exe`.
2. Complete the onboarding wizard.
3. Add a server URL or import a subscription.
4. Select a server and click connect.
5. Wait until the tray/UI shows the proxy as ready.

The app stores runtime data in the install directory:

- `secret.key` is the active server URI.
- `config.singbox.json` is generated for `sing-box`.
- `data/routing.json` stores routing and DNS settings.
- `data/profiles/` stores profiles.
- `data/subscriptions/` stores encrypted subscription metadata.

## First Connection Checklist

If the first connection fails:

- Check that no other app uses ports `10807`, `9090`, or the SafeSky API port.
- Check that `sing-box.exe` is present and larger than 1 MiB.
- Run diagnostics from the UI.
- Try a simple VLESS/Trojan/Shadowsocks key before advanced routing rules.
- If TUN startup fails, restart Windows once to release stale driver state.

## Daily Use

- Use the tray menu for fast connect/disconnect and profile switching.
- Use profiles when you need separate work, gaming, or strict privacy setups.
- Keep telemetry disabled unless you intentionally opt in.
