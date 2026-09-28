# The heartbeat is unauthenticated and carries no payload

The reassurance endpoint accepts a bare request from anything that can reach the tailnet and
records the local time of arrival. There is no shared secret, no signature, and nothing in the
body of the request is read.

The threat model does not support more. The daemon binds to the tailnet only, and the tailnet
is shared with other members. An unauthenticated endpoint means any of them — deliberately or
by a stuck script — can keep the switch permanently disarmed, and a disarmed switch is
silent. That is a real weakness and it is being accepted knowingly.

## Consequences

Because there is no signed payload, the original concern about an attacker replaying a
captured heartbeat to hold the window open forever does not apply, and the deadline can be
measured from local receipt time. This removes the need for clock agreement between client and
server, removes signature verification from the daemon, and makes the channel resilient to
wall-clock jumps on the VM, which a client-supplied timestamp would not have been.

If this machine is ever exposed beyond the tailnet, or the tailnet stops being trusted, this
decision must be revisited — a disarmed switch is indistinguishable from a healthy one, so
the failure is invisible rather than loud.
