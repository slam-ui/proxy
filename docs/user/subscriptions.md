# Subscriptions

Subscriptions let SafeSky fetch a remote list of server links and keep imported
servers updated.

## Add A Subscription

1. Open the subscriptions view.
2. Paste an HTTPS subscription URL.
3. Choose a refresh interval.
4. Save.

SafeSky downloads the subscription immediately, parses supported server links,
and adds them to the server list. Subscription metadata is encrypted before it is
written under `data/subscriptions/`.

## Refresh Behavior

- Manual refresh is available from the UI.
- Automatic refresh uses the subscription interval.
- Backoff is applied after failures.
- Background refresh is paused on battery power and resumes on the next launch
  while connected to AC.

## Security Rules

- Subscription URLs must use HTTPS.
- HTTP redirects are rejected.
- Redirects to unsupported URLs are rejected.
- Plain subscription URLs are not written to diagnostics or telemetry.

## Troubleshooting

| Symptom | What to check |
| --- | --- |
| Subscription imports no servers | The provider may return unsupported protocols or an invalid base64 body. |
| Refresh fails with HTTP error | Check the provider status and URL. |
| Servers import but do not connect | Test one server directly and run diagnostics. |
| URL changed | Remove the old subscription and add the new URL. |
