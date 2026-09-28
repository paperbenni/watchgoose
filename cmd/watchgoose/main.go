// Command watchgoose runs the listener, sender, and setup wizard.
package main

import (
	"fmt"
	"os"
	"strings"

	"watchgoose/internal/listener"
	"watchgoose/internal/poker"
	"watchgoose/internal/setup"
)

func main() { os.Exit(dispatch(os.Args[1:])) }

func dispatch(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	switch args[0] {
	case "listen":
		return listener.Run(args[1:])
	case "poke":
		return poker.Run(args[1:], os.Stderr)
	case "setup":
		if err := setup.Run(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "watchgoose setup:", err)
			return 1
		}
		return 0
	case "help", "-h", "--help":
		usage()
		return 0
	default:
		// Older installed units and the reboot escalation child still pass
		// listener flags directly. Keep them working across an upgrade.
		if strings.HasPrefix(args[0], "-") {
			return listener.Run(args)
		}
		fmt.Fprintf(os.Stderr, "watchgoose: unknown command %q\n", args[0])
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "Usage: watchgoose <listen|poke|setup> [options]")
	fmt.Fprintln(os.Stderr, "  listen  receive reassurance and repair this machine on silence")
	fmt.Fprintln(os.Stderr, "  poke    periodically reassure a listener from another machine")
	fmt.Fprintln(os.Stderr, "  setup   choose a role and install this binary, config, and service")
}
