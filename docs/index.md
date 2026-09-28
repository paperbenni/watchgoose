---
layout: home

hero:
  name: watchgoose
  text: If you don't honk, VM gets bonk
  tagline: A dead-man's switch for a Linux work VM
  actions:
    - theme: brand
      text: Get started
      link: /guide/install
    - theme: alt
      text: How it works
      link: /guide/how-it-works

features:
  - title: External reassurance
    details: A separate machine periodically tells the VM that it can still reach it.
  - title: Access repair
    details: When requests stop, the VM repairs configured SSH access before rebooting.
  - title: Reboot backstop
    details: A forceful kernel reboot follows if the graceful reboot stalls.
---

watchgoose measures whether the sender can reach its HTTP endpoint. It does
not check whether SSH works or diagnose why requests stopped. Read the
[limits](/guide/limits) before enabling unattended reboots.
