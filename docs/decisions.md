# Design decisions

These records explain the assumptions and tradeoffs behind watchgoose.

| ADR | Decision |
| --- | --- |
| [0001](/adr/0001-accept-unattended-reboot-loops) | Accept unattended reboot loops. |
| [0002](/adr/0002-escalate-to-sysrq-b) | Escalate to a forceful kernel reboot if graceful reboot stalls. |
| [0003](/adr/0003-leave-simplevm-timers-enabled) | Leave the original VM's simplevm timers enabled. |
| [0004](/adr/0004-repair-skips-accounts-when-volume-absent) | Skip ordinary accounts when their volume is absent. |
| [0005](/adr/0005-single-daemon) | One daemon listens, decides and escalates. |
| [0006](/adr/0006-unauthenticated-heartbeat) | Accept unauthenticated reassurance on a trusted network. |
| [0007](/adr/0007-constrain-earlyoom) | Constrain earlyoom separately from the switch. |
| [0008](/adr/0008-reachability-not-health) | Measure reachability rather than machine health. |
