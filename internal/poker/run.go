package poker

import (
	"context"
	"errors"
	"flag"
	"io"
	"log/slog"
	"os/signal"
	"syscall"
)

// Run sends reassurance until stopped, or once when requested.
func Run(args []string, stderr io.Writer) int {
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	opts, err := parseFlags(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		log.Error("cannot start, bad configuration", "err", err)
		usage(stderr)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	p := &pinger{client: newClient(opts), url: opts.URL, userAgent: "watchgoose-poke/1"}
	if err := run(ctx, p, opts, log); err != nil {
		log.Error("reassurance failed", "url", opts.URL, "err", err)
		return 1
	}
	return 0
}
