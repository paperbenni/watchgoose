# Reachability, not health, is what the switch measures

The switch answers exactly one question: has anyone outside the machine spoken to it
recently? It never inspects whether the machine is healthy, and it never runs a diagnostic to
decide whether to act. Absence of [reassurance](../CONTEXT.md) is both the trigger and the
only input.

This keeps the switch from developing opinions. A monitor that inspects and judges can
develop false confidence — it reports "healthy" on a machine that is subtly broken, and the
one time its judgement was wrong would be the one that mattered. Refusing to judge means the
switch's behaviour is fully determined by a single timestamp and cannot be wrong in the
interior.

It also means the switch inherits the limitations of reachability. A machine that is
unreachable from the tailnet for a network reason, rather than a fault reason, will be
repaired and rebooted just the same — and by
[ADR 0001](./0001-accept-unattended-reboot-loops.md), repeatedly.
