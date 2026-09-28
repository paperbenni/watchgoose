# Limits

- A sender failure or network outage can trigger the same repair and reboot
  as an unreachable VM. Continued silence can cause unattended reboot loops.
  [ADR 0001](/adr/0001-accept-unattended-reboot-loops) accepts this for a work
  machine.
- An SSH lockout does **not** trigger repair while the sender can still reach
  the HTTP endpoint. You can [stop the sender deliberately](/guide/operate#deliberately-trigger-repair-after-an-ssh-lockout)
  to make the deadline expire.
- The reassurance endpoint is unauthenticated. A reachable peer can suppress
  the switch by continuing to post; there is no independent alert for that.
- A forceful reboot can lose recent writes because it does not sync disks.
  [ADR 0002](/adr/0002-escalate-to-sysrq-b) explains the escalation.
- A kernel panic or completely stopped userspace cannot be repaired by this
  userspace daemon. The program has no notification path.

The [design decisions](/decisions) explain why these behaviors were accepted
for the original VM. Evaluate them before installing on another machine.
