# Escalate to sysrq-b when a graceful reboot stalls

The forceful rung writes the hardcoded byte `b` to `/proc/sysrq-trigger` after
the graceful timeout. This uses the kernel directly when systemd cannot finish
a reboot. It does not write `/proc/sys/kernel/sysrq`: that mask controls keyboard
invocation only, while privileged writes to `/proc/sysrq-trigger` are allowed
regardless of the mask. The previous implementation wrote mask `24` based on an
incorrect reading of the kernel documentation.

The byte is fixed in code. No route, configuration value or client request can
choose another SysRq operation. In particular, this code cannot request `o`
(poweroff), which would leave this VM down without a way to power it back on.

SysRq-b immediately reboots without syncing or unmounting filesystems. It is
reserved for a graceful reboot that has failed to complete. This last rung can
lose recent writes; it is still preferable to a machine that remains
unreachable indefinitely.

The escalation child must survive the daemon's systemd unit stopping during
reboot. Its own session does not escape the unit cgroup. The unit therefore uses
`KillMode=process`: systemd stops the main daemon, while the child ignores
SIGTERM and remains alive until it acts, sees fresh reassurance, or reaches its
bounded lifetime. `ExecStopPost` cancels waiting children after an explicit
service stop, while preserving them when systemd is shutting down or the
daemon failed unexpectedly. Uninstall also
cancels children before and after stopping the service.

The daemon also waits only through the child's maximum lifetime. If both rungs
fail and the machine remains up, polling resumes and the switch retries.

Kernel reference: https://docs.kernel.org/admin-guide/sysrq.html
