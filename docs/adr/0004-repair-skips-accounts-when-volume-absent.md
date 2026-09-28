# Repair skips the ordinary accounts when the volume is absent

The repair step always fixes the [recovery user](../CONTEXT.md), and repairs configured
ordinary accounts only when the [volume](../CONTEXT.md) is actually mounted. When it is not, those
writes are silently ineffective: `/home` is an empty directory on the root disk, so anything
written there is shadowed the moment the volume mounts on a later boot.

A reboot is the remedy for a missing volume, not a key rewrite. Writing the keys anyway would
produce a repair log that claims success while changing nothing that will ever be read.

## Consequences

The most likely reason for the switch to fire is a volume that did not mount — and that is
precisely the case in which repairing the ordinary accounts achieves nothing. A log line
distinguishing "volume absent, only recovery user repaired" from "volume present, all accounts
repaired" is worth more than a uniform success message, because the two cases have different
remedies.
