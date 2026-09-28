# watchgoose

**It only acts when nobody is looking.**

A dead-man's switch for one shared Linux VM. The machine watches for a signal
from outside, and when that signal stops arriving it repairs its own access and
reboots. Not because anything reported a fault. Because nobody spoke to it.

---

## The idea, inverting the usual one

The reflexive question about a machine you cannot reach is "is it broken?". You
check disk space, check whether sshd is up, check the network, check the logs.
Every one of those checks needs a working machine to run them on, and a machine
that is unreachable is by definition not the machine telling you what is wrong.

A dead-man's switch flips that around. It never asks whether the machine is
broken. It asks one question:

> has anyone outside spoken to me recently?

The signal that answers it is called **reassurance**: a periodic arrival whose
only meaning is that whoever sent it can still reach the machine. It carries no
instruction, it asserts nothing about health, and it is not answered. The
arrival is the whole message.

So the failure mode is inverted. Instead of a monitor that alerts you when
something is wrong, there is a machine that acts when nothing has happened. The
absence of reassurance is the trigger and the only input.

- **Reassurance** — a periodic signal arriving from outside the machine.
- **Reassurance deadline** — how long the machine tolerates going without
  reassurance before concluding it is unreachable. Deliberately several times
  the reassurance interval, so one missed poke is never mistaken for silence.
- **Uptime floor** — the minimum age a freshly-booted machine must reach before
  the switch may act. It exists to make a boot loop harmless, not to make
  rebooting rare.
- **Repair** — restoring the ability to log in, before any reboot is attempted.
  Repair is what a human would have done by hand. The reboot is a separate,
  later act.
- **Re-arm** — the switch becoming eligible to act again after it has already
  acted. Here that means every boot, deliberately.

The full vocabulary lives in [CONTEXT.md](CONTEXT.md), and it is worth reading
before reading the code. The words are load-bearing: a "heartbeat" would suggest
the machine is alive, which is not what the signal means, and a "watchdog"
would suggest it watches for faults, which it does not do at all.

## The two halves

| Binary | Runs on | Job |
| --- | --- | --- |
| `watchgoose` | the machine itself | Accepts reassurance, watches the reassurance deadline, repairs access and reboots. |
| `goosepoke` | something more reliable than the machine | Sends reassurance on a timer. |

`goosepoke` must not run on the machine it is reassuring. A reassurance that dies
with its own machine is indistinguishable, from that machine's point of view,
from nobody sending anything, and silence is the trigger.

`goosepoke` sends the barest possible request: a POST with no body, no credential, no
token, no timestamp and no signature. The whole payload of a poke is the fact of
its arrival. See [ADR 0006](docs/adr/0006-unauthenticated-heartbeat.md) for why
there is no authentication at all.

Because that is the design, the most important property of `goosepoke` is that **its
own failures are loud**. A client that has silently stopped poking looks, from
the machine, exactly like a machine nobody can reach. So `goosepoke` logs every
failure with enough detail to diagnose it, retries transient network errors
forever rather than exiting, bounds each request with a short timeout so a hung
connection cannot wedge the loop, and sends one poke immediately on start rather
than waiting out the first interval.

## Why

On the machine this was written for, a boot failed to mount `/boot`. That blocked
`local-fs.target`, so the boot stalled into a degraded state. `/vol/storage1` and
`/home` are `nofail` in fstab, so systemd only *wants* them and nothing
*required* them, so they were never mounted. The root disk's `/home` is an empty
directory. And `sshd` only had `RequiresMountsFor=/run/sshd`, so it started
anyway and answered every connection with a key rejection, for every user.

The result was 0 successful logins and 91 failed attempts across a 33-hour
window, and nothing could be done about it from inside: there was nowhere to log
in from, which is the only place you could fix a login problem.

The general lesson is the reason this project exists. A single unrelated mount
failure removed every user's access, and the only symptom anyone could observe
was "public key rejected" — a message that says nothing about the real cause and
points at the wrong layer entirely. Every check that would have explained it
needed a login, and the login was the thing that was broken.

watchgoose's answer is the inverted one. If the machine can no longer hear from
outside, it assumes the problem is that nobody can get in, fixes what it can
locally, and reboots. The volume coming back is what actually fixes it.

## Installing

Everything is driven by a `justfile`. Install `just` and Go 1.27.1 or later, then:

```sh
just build          # static binaries into dist/ (amd64 and arm64 for goosepoke)
just check          # gofmt, go vet, go test
just test-vm        # install and start the daemon on THIS machine
just check-config   # validate the installed config with the daemon itself
just uninstall-vm   # remove it again
```

`just build` cross-compiles with `CGO_ENABLED=0`. That is a requirement, not a
preference: the target must run the binary with nothing installed and no
matching glibc, and a dynamically linked binary there is a binary that silently
does not run.

The client goes on a separate host, over SSH, and it touches nothing unless
both the host and the URL are given — guessing a target is how you end up
reassuring the wrong box:

```sh
just test-client HOST=user@raspberrypi url=http://100.76.187.120:9099/reassure
just test-client HOST=user@raspberrypi url=http://100.76.187.120:9099/reassure arch=arm64
```

That installs `/usr/local/bin/goosepoke`, renders a `goosepoke.service` unit with the URL
and interval baked in, and enables it. To poke once by hand, which is the first
thing worth doing after any install:

```sh
URL=http://100.76.187.120:9099/reassure
ssh user@raspberrypi "sudo -u goosepoke /usr/local/bin/goosepoke -url $URL -once"
```

The exit status is meaningful: 0 when the poke registered, non-zero when it did
not, so cron or a monitoring check can act on it.

### goosepoke flags

```
-url       full URL to POST to, e.g. http://100.76.187.120:9099/reassure
           (required unless -config is given)
-interval  how often to poke                            (default 5m)
-timeout   per-request timeout                          (default 15s)
-once      send a single poke and exit; non-zero on failure
-insecure  skip TLS verification; only for a self-signed endpoint
-config    optional YAML file whose keys match the flags
```

`-config` takes a small YAML file with the same names as the flags — `url`,
`interval`, `timeout`, `insecure` — and nothing else. This is a different schema
from the daemon's config on purpose: the daemon's file is full of things that
mean nothing to a client, like `authorized_keys`.

```yaml
# ~/.config/goosepoke.yaml — optional, root- or user-owned
url: http://100.76.187.120:9099/reassure
interval: 5m
timeout: 15s
insecure: false
```

Explicit flags always win over the file. The file supplies defaults; it does not
override the command line. Anything the file does not mention falls back to the
built-in default, so a partial file only overrides what it names.

`goosepoke` exits non-zero only for bad configuration. A network error is transient by
definition and the loop keeps going.

## Configuring the daemon

The daemon's configuration is a single YAML file installed to
`/etc/watchgoose.yaml`, owned by root. Every duration is a Go duration string
(`"20m"`, `"90s"`), and anything omitted falls back to the built-in default, so
the file only needs to state what differs. A fully commented example lives at
[`deploy/watchgoose.example.yaml`](deploy/watchgoose.example.yaml); the shape is:

```yaml
server:
  listen: "100.76.187.120:9099"   # a tailnet address, not 0.0.0.0
  poll_interval: 1m
  state_file: /srv/watchgoose/var/last-reassurance
reassurance:
  path: /reassure                  # the route goosepoke POSTs to
  deadline: 20m                    # the reassurance deadline
guard:
  min_uptime: 30m                  # the uptime floor
volume:
  mountpoint: /home
repair:
  settle: 1m
  accounts: [benjamin, ubuntu]     # repaired only when the volume is mounted
  authorized_keys: ["ssh-ed25519 AAAA... benjamin@host"]
  recovery_user: recovery
  recovery_home: /srv/recovery     # must be outside volume.mountpoint
  recovery_nopasswd_sudo: true
reboot:
  graceful_timeout: 5m
log:
  log_file: /srv/watchgoose/var/watchgoose.log
```

Three things in that file are worth reading twice:

- `reassurance.deadline` must comfortably exceed `goosepoke`'s `-interval`, or the
  switch will fire between pokes. 20 minutes against a 5 minute interval is
  three pokes of slack.
- `repair.recovery_home` must be outside `volume.mountpoint`. The daemon
  refuses to start if it is not, because a recovery account living on the volume
  is a recovery account that vanishes exactly when it is needed.
- `server.listen` is the only thing limiting who can reach the daemon, because
  the endpoint carries no authentication.

The daemon's own flags are `-config <path>`, which defaults to
`/etc/watchgoose.yaml`, and `-check`, which validates the configuration, prints
the resolved result to stdout and exits, touching neither the network nor the
state file nor the log. `just check-config` runs the installed binary with
`-check`, so a config the daemon would reject cannot pass there.

The daemon also re-executes itself with private `-escalate-child*` flags for the
forceful reboot rung. Those are internal plumbing for the escalation child, not
flags for humans.

## What happens when the deadline passes

1. The reassurance deadline passes while the machine is past its uptime floor.
2. **Repair.** The recovery user is always repaired. The ordinary accounts are
   repaired only when the volume is actually mounted, because when it is not
   `/home` is an empty directory on the root disk and anything written there is
   shadowed the moment the volume mounts on a later boot. A repair log that says
   "done" while changing nothing that will ever be read is worse than an honest
   skip. Accounts found locked are unlocked when the volume is present.

   The repair log distinguishes "volume absent, only recovery user repaired"
   from "volume present, all accounts repaired", because the two have different
   remedies: a missing volume is fixed by the reboot, not by rewriting keys.
3. After a settle period, a child process is spawned in its own session, so
   that it survives the teardown that is about to happen. It sleeps out
   `reboot.graceful_timeout` as a backstop for the rung that comes next.
4. The first rung: a graceful `systemctl reboot`. It is launched and not waited
   for, because a wedged PID 1 may be exactly why the switch is firing.
5. If nothing has taken the machine down within the graceful timeout, the child
   writes the reboot byte to `/proc/sysrq-trigger`. It re-reads the reassurance
   deadline first, so a reassurance arriving during the graceful rung stops the
   ladder at the last rung too.
6. The machine comes back, the uptime floor resets, and the switch is armed
   again.

Every irreversible step above is preceded by a fresh re-read of the
reassurance deadline, so a poke arriving partway through stands the whole
sequence down.

## Operational notes

### Poke to stop the loop

Sending reassurance during the countdown abandons the reboot.

This is the intended way to stop a loop, and it is better than racing the timer.
The escalation child re-checks the reassurance deadline before taking the
forceful rung specifically so that a poke stops the ladder at the last step too,
not only between steps. So: to abandon a reboot in progress, poke the machine.

To see what the switch currently believes, `GET /health` on the listen address
returns the last reassurance time, whether it is stale, and whether the uptime
floor is holding. That endpoint describes the switch, not the machine — it is
for a human with `curl`, and it is not a health check in the usual sense.

### Reboot loops are accepted, by decision

If the failure is sustained, the machine will reboot roughly every half hour,
indefinitely, unattended. That is
[ADR 0001](docs/adr/0001-accept-unattended-reboot-loops.md), and it is
deliberate, not an oversight.

The reasoning: this is a **work machine**, not a service. Nothing outside it is
affected by it being down. A person who cannot get in cannot use the machine at
all, so a reachable-but-broken box is worth less than a reachable box that is
being repeatedly reset. The uptime floor is what makes the loop harmless rather
than rapid, and the loop is itself legible: a machine that keeps restarting is
telling you something is wrong, and it will keep telling you.

The honest cost: a sustained failure resolves to a machine that needs a human,
and that human now arrives at a box that has power-cycled perhaps a hundred
times. Because the only ceiling is "however long until someone notices", the
practical limit here is social rather than technical. If this machine is ever
left unattended for an extended stretch, the loop becomes a nuisance rather than
a safety net. Reconsider if the machine's role ever changes from personal work
to anything with outside consumers.

### The recovery user

`recovery` is a standing account whose credentials live on the root disk
(`/srv/recovery` in the example), not on the volume. It is an escape hatch, not
an automation identity: it exists so that a human has somewhere to get in from
when the ordinary accounts are unreachable.

Its home being on the root disk is on purpose, and buys two separate things.
The root disk is the only storage expected to be present when the volume is not,
so a recovery account on the volume is a recovery account that disappears in
precisely the situation it exists for. And `/srv/recovery` is outside `/home/*`,
which is the only thing `simplevm`'s `deactivate_homes.sh` keys on, so the
known lockout bug in [ADR 0003](docs/adr/0003-leave-simplevm-timers-enabled.md)
never locks this account out.

It is given passwordless sudo, because a recovery account that cannot fix
anything is not a recovery account. Nothing automated ever logs in as it.

## Deliberate limitations

These are not oversights. They are the price of the design, accepted knowingly.

**The heartbeat is unauthenticated and a suppressed switch is silent.** Anything
on the tailnet can post to the endpoint, so any of them — deliberately, or by a
stuck script — can keep the switch permanently disarmed, and a disarmed switch
produces no signal at all. This is accepted on purpose
([ADR 0006](docs/adr/0006-unauthenticated-heartbeat.md)): the daemon binds to
the tailnet, the tailnet is shared, and a threat model that warranted a secret
would buy nothing when the endpoint is unauthenticated anyway. If this machine
is ever exposed beyond the tailnet, or the tailnet stops being trusted, that
decision has to be revisited. Until then, a disarmed switch is indistinguishable
from a healthy one, and the failure is invisible rather than loud.

**It measures reachability, never health.** The switch will never diagnose
anything, never inspect a service, never form an opinion about whether the
machine is well
([ADR 0008](docs/adr/0008-reachability-not-health.md)). That is the point: a
monitor that inspects and judges develops false confidence, reporting "healthy"
on a subtly broken machine, and the one time its judgement was wrong would be
the one that mattered. The consequence to be explicit about: a machine
unreachable for a *network* reason gets repaired and rebooted exactly like one
broken for a fault reason, and by
[ADR 0001](docs/adr/0001-accept-unattended-reboot-loops.md) repeatedly.

**There is no notification path.** No email, no webhook, no chat message. When
the switch fires, you find out by noticing the machine restarting, or by reading
`goosepoke`'s log and seeing that its own failures stopped it reaching the machine.
Nothing here will tell you that a switch fired, because adding a notification
path means adding a dependency whose failure is a new way to be unreachable.

**A kernel panic hangs instead of rebooting.** This kernel has `panic=0`, so a
panic leaves the machine wedged rather than cycling. Nothing in userspace can
help, the dead-man's switch included, because that is exactly the state a
userspace process cannot be running in. A machine that panics needs a hypervisor
to cycle it, and there is no control panel to power one back on.

**The escalation rung arms a bit that could also power off.** The forceful rung
enables the kernel sysrq magic-control bit, which is what makes `b` work at all
— and which also makes `o` (poweroff), `i` (kill all processes) and `c`
(deliberate crash) available. It is armed at the moment of escalation and for
seconds, not enabled in a persistent sysctl, so it is absent during all normal
operation. The code path can only ever write the reboot byte: it is a hardcoded
literal, and the decision to escalate is never taken by anything that accepts
external input. Root could already `systemctl poweroff` this machine anyway, and
it would not come back on its own, so the marginal risk is confined to this
project writing the wrong byte
([ADR 0002](docs/adr/0002-escalate-to-sysrq-b.md)).

Also worth knowing: the simplevm metadata timers carry a known, currently-latent
bug that can lock every ordinary user out of this machine
([ADR 0003](docs/adr/0003-leave-simplevm-timers-enabled.md)). The switch does
not prevent it and will re-trigger it on every boot. The mitigation is
structural — the recovery user's home is outside the path the buggy script keys
on, and repair unlocks the ordinary accounts when it finds them locked — so
self-healing survives the bug rather than removing it.

## Decisions

Each of these is binding on the code and worth reading before changing it.

| ADR | Summary |
| --- | --- |
| [0001](docs/adr/0001-accept-unattended-reboot-loops.md) | Accept unattended reboot loops as the failure mode; the switch re-arms on every boot with no cap. |
| [0002](docs/adr/0002-escalate-to-sysrq-b.md) | Escalate to `sysrq-b` by arming the magic-control bit at fire time, never persistently. |
| [0003](docs/adr/0003-leave-simplevm-timers-enabled.md) | Leave the simplevm metadata timers enabled despite their known lockout bug. |
| [0004](docs/adr/0004-repair-skips-accounts-when-volume-absent.md) | Repair skips the ordinary accounts when the volume is absent, rather than writing to a shadowed path. |
| [0005](docs/adr/0005-single-daemon.md) | One daemon owns listening, deciding and escalating; no timer, no cron backstop. |
| [0006](docs/adr/0006-unauthenticated-heartbeat.md) | The heartbeat is unauthenticated and carries no payload; a disarmed switch is silent. |
| [0007](docs/adr/0007-constrain-earlyoom.md) | Constrain earlyoom away from `sshd` and the daemon, removing a distinct way to become unreachable. |
| [0008](docs/adr/0008-reachability-not-health.md) | Reachability, not health, is what the switch measures; it never diagnoses. |

## Layout

```
.
├── cmd/
│   ├── goosepoke/          the client: sends reassurance on a timer
│   └── watchgoose/         the daemon: accepts it, decides, repairs, reboots
├── internal/
│   ├── config/             the daemon's YAML schema and its validation
│   ├── mountinfo/          is this path a mountpoint? (volume detection)
│   ├── reboot/             the escalation ladder, including the sysrq rung
│   ├── repair/             recovery user, keys, account unlock
│   └── state/              the persisted reassurance timestamp
├── deploy/                 install scripts, systemd units, example config
├── docs/adr/               the binding decisions
├── justfile                every path and every deploy action
├── CONTEXT.md              the vocabulary this project is written in
└── README.md
```

## Contributing

Read [CONTEXT.md](CONTEXT.md) first, then the ADRs. Most of the surprising-looking
parts of this codebase are surprising on purpose, and the comments say so at the
point where it matters. If a change contradicts an ADR, the ADR is what needs
updating, in the same commit, with the reasoning written down.

```sh
just check   # gofmt -l must print nothing; go vet ./...; go test ./...
```

---

**It only acts when nobody is looking.**
