// Command hal9001 launches HAL9001's native terminal interface: a
// conversational thinking partner with permanent 444 read-only authority.
// This foundation wires conversation storage and the interface; model
// provider adapters arrive with the provider milestone. Until then the
// interface stores what you say and says so honestly instead of
// fabricating replies.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/sipeed/picoclaw/hal9001/internal/config"
	"github.com/sipeed/picoclaw/hal9001/internal/engine"
	"github.com/sipeed/picoclaw/hal9001/internal/provider"
	"github.com/sipeed/picoclaw/hal9001/internal/session"
	"github.com/sipeed/picoclaw/hal9001/internal/tui"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "hal9001:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cfg, err := config.LoadErr(args)
	if err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if cfg.NoColor {
		os.Setenv("NO_COLOR", "1")
	}
	store, err := session.Open(cfg.StoreDir, cfg.Session)
	if err != nil {
		return err
	}
	// No provider adapters ship with the foundation, so the engine runs
	// without one: the TUI stores the conversation and reports that no
	// provider is configured instead of inventing answers.
	eng := engine.New(store, nil, provider.Model{ID: cfg.Model})
	return tui.Run(eng)
}
