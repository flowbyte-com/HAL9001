package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/isolation"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/mpm"
	"github.com/sipeed/picoclaw/pkg/tools"
)

// deepClawUnsandboxedEnv is the explicit trusted-development escape hatch. It
// is intentionally separate from the protected configuration: it logs loudly
// and makes no read-only MPM claim.
const deepClawUnsandboxedEnv = "DEEPCLAW_MPM_UNSANDBOXED"

func deepClawUnsandboxedRequested() bool {
	return strings.TrimSpace(os.Getenv(deepClawUnsandboxedEnv)) == "1"
}

// deepClawProtectedPaths lists the host paths the coding tools must not write:
// the MPM workspace (databases and stored records), the MPM home (CLI config and
// caches) and the MPM executable itself, which the trusted retriever will run
// again on later turns.
func deepClawProtectedPaths(cfg mpm.Config) []string {
	paths := []string{cfg.Workspace, cfg.HomeDir}
	if executable := strings.TrimSpace(cfg.Executable); executable != "" {
		if !filepath.IsAbs(executable) {
			if resolved, err := exec.LookPath(executable); err == nil {
				executable = resolved
			} else if abs, absErr := filepath.Abs(executable); absErr == nil {
				// A relative path with a separator is resolved by exec.Command
				// against the agent process working directory, not PATH.
				executable = abs
			}
		}
		if executable != "" {
			paths = append(paths, executable)
		}
	}
	protected := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
		protected = append(protected, path)
	}
	return protected
}

// configureDeepClawMPMIsolation turns the coding-tool execution environment
// into one that cannot reach the MPM store. It enables the existing subprocess
// isolation backend, registers the MPM paths as protected (masked in the Linux
// bubblewrap view, denied to the in-process file tools) and verifies that the
// host can enforce the boundary. A failure means the caller must fail closed.
func configureDeepClawMPMIsolation(cfg *config.Config, mpmCfg mpm.Config) (unsandboxed bool, err error) {
	paths := deepClawProtectedPaths(mpmCfg)
	if err := isolation.ProtectPaths(paths...); err != nil {
		return false, err
	}

	if deepClawUnsandboxedRequested() {
		logger.WarnCF("deepclaw", "UNSAFE trusted-development mode enabled: "+
			"DEEPCLAW_MPM_UNSANDBOXED=1 leaves shell execution unisolated and can write MPM",
			map[string]any{
				"protected_paths": paths,
				"risk":            "the model-driven shell can modify MPM data; do not use with untrusted input",
			})
		return true, nil
	}

	if cfg != nil {
		cfg.Isolation.Enabled = true
		isolation.Configure(cfg)
	}
	if err := isolation.SupportsProtectedPaths(); err != nil {
		return false, fmt.Errorf("MPM read-only isolation is unavailable: %w", err)
	}
	if err := isolation.Preflight(); err != nil {
		return false, fmt.Errorf("prepare MPM read-only isolation: %w", err)
	}
	return false, nil
}

// deepClawMPMConfigFromEnv returns the MPM paths from the environment even when
// retriever initialization failed, so a requested-but-unusable MPM setup still
// protects whatever store the operator pointed at.
func deepClawMPMConfigFromEnv() mpm.Config {
	return mpm.Config{
		Executable: os.Getenv("DEEPCLAW_MPM_EXECUTABLE"),
		Workspace:  os.Getenv("MPM_WORKSPACE"),
		HomeDir:    os.Getenv("DEEPCLAW_MPM_HOME"),
		ProjectID:  os.Getenv("DEEPCLAW_MPM_PROJECT"),
	}
}

// removeExecForFailedIsolation is the fail-closed response to an unavailable
// protection backend: no shell capability is safer than an unprotected shell
// that can write MPM.
func removeExecForFailedIsolation(registry *tools.ToolRegistry, cause error) {
	logger.ErrorCF("agent", "MPM read-only isolation unavailable; removing shell execution",
		map[string]any{
			"error": cause.Error(),
			"hint": "install bubblewrap on Linux or set " + deepClawUnsandboxedEnv +
				"=1 for the documented unsafe development mode",
		})
	if registry == nil {
		return
	}
	if execTool, ok := registry.Get("exec"); ok {
		registry.UnregisterOwned(execTool)
	}
}
