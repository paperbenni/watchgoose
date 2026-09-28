# Install

Requirements: Linux with systemd, Go 1.27.1 or later, `just`, and two machines
that can reach each other over the network. The listener is the machine that
will repair and reboot itself; the poker is a separate, more reliable machine.

1. Clone this repository on the **listener** and run:

   ```sh
   just setup
   ```

   Choose **Listen and repair this machine**. The Huh wizard asks for the
   listener's private or tailnet `IP:port`, at least one SSH public key for the
   recovery user, and optional home volume and account settings. It validates
   the generated config before installing `/srv/watchgoose/watchgoose`,
   `/etc/watchgoose.yaml`, and `watchgoose.service`. When available, the wizard
   fills in the local Tailscale IPv4 address, a mounted `/home`, and plain
   public keys from your `~/.ssh/authorized_keys`; review and edit each value.
   On a fresh install the switch waits for the first successful poke before it
   can repair or reboot.
   On a rerun, the wizard offers to keep the existing config or edit its values.

2. Clone this repository on the **poker** and run:

   ```sh
   just setup
   ```

   Choose **Poke other machines**. Enter each listener's full reassurance URL
   on its own line, including its path, such as
   `http://VM_TAILNET_IP:9099/reassure`. Setup installs
   `/usr/local/bin/watchgoose`, writes one config under `/etc/watchgoose/poke/`
   per listener, and enables one
   `watchgoose-poke@N.service` per URL. Each service pokes immediately and then
   every five minutes, so the first successful request activates a fresh
   listener. Re-run setup to edit the prefilled server list.

The built binary also runs the wizard directly as `watchgoose setup`.
`watchgoose listen -help` and `watchgoose poke -help` list their options. A
single request can be sent with
`watchgoose poke -url http://VM_TAILNET_IP:9099/reassure -once`.

See [Network and ports](/guide/network) for firewall and address requirements.
