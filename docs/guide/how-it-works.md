# How it works

| Program | Where it runs | What it does |
| --- | --- | --- |
| `watchgoose` | The VM | Receives reassurance, records each request's arrival time, and checks the deadline. |
| `goosepoke` | A separate, more reliable machine | Sends an empty HTTP `POST` immediately on start and then at a set interval. |

The example setup sends `POST /reassure` every **5 minutes**. The VM replies
with HTTP **204 No Content** and records its own receipt time. The sender's
clock and request body do not matter.

If no request arrives for **20 minutes**, the daemon notices on its next poll
(every **1 minute**). Once the VM has been up for at least **30 minutes**, it
repairs access, waits **1 minute**, and starts a graceful reboot. If that
reboot stalls for **5 minutes**, a child process triggers a forceful kernel
reboot. These times are configurable in the
[example config](https://github.com/paperbenni/watchgoose/blob/main/deploy/watchgoose.example.yaml).

On a fresh install, repair and reboot wait for the **first successful poke**.
After that, the recorded timestamp survives service restarts and VM boots;
the normal deadline applies without another setup step. A lost or corrupt
state file after installation is treated as missed reassurance.

`goosepoke` must run on another machine. It logs failed requests and keeps
retrying; a failed client or network path otherwise looks like an unreachable
VM to the daemon. A poke means only that this HTTP request arrived. It is
not proof that SSH or any other service works.

For the terms used throughout this project, see [Terminology](/CONTEXT).
