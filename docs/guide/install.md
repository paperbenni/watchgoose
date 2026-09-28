# Install

Run this on each machine from an interactive terminal:

```sh
curl -fsSL https://raw.githubusercontent.com/paperbenni/watchgoose/main/bootstrap.sh | bash
```

The command downloads the latest Linux binary, checks its SHA-256 checksum,
and opens the setup wizard. Choose **Listen and repair this machine** on the VM
that needs recovery. Choose **Poke other machines** on a separate, more reliable
machine. Both machines need Linux with systemd, `curl`, and network access to
each other. The one-line command will work after the first release is
published; use the source-build instructions below until then.

On the listener, review the suggested Tailscale address, mounted home volume,
and SSH public keys. The wizard installs the binary, config, and service, then
starts the listener. A fresh listener waits for its first successful poke
before it can repair or reboot. On a rerun, the wizard offers to keep the
existing config or edit it.

On the poker, enter each listener's full reassurance URL on its own line, such
as `http://VM_TAILNET_IP:9099/reassure`. The wizard installs the binary and a
service for each URL. Each service pokes immediately and then every five
minutes. Rerun setup to edit the prefilled server list.

## Build from source

Until a release is available, install Go 1.27.1 or later and `just`, then run
this on each machine:

```sh
git clone https://github.com/paperbenni/watchgoose.git
cd watchgoose
just setup
```

The built binary can also run the wizard as `watchgoose setup`.
`watchgoose listen -help` and `watchgoose poke -help` list their options.

See [Network and ports](/guide/network) for firewall and address requirements.
