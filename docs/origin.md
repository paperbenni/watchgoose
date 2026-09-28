# Why watchgoose exists

On the VM this was written for, a boot failed to mount `/boot`. That blocked
`local-fs.target`, so the boot stalled in a degraded state. `/vol/storage1` and
`/home` were `nofail` in fstab, so systemd only *wanted* them and nothing
*required* them. They were never mounted. The root disk's `/home` was empty,
but `sshd` started anyway and rejected every user's key.

There were no successful logins and 91 failed attempts over 33 hours. The
visible symptom, “public key rejected,” pointed at the wrong layer. Every
check that could explain the real fault needed a login, and login was the
thing that had failed.

watchgoose responds to the absence of requests from a separate machine. It
repairs what it can locally and reboots. In this incident, the volume mounting
on a later boot is what would actually restore ordinary users' access. This
does not tell the daemon why requests stopped; a network failure or a broken
sender produces the same response.
