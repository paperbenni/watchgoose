# watchgoose

watchgoose is a dead-man's switch for a Linux work VM. A second machine sends
periodic HTTP requests to the VM. If they stop arriving, the VM repairs SSH
access and reboots. It measures whether the sender can reach the VM; it does
not diagnose the VM's health. See [the incident that prompted it](docs/origin.md)
and [the design decisions](docs/adr/).

## How the signal works

| Program | Where it runs | What it does |
| --- | --- | --- |
| `watchgoose` | The VM | Listens for reassurance, records the time of each request, and checks the deadline. |
| `goosepoke` | A separate, more reliable machine | Sends an empty HTTP `POST` immediately on start and then at a set interval. |

The default examples use `POST /reassure` every **5 minutes**. A successful
request gets HTTP **204 No Content**. The VM records its own receipt time; the
sender's clock and request body do not matter. If no request arrives for **20
minutes**, the daemon notices on its next poll (every **1 minute**). Once the VM
has been up for at least **30 minutes**, it repairs access, waits **1 minute**,
and starts a reboot. If a graceful reboot stalls for **5 minutes**, a child
process triggers a forceful kernel reboot. These times are configurable in
[`watchgoose.example.yaml`](deploy/watchgoose.example.yaml).

On a fresh install, repair and reboot wait for the **first successful poke**.
After that, the recorded time survives service restarts and VM boots; the
normal deadline applies without another setup step. A lost or corrupt state
file after installation is treated as missed reassurance.

`goosepoke` must run on another machine. It logs failed requests and keeps
retrying; a failed client or network path otherwise looks like an unreachable
VM to the daemon. A poke is reassurance that a request arrived, not proof that
SSH or any other service works.

## Network and ports

| Host | Required connection | Default example |
| --- | --- | --- |
| VM | Inbound TCP from the `goosepoke` host to `server.listen` | Port `9099` on the VM's tailnet IP |
| `goosepoke` host | Outbound TCP to the VM's address and port | `VM_TAILNET_IP:9099` |

If a host firewall or tailnet ACL blocks that path, allow the client to reach
the VM on the configured TCP port. No public internet port is required, and
`goosepoke` needs no inbound port, and the program uses no UDP ports. The HTTP
response uses the same connection.
The `/health` endpoint uses the **same VM port**. SSH is needed to deploy the
client and to log into the VM, but it is separate from this signal.

Bind `server.listen` to the VM's tailnet IP, such as `100.x.y.z:9099`; do not
use `0.0.0.0` unless you intend to expose the endpoint on every interface.
The daemon serves plain HTTP, with no built-in TLS or authentication. Anyone
who can reach the endpoint can keep the switch disarmed, so restrict access
with your tailnet ACL or firewall if needed. The listen address selects an
interface; it does not authenticate clients. See [ADR 0006](docs/adr/0006-unauthenticated-heartbeat.md).

## Install

Requirements: Linux with systemd, Go 1.27.1 or later, `just`, and a separate
machine that can reach the VM over the network. Replace `VM_TAILNET_IP` and
`user@client-host` below with your actual values.

1. Configure the VM. Copy the [example config](deploy/watchgoose.example.yaml)
   to `/etc/watchgoose.yaml`. Set `server.listen` to the VM's reachable tailnet
   address and put your real public key or keys in `repair.authorized_keys`.
   Set `repair.accounts` and `volume.mountpoint` if ordinary home directories
   need repair. The example deliberately has no usable address or key, so it
   cannot start unchanged.

2. Build and install on the VM:

   ```sh
   just build
   sudo deploy/install-vm.sh
   sudo /srv/watchgoose/watchgoose -config /etc/watchgoose.yaml -check
   ```

   The installer preserves an existing config and state file. On a new install,
   it creates a waiting marker; it does not create the recovery account.

3. From the **separate machine**, verify the network path and activate the
   switch with one request:

   ```sh
   curl -fsS -X POST -m 10 http://VM_TAILNET_IP:9099/reassure
   ```

   A successful request has an empty response and exit status 0. The client
   must keep poking before the 20-minute deadline passes.

4. Install the continuous client from this repository, over SSH to that
   separate machine:

   ```sh
   just test-client user@client-host http://VM_TAILNET_IP:9099/reassure
   # Add "5m 15s arm64" for an arm64 client host.
   ```

   This installs `/usr/local/bin/goosepoke` and enables `goosepoke.service`.
   The URL must include the `/reassure` path. Check the sender's journal with
   `ssh user@client-host 'journalctl -u goosepoke -f'`. For a one-off client
   check, run `goosepoke -url http://VM_TAILNET_IP:9099/reassure -once` on that
   host; it exits nonzero if the request fails. `goosepoke -help` lists its
   interval, timeout, TLS and optional YAML config flags.

## Operate

- `curl http://VM_TAILNET_IP:9099/health` reports the last reassurance, whether
  it is stale, and whether the uptime floor is holding. It describes the switch,
  not the VM's health.
- The daemon's audit log is `/srv/watchgoose/var/watchgoose.log`. Startup and
  service errors are also visible with `journalctl -u watchgoose`.
- If SSH is locked out but the HTTP endpoint is still reachable, the automatic
  client keeps reassuring the VM and repair will not start. To deliberately
  trigger repair, **on the separate sender machine** run
  `sudo systemctl disable --now goosepoke`. With no other sender poking, the VM
  acts after the remaining reassurance deadline and uptime floor, then repairs
  and reboots. This is not immediate. Once access is restored, run
  `sudo systemctl enable --now goosepoke` on the sender so the VM does not
  repeat the reboot cycle. Do not send manual pokes while waiting for repair.
- Before the reboot ladder starts, a successful poke prevents it from starting.
  Once reboot has been requested, wait for the VM to return, then check it and
  resume poking. A poke during reboot is not a cancellation command.
- `sudo just uninstall-vm` stops and removes the daemon. By default it keeps
  `/etc/watchgoose.yaml`, the state and the recovery account. `PURGE=1` also
  removes config and state; the [uninstaller](deploy/uninstall-vm.sh) documents
  the separate recovery-account removal option.

When repair runs, the daemon creates or repairs the recovery account on the
root disk with the configured SSH keys and optional passwordless sudo. It
repairs ordinary accounts only if their configured volume is mounted; otherwise
writing their keys could put them on the wrong disk. The recovery account is
**not** created at install time.

## Limits to understand before enabling it

- A sender failure or network outage can trigger the same repair and reboot as
  an unreachable VM. Continued silence can cause unattended reboot loops.
  [ADR 0001](docs/adr/0001-accept-unattended-reboot-loops.md) accepts this for
  this work machine.
- The reassurance endpoint is unauthenticated. A reachable peer can suppress
  the switch by continuing to post; there is no independent alert for that.
- The forceful reboot can lose recent writes because it does not sync disks.
  [ADR 0002](docs/adr/0002-escalate-to-sysrq-b.md) explains the escalation.
- A kernel panic or a completely stopped userspace cannot be repaired by this
  userspace daemon. The program has no notification path.

For the project's terminology, see [CONTEXT.md](CONTEXT.md). Changes that alter
these behaviors should update the relevant [ADR](docs/adr/) and run `just check`.
