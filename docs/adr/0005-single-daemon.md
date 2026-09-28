# A single daemon owns listening, deciding, and escalating

One long-running process does all three jobs: it accepts [reassurance](../CONTEXT.md), it
periodically checks whether the [reassurance deadline](../CONTEXT.md) has passed, and it
carries out repair and reboot. There is no separate decider invoked by a timer, and no cron
backstop.

This was originally split into a network-facing listener and a separately-scheduled decider,
on the grounds that a process accepting external input should not also be able to reboot the
machine. That reasoning assumes a threat model this machine does not have: the daemon is
reachable only from the tailnet, and carries no authentication at all, so anyone who can post
to it can already suppress the signal entirely. Given that, hardening the boundary between
the two halves buys nothing.

The collapse is a genuine simplification. A single process needs no timer, no cron fallback,
no lockfile to serialise two schedulers, and no shared state to lose across a respawn. It
starts once and thereafter is simply a running process, which is the property that makes it
survive systemd misbehaving in the first place.

## Consequences

Nothing schedules the switch; it self-polls. The only systemd involvement is starting the
daemon, so a wedged PID 1 can no longer prevent the switch from acting — which is the whole
reason for running a daemon rather than a timer.

Reassurance is recorded to disk rather than memory, so a crash and respawn cannot be mistaken
for silence. The [uptime floor](../CONTEXT.md) is read from `/proc/uptime` and is not
persisted at all, which is why [re-arming on every boot](../CONTEXT.md#language) needs no
state of its own.
