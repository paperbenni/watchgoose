# Leave the simplevm metadata timers enabled despite their known lockout bug

`homes-sync.timer` runs `deactivate_homes.sh` every two minutes. If the metadata server
returns an empty `home_users` array, that script runs `usermod -L` on every account whose
home is under `/home/*` and deletes their `metadata_authorized_keys`. This is a known,
currently-latent way to lock every ordinary user out of this machine — the server is presently
answering `{"detail": "Key [...] not found"}` and the script takes an early-exit branch instead.

The timers are being left enabled. The switch does not touch simplevm, and the dead loop in
`simplevm-metadata-synchronizer` (a `systemctl daemon-reload` every 30 seconds, ~4,875
restarts in one day) is left running too.

## Consequences

The switch does not prevent this bug and will re-trigger it on every boot. Mitigation is
instead structural: the [recovery user](../CONTEXT.md) has a home outside `/home/*`, which is
the only thing that script keys on, so it is never locked, and the repair step unlocks the
ordinary accounts when it finds them locked. Self-healing therefore survives the bug rather
than removing it.

If the metadata service is ever repaired and starts working, `create_user_home.sh` will strip
`sudo`/`wheel`/`admin` from any account it provisions — which would include the recovery user
if it were ever named in `home_users`. That has not been observed to happen, because the
service has never successfully returned a `home_users` payload.
