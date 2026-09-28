# Operate

## Check status

- `curl http://VM_TAILNET_IP:9099/health` reports the last reassurance,
  whether it is stale, and whether the uptime floor is holding. It describes
  the switch, not the VM's health.
- The daemon's audit log is `/srv/watchgoose/var/watchgoose.log`. Startup and
  service errors are also visible with `journalctl -u watchgoose`.
- On the poker, `journalctl -u 'watchgoose-poke@*.service' -f` shows successful
  and failed pokes. The instance numbers and URLs are printed by `just setup`.

## Deliberately trigger repair after an SSH lockout

If SSH is locked out but the HTTP endpoint is still reachable, the automatic
poker keeps reassuring the VM and repair will not start. On the **separate
poker machine**, stop and disable the relevant instance:

Use the instance number assigned to that VM by `just setup` (shown in its
summary and `/etc/watchgoose/poke/1.yaml`). For example:

```sh
sudo systemctl disable --now watchgoose-poke@1.service
```

With no other sender poking, the VM acts after the remaining reassurance
deadline and uptime floor, then repairs and reboots. This is not immediate.
Do not send manual pokes while waiting for repair.

Once access is restored, re-enable the sender so the VM does not repeat the
reboot cycle:

```sh
sudo systemctl enable --now watchgoose-poke@1.service
```

Before the reboot ladder starts, a successful poke prevents it from starting.
Once reboot has been requested, wait for the VM to return, then check it and
resume poking. A poke during reboot is not a cancellation command.

## Repair and removal

When repair runs, the daemon creates or repairs the recovery account on the
root disk with the configured SSH keys and optional passwordless sudo. It
repairs ordinary accounts only if their configured volume is mounted;
otherwise writing their keys could put them on the wrong disk. The recovery
account is **not** created at install time.

`sudo just uninstall-vm` stops and removes the daemon. By default it keeps
`/etc/watchgoose.yaml`, the state and the recovery account. `PURGE=1` also
removes config and state; the
[uninstaller](https://github.com/paperbenni/watchgoose/blob/main/deploy/uninstall-vm.sh)
documents the separate recovery-account removal option.
