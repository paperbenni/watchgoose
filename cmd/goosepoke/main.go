// Command goosepoke is the client half of a dead-man's switch: it periodically
// tells the machine that someone out here can still reach it.
//
// The entire payload of a poke is the arrival. There is no credential, no
// token, no timestamp and no signature, because the only meaning of the signal
// is "this got here". See docs/adr/0006-unauthenticated-heartbeat.md.
//
// The single most important property of this program is that its own failures
// are loud. A client that has silently stopped poking is indistinguishable,
// from the machine's side, from a machine nobody can reach: both look like
// silence, and silence is the trigger. So every failure is logged with enough
// detail to diagnose it, a transient network error is retried rather than
// swallowed, and only a bad configuration ends the process. The loop is the
// product; anything that quietly hides its own breakage is worse than not
// running at all.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	opts, err := parseFlags(os.Args[1:], os.Stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		log.Error("cannot start, bad configuration", "err", err)
		usage(os.Stderr)
		os.Exit(2)
	}

	// SIGTERM is what systemd sends on stop; SIGINT is what a person sends
	// from a terminal. Both mean the same thing here: finish and get out.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	p := &pinger{
		client:    newClient(opts),
		url:       opts.URL,
		userAgent: "watchgoose-poke/1",
	}
	if err := run(ctx, p, opts, log); err != nil {
		log.Error("reassurance failed", "url", opts.URL, "err", err)
		os.Exit(1)
	}
}
