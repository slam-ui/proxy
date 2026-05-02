# Kill Switch Policy

SafeSky uses fail-close kill switch behavior.

## Crash Startup

When firewall rules are left by a previous session and `data/killswitch_state.json` has:

```json
{
  "active": true,
  "expected_clean_shutdown": false
}
```

startup does not delete firewall rules. The UI shows a recovery row and the user must either reconnect or explicitly remove the block.

## Clean Shutdown

On explicit disable or clean shutdown, SafeSky removes its firewall rules and stores:

```json
{
  "active": false,
  "expected_clean_shutdown": true
}
```

Only this clean state allows startup cleanup.

## Manual Unblock

Settings exposes "Снять блокировку" for the crash recovery state. The UI asks for confirmation because this returns traffic to the unprotected network.
