# earlyoom is constrained away from sshd and the switch

`earlyoom` currently runs with `-r 3600` and no `--avoid` list, so its process selection is
unconstrained. An `--avoid` list covering `init`, `sshd`, `earlyoom` itself, and the
dead-man's switch daemon is added instead.

This is not part of the switch. It removes a distinct way the machine becomes unreachable: if
earlyoom selects `sshd`, the only way in is gone, and rebooting simply puts the machine back
in the same position. The switch can respond to that but cannot prevent it.

The file already carried a commented-out `--avoid` example, so the mechanism was considered
at some point and not enabled.
