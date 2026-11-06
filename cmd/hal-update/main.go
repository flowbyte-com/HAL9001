// Command hal-update installs, checks, updates and rolls back HAL9001.
//
// The updater is deliberately standalone: it links only the installation
// package and the build identity, so recovering HAL never requires
// starting the HAL runtime or using MPM.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/flowbyte-com/HAL9001/internal/buildinfo"
	"github.com/flowbyte-com/HAL9001/internal/install"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "hal-update:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("hal-update", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		status          bool
		check           bool
		rollback        bool
		bootstrap       bool
		version         bool
		from            string
		replaceExisting bool
		remote          string
		branch          string
	)
	fs.BoolVar(&status, "status", false, "report the installed release, rollback candidate and health; no network")
	fs.BoolVar(&check, "check", false, "check the stable branch for a newer commit; change nothing")
	fs.BoolVar(&rollback, "rollback", false, "atomically restore the recorded previous release; no network or rebuild")
	fs.BoolVar(&bootstrap, "bootstrap", false, "install a release from a local checkout")
	fs.BoolVar(&version, "version", false, "print the updater version and build commit")
	fs.StringVar(&from, "from", "", "bootstrap source checkout (default: the current directory)")
	fs.BoolVar(&replaceExisting, "replace-existing", false, "during bootstrap, preserve an unrelated existing hal/hal-update under state/backups before replacing it")
	fs.StringVar(&remote, "remote", envOr(install.EnvRemote, install.DefaultRemote), "trusted Git remote for updates")
	fs.StringVar(&branch, "branch", envOr(install.EnvBranch, install.DefaultBranch), "stable branch updates follow")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "hal-update — install, check, update and roll back HAL9001")
		fmt.Fprintln(stderr)
		fmt.Fprintln(stderr, "Usage:")
		fmt.Fprintln(stderr, "  hal-update --status")
		fmt.Fprintln(stderr, "  hal-update --check")
		fmt.Fprintln(stderr, "  hal-update")
		fmt.Fprintln(stderr, "  hal-update --rollback")
		fmt.Fprintln(stderr, "  hal-update --bootstrap [--from DIR] [--replace-existing]")
		fmt.Fprintln(stderr, "  hal-update --version")
		fmt.Fprintln(stderr)
		fmt.Fprintln(stderr, "Options:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	actions := 0
	for _, set := range []bool{status, check, rollback, bootstrap, version} {
		if set {
			actions++
		}
	}
	if actions > 1 {
		return fmt.Errorf("choose one of --status, --check, --rollback, --bootstrap or --version")
	}
	if from != "" && !bootstrap {
		return fmt.Errorf("--from is only valid with --bootstrap")
	}
	if replaceExisting && !bootstrap {
		return fmt.Errorf("--replace-existing is only valid with --bootstrap")
	}
	if version {
		fmt.Fprint(stdout, buildinfo.Report("HAL9001 updater"))
		return nil
	}

	layout, err := install.DefaultLayout()
	if err != nil {
		return err
	}
	if status {
		fmt.Fprint(stdout, install.Inspect(layout).Report())
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if check {
		result, err := install.Check(ctx, layout, install.NewGitRemote(remote), branch)
		if err != nil {
			return err
		}
		fmt.Fprint(stdout, result.Report())
		return nil
	}
	if rollback {
		result, err := install.Rollback(ctx, layout)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, result.Report)
		return nil
	}
	if bootstrap {
		if from == "" {
			from = "."
		}
		result, err := install.Bootstrap(ctx, layout, from, nil, replaceExisting)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, result.Report)
		return nil
	}
	result, err := install.Update(ctx, layout, install.NewGitRemote(remote), branch, nil)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, result.Report)
	return nil
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
