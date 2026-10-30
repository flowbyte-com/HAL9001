package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/sipeed/picoclaw/pkg/bench"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "run":
		if err := runCmd(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "bench run:", err)
			os.Exit(1)
		}
	case "trial":
		if err := trialCmd(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "bench trial:", err)
			os.Exit(1)
		}
	case "help", "-h", "--help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `deepclaw benchmark harness

  bench run   --experiment A|B|C [options]
  bench trial --spec /path/to/trial-spec.json

Run options:
  --experiment A|B|C        A = memory policy, B = coding tool profile,
                            C = task-state mechanism (required)
  --trials N                independent trials per condition/scenario (default 1)
  --provider mock|live      provider kind (default mock)
  --provider-config PATH    PicoClaw config for a live provider
  --model NAME              exact model identifier (required for live)
  --mpm-binary PATH         MPM executable for MPM conditions
  --conditions a,b          restrict conditions
  --scenarios a,b           restrict scenarios
  --out DIR                 output root (default .lab/bench/003f)
  --run-id ID               reproducible run identifier`)
}

func runCmd(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	experiment := fs.String("experiment", "", "A or B")
	trials := fs.Int("trials", 1, "trials per cell")
	provider := fs.String("provider", "mock", "mock or live")
	providerConfig := fs.String("provider-config", "", "provider config path")
	model := fs.String("model", "", "exact model identifier")
	mpmBinary := fs.String("mpm-binary", "", "MPM executable")
	conditions := fs.String("conditions", "", "comma-separated conditions")
	scenarios := fs.String("scenarios", "", "comma-separated scenarios")
	out := fs.String("out", "", "output root")
	runID := fs.String("run-id", "", "run identifier")
	if err := fs.Parse(args); err != nil {
		return err
	}
	summary, err := bench.RunExperiment(bench.RunOptions{
		Experiment:         *experiment,
		RunID:              *runID,
		OutDir:             *out,
		ProviderKind:       *provider,
		ProviderConfigPath: *providerConfig,
		ModelName:          *model,
		MPMBinary:          *mpmBinary,
		Trials:             *trials,
		Conditions:         splitCSV(*conditions),
		Scenarios:          splitCSV(*scenarios),
	})
	if err != nil {
		return err
	}
	fmt.Printf("run %s: %d trials, %d pass, %d fail, %d error\n",
		summary.RunID, summary.Validation.Total, summary.Validation.Passed,
		summary.Validation.Failed, summary.Validation.Errors)
	for _, aggregate := range summary.Aggregates {
		fmt.Printf("  %-18s %-30s correct=%d/%d requests=%d\n",
			aggregate.Condition, aggregate.Scenario, aggregate.Correct,
			aggregate.Trials, aggregate.LLMRequests)
	}
	return nil
}

func trialCmd(args []string) error {
	fs := flag.NewFlagSet("trial", flag.ContinueOnError)
	spec := fs.String("spec", os.Getenv("DEEPCLAW_BENCH_TRIAL_SPEC"), "trial spec path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*spec) == "" {
		return fmt.Errorf("--spec or DEEPCLAW_BENCH_TRIAL_SPEC is required")
	}
	result, err := bench.RunTrialFile(*spec)
	if err != nil {
		// A non-passing result is still a recorded observation; the parent reads
		// it from disk and decides how to report it.
		fmt.Println(result.Status, result.Error)
		return nil
	}
	fmt.Println("pass", result.TrialID)
	return nil
}

func splitCSV(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}
