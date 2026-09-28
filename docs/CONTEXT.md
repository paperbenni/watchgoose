# watchgoose

A dead-man's switch. Two programs: `watchgoose`, which runs on a machine and acts when it
stops hearing from outside, and `goosepoke`, which runs on something more reliable and keeps
speaking to it. The machine repairs its own access and reboots; nobody has to notice.

## Language

**Work machine**:
A machine whose purpose is one person's access to their own work, not the availability of a
service to third parties. Losing it costs that person time; keeping it up when it is broken
costs nothing that anyone outside can observe.
_Avoid_: production host, server, service

**Dead-man's switch**:
A mechanism that acts on the *absence* of a signal rather than the presence of a fault. It
never asks "is the machine broken?" — only "is anyone still out there?"
_Avoid_: watchdog, health check, liveness probe

**Reassurance**:
A periodic signal arriving from outside the machine, whose only meaning is that whoever sends
it can still reach the machine. It carries no instruction and asserts no health.
_Avoid_: heartbeat (connotes liveness of the machine), ping, keepalive

**Reassurance deadline**:
How long the machine will tolerate going without reassurance before concluding it is
unreachable. Deliberately several times the reassurance interval, so that a single missed
poke is never mistaken for silence.
_Avoid_: timeout, grace period

**Uptime floor**:
The minimum age a freshly-booted machine must reach before the switch is allowed to act on it.
Its purpose is to make a boot loop harmless rather than to make rebooting rare.
_Avoid_: boot delay, cooldown, rate limit

**Re-arm**:
The switch becoming eligible to act again after it has already acted. A switch that
re-arms on every boot will act repeatedly for as long as the silence persists.
_Avoid_: reset, retry budget

**Repair**:
Restoring the ability to log in, before any reboot is attempted. Repair is what a human
would have done by hand; the reboot is a separate, later act.
_Avoid_: remediation, self-heal, fix

**Recovery user**:
A standing account whose credentials live on the root disk rather than on the data volume,
existing so that a human has somewhere to get in from when the ordinary accounts are
unreachable. It is an escape hatch, not an automation identity.
_Avoid_: break-glass account, rescue account, backdoor

**Volume**:
The separate data disk holding the ordinary user home directories. Distinct from the root
disk, and expected to be sometimes absent at boot.
_Avoid_: /home, secondary disk, data partition

**Root disk**:
The disk holding the operating system, and therefore the only storage expected to be present
when the volume is not. Anything that must work during a volume failure belongs here.
_Avoid_: system disk, local disk, boot disk
