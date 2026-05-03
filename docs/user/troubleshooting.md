# Troubleshooting

## App Starts But Proxy Is Not Ready

- Check the status page for `warming=true`.
- Wait for Wintun cleanup to finish.
- Verify that `sing-box.exe` exists next to the app.
- Make sure ports `10807`, `9090`, and the local API port are free.

## TUN Fails To Start

- Run SafeSky as administrator.
- Restart Windows if a stale Wintun adapter is stuck.
- Check logs for `Cannot create a file when that file already exists`.
- Let SafeSky perform startup cleanup before clicking connect again.

## Websites Still See The Real IP

- Run diagnostics and leak tests.
- Check that the selected server is actually connected.
- Disable browser QUIC/HTTP3 if the site bypasses TCP proxy paths.
- Ensure routing rules do not send that domain or process direct.

## DNS Leak Warning

- Check DNS settings in routing.
- Keep remote DNS through the proxy.
- Avoid adding direct DNS rules unless you understand the impact.

## Subscription Import Fails

- Ensure the URL uses HTTPS.
- Check that the provider returns supported links.
- Try downloading the subscription in a browser to verify provider status.

## Update Fails

- Check network connectivity.
- Ensure the release metadata URL is reachable.
- Confirm the staged file SHA256 matches metadata.
- Start the app from a writable install directory.

## Logs To Collect

- `safesky.log`
- `config.singbox.json`
- latest `data/crash-*.json` if present
- screenshot of diagnostics

Mask server URLs and credentials before sharing logs.
