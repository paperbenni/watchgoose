# Network and ports

| Host | Required connection | Example |
| --- | --- | --- |
| VM | Inbound TCP from the `goosepoke` host to `server.listen` | Port `9099` on the VM's tailnet IP |
| `goosepoke` host | Outbound TCP to the VM's address and port | `VM_TAILNET_IP:9099` |

If a host firewall or tailnet ACL blocks that path, allow the client to reach
the VM on the configured TCP port. No public internet port is required.
`goosepoke` needs no inbound port, and watchgoose uses no UDP ports. The HTTP
response uses the same connection. The `/health` endpoint uses the **same VM
port**. SSH is used to deploy the client and log into the VM, but is separate
from this signal.

Bind `server.listen` to the VM's private or tailnet IP, such as
`100.x.y.z:9099`. Binding to `0.0.0.0` exposes the endpoint on every
interface. The daemon serves plain HTTP with no built-in TLS or
authentication. Anyone who can reach the endpoint can keep the switch
disarmed, so restrict access with a tailnet ACL or firewall if needed. The
listen address selects an interface; it does not authenticate clients. See
[ADR 0006](/adr/0006-unauthenticated-heartbeat).
