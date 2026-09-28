# watchgoose

> If you don't honk, VM gets bonk

<img width="277" alt="watchgoose" src="https://github.com/user-attachments/assets/627c8bd1-dfba-41d8-b7a4-a47c34729755" />

watchgoose is a dead-man's switch for a Linux work VM. A separate machine
sends periodic HTTP requests; if they stop arriving, the VM repairs SSH
access and reboots. It measures whether the sender can reach the VM, not
whether SSH works.

**[Read the documentation](https://paperbenni.github.io/watchgoose/)** for
[installation](https://paperbenni.github.io/watchgoose/guide/install.html),
[network requirements](https://paperbenni.github.io/watchgoose/guide/network.html),
and [operating it](https://paperbenni.github.io/watchgoose/guide/operate.html).

The docs are built with VitePress. To preview them locally, run `npm ci` and
`npm run docs:dev`. Code checks run with `just check`.
