# Install

Requirements: Linux with systemd, Go 1.27.1 or later, `just`, and a separate
machine that can reach the VM over the network. Replace `VM_TAILNET_IP` and
`user@client-host` below with your actual values.

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

3. From the **separate machine**, verify the network path and activate the
   switch with one request:

   ```sh
   curl -fsS -X POST -m 10 http://VM_TAILNET_IP:9099/reassure
   ```

   A successful request has an empty response and exit status 0. The client
   must keep poking before the 20-minute deadline passes.

4. Install the continuous client from the checkout, over SSH to that
   separate machine:

   ```sh
   just test-client user@client-host http://VM_TAILNET_IP:9099/reassure
   # Add "5m 15s arm64" for an arm64 client host.
   ```

   This installs `/usr/local/bin/goosepoke` and enables `goosepoke.service`.
   The URL must include the `/reassure` path. Check the sender's journal with
   `ssh user@client-host 'journalctl -u goosepoke -f'`. For a one-off client
   check, run `goosepoke -url http://VM_TAILNET_IP:9099/reassure -once` on that
   host; it exits nonzero if the request fails. `goosepoke -help` lists its
   interval, timeout, TLS and optional YAML config flags.

See [Network and ports](/guide/network) for firewall and address requirements.
