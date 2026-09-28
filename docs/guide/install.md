# Install

Requirements: Linux with systemd, Go 1.27.1 or later, `just`, and a separate
machine that can reach the VM over the network. Replace `VM_TAILNET_IP` below
with your actual address.

1. On the VM, copy the
   [example config](https://github.com/paperbenni/watchgoose/blob/main/deploy/watchgoose.example.yaml)
   to `/etc/watchgoose.yaml`. Set `server.listen` to the VM's reachable
   address and put your real public key or keys in `repair.authorized_keys`.
   Set `repair.accounts` and `volume.mountpoint` if ordinary home directories
   need repair. The example deliberately has no usable address or key, so it
   cannot start unchanged.

2. Build and install from the repository checkout on the VM:

   ```sh
   just build
   sudo deploy/install-vm.sh
   sudo /srv/watchgoose/watchgoose -config /etc/watchgoose.yaml -check
   ```

   The installer preserves an existing config and state file. On a new
   install, it creates a waiting marker; it does not create the recovery
   account.

3. On the **separate sender machine**, clone the repository and run:

   ```sh
   just install-client
   ```

   Enter each watched VM's full URL, such as
   `http://VM_TAILNET_IP:9099/reassure`, on its own line, then press Enter on
   a blank line. The path is required. The recipe builds a native static binary,
   installs it at `/usr/local/bin/goosepoke`, and enables one service per URL.
   Each service pokes immediately, activating a fresh switch, and then every
   five minutes. Re-run the
   recipe to replace the server list. Check the sender's journal with
   `journalctl -u 'goosepoke@*.service' -f`. A one-off check is
   `goosepoke -url http://VM_TAILNET_IP:9099/reassure -once`; it exits nonzero
   if the request fails. `goosepoke -help` lists its interval, timeout, TLS and
   optional YAML config flags.

   If you build on the VM and deploy to a sender over SSH instead, the older
   `just test-client user@client-host http://VM_TAILNET_IP:9099/reassure`
   recipe remains available for one server.

See [Network and ports](/guide/network) for firewall and address requirements.
