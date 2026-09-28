# Accept unattended reboot loops as the failure mode

The switch re-arms on every boot rather than latching after firing once, and carries no cap
on how many unattended reboots it may perform. A machine that is unreachable and stays
unreachable will therefore reboot roughly every half hour indefinitely.

This is deliberate. The machine is a [work machine](../CONTEXT.md), not a service: nothing
outside it is affected by it being down, and a person who cannot get in cannot use the
machine at all. Availability of a reachable-but-broken box is worth less than a reachable box
that is being repeatedly reset. The [uptime floor](../CONTEXT.md) makes the loop harmless
rather than rapid, and the loop is in fact a legible signal — a person who notices the machine
has been restarting knows to go and look.

## Consequences

A sustained failure still resolves to a machine that needs a human, and the human now arrives
at a machine that has power-cycled perhaps a hundred times. Because the ceiling on that is
"however long until someone notices", the practical limit is social rather than technical: if
this box is ever left unattended for an extended period, the loop becomes a nuisance rather
than a safety net. Reconsider if the machine's role ever changes from personal work to
anything with outside consumers.
