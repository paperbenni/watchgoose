# Escalate to sysrq-b by arming the magic bit at fire time

The forceful rung of the reboot ladder enables the kernel sysrq "magic control" bit in
`/proc/sys/kernel/sysrq` and then writes `b` to `/proc/sysrq-trigger`, which is the only way
to reboot the kernel without going through systemd. The bit is not enabled in a persistent
sysctl; it is written at the moment of escalation and is therefore absent during all normal
operation.

## Considered Options

The alternative was a small helper calling `reboot(2)` directly, which never needs the bit and
so cannot power off at all. It was rejected because enabling the bit grants no capability
that root does not already have — `systemctl poweroff` is always available to root — so the
marginal risk is confined to our own code writing the wrong byte. The bit was made
fire-time-only anyway, so the dangerous functions (`o` poweroff, `i` kill-all-processes,
`c` deliberate crash) are unavailable except for a few seconds, and the byte written is a
hardcoded literal rather than a variable.

## Consequences

Root can already shut this machine down, and it will never come back up on its own, because
there is no hypervisor control panel available to power it on. The switch is therefore built
so that no code path it owns can reach a power-off: the escalation writes exactly one byte,
and the decision to escalate is never taken by anything that accepts external input.

`panic=0` on this kernel means a panic hangs rather than reboots, so the reboot-on-oops sysrq
bit is the only thing standing between a kernel fault and a machine that never returns.
