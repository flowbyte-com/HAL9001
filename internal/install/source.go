package install

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var branchPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

// SourceMeta is the immutable identity of a staged source checkout.
type SourceMeta struct {
	Commit  string
	Version string
	Epoch   int64
	Source  string
}

// Remote resolves the stable branch and fetches one exact commit into
// an isolated directory. Implementations must never build from a moving
// branch reference.
type Remote interface {
	Resolve(ctx context.Context, branch string) (string, error)
	Fetch(ctx context.Context, commit, dest string) (SourceMeta, error)
	IsAncestor(ctx context.Context, branch, older, newer string) (bool, error)
}

// GitRemote talks to the configured trusted repository.
type GitRemote struct {
	URL string
}

// NewGitRemote returns a remote for url, or the official default.
func NewGitRemote(url string) GitRemote {
	if strings.TrimSpace(url) == "" {
		url = DefaultRemote
	}
	return GitRemote{URL: strings.TrimSpace(url)}
}

// ValidateBranch rejects text that could be read as a Git option.
func ValidateBranch(branch string) error {
	if !branchPattern.MatchString(branch) || strings.Contains(branch, "..") ||
		strings.Contains(branch, "//") || strings.HasSuffix(branch, "/") ||
		strings.HasSuffix(branch, ".") {
		return fmt.Errorf("invalid stable branch name %q", branch)
	}
	return nil
}

func (r GitRemote) validate() error {
	url := strings.TrimSpace(r.URL)
	if url == "" {
		return fmt.Errorf("update remote is empty")
	}
	if strings.HasPrefix(url, "-") {
		return fmt.Errorf("update remote %q is not a valid Git remote", url)
	}
	if strings.ContainsAny(url, "\x00\n\r") {
		return fmt.Errorf("update remote contains a control character")
	}
	return nil
}

// Resolve returns the exact commit currently at refs/heads/<branch>.
func (r GitRemote) Resolve(ctx context.Context, branch string) (string, error) {
	if err := r.validate(); err != nil {
		return "", err
	}
	if err := ValidateBranch(branch); err != nil {
		return "", err
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return "", fmt.Errorf("Git is required to check the update source: %w", err)
	}
	cmd := exec.CommandContext(ctx, git, "ls-remote", "--heads", r.URL, "refs/heads/"+branch)
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("cannot reach the update source %s: %v: %s", r.URL, err, trimOutput(out))
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[1] != "refs/heads/"+branch {
			continue
		}
		if !ValidRevision(fields[0]) {
			return "", fmt.Errorf("the update source returned an invalid commit %q", fields[0])
		}
		return fields[0], nil
	}
	return "", fmt.Errorf("the update source has no stable branch %q", branch)
}

// Fetch checks out exactly one commit in an isolated directory. No
// branch tip is checked out and no Git hooks are run.
func (r GitRemote) Fetch(ctx context.Context, commit, dest string) (SourceMeta, error) {
	var meta SourceMeta
	if err := r.validate(); err != nil {
		return meta, err
	}
	if !ValidRevision(commit) {
		return meta, fmt.Errorf("refusing to fetch non-exact revision %q", commit)
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return meta, fmt.Errorf("Git is required to update HAL9001: %w", err)
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return meta, err
	}
	run := func(args ...string) error {
		cmd := exec.CommandContext(ctx, git, args...)
		cmd.Dir = dest
		cmd.Env = gitEnv()
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%v: %s", err, trimOutput(out))
		}
		return nil
	}
	if err := run("init", "--quiet"); err != nil {
		return meta, fmt.Errorf("cannot prepare the source checkout: %w", err)
	}
	if err := run("-c", "core.hooksPath=/dev/null", "fetch", "--quiet", "--depth=1", "--no-tags", r.URL, commit); err != nil {
		return meta, fmt.Errorf("cannot fetch commit %s: %w", commit, err)
	}
	if err := run("-c", "core.hooksPath=/dev/null", "checkout", "--quiet", "--detach", "FETCH_HEAD"); err != nil {
		return meta, fmt.Errorf("cannot check out commit %s: %w", commit, err)
	}
	head, err := gitOutput(ctx, dest, "rev-parse", "HEAD")
	if err != nil {
		return meta, err
	}
	if head != commit {
		return meta, fmt.Errorf("the fetched checkout is at %s, not the pinned commit %s", head, commit)
	}
	epoch, err := gitEpoch(ctx, dest)
	if err != nil {
		return meta, err
	}
	version, _ := gitVersion(ctx, dest)
	if version == "" {
		version = "dev-" + commit[:8]
	}
	return SourceMeta{
		Commit:  commit,
		Version: version,
		Epoch:   epoch,
		Source:  r.URL,
	}, nil
}

// IsAncestor reports whether older is reachable from the current tip of
// branch. It fetches history into a disposable bare repository; the
// installation and the operator's checkout are never touched.
func (r GitRemote) IsAncestor(ctx context.Context, branch, older, newer string) (bool, error) {
	if err := r.validate(); err != nil {
		return false, err
	}
	if err := ValidateBranch(branch); err != nil {
		return false, err
	}
	if !ValidRevision(older) || !ValidRevision(newer) {
		return false, fmt.Errorf("ancestry check needs two full commit SHAs")
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return false, fmt.Errorf("Git is required to check the update source: %w", err)
	}
	tmp, err := os.MkdirTemp("", "hal9001-check-*")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(tmp)
	run := func(args ...string) (int, string, error) {
		cmd := exec.CommandContext(ctx, git, args...)
		cmd.Dir = tmp
		cmd.Env = gitEnv()
		out, err := cmd.CombinedOutput()
		if err == nil {
			return 0, string(out), nil
		}
		if exit, ok := err.(*exec.ExitError); ok {
			return exit.ExitCode(), string(out), nil
		}
		return -1, string(out), err
	}
	if code, out, err := run("init", "--quiet", "--bare"); err != nil || code != 0 {
		return false, fmt.Errorf("cannot prepare the ancestry check: %v: %s", err, trimOutput([]byte(out)))
	}
	fetch := func(filter bool) (int, string, error) {
		args := []string{"fetch", "--quiet", "--no-tags"}
		if filter {
			args = append(args, "--filter=blob:none")
		}
		args = append(args, r.URL, "refs/heads/"+branch+":refs/heads/hal-check")
		return run(args...)
	}
	code, out, err := fetch(true)
	if err != nil || code != 0 {
		code, out, err = fetch(false)
	}
	if err != nil || code != 0 {
		return false, fmt.Errorf("cannot fetch branch %q for comparison: %v: %s", branch, err, trimOutput([]byte(out)))
	}
	if code, _, _ := run("cat-file", "-e", newer+"^{commit}"); code != 0 {
		return false, fmt.Errorf("the update source did not provide commit %s", newer)
	}
	if code, _, _ := run("cat-file", "-e", older+"^{commit}"); code != 0 {
		// The installed revision is not in the stable branch's history.
		return false, nil
	}
	code, out, err = run("merge-base", "--is-ancestor", older, newer)
	if err != nil {
		return false, err
	}
	switch code {
	case 0:
		return true, nil
	case 1:
		return false, nil
	default:
		return false, fmt.Errorf("ancestry check failed: %s", trimOutput([]byte(out)))
	}
}

// LocalSource resolves a clean checkout for bootstrap installation.
func LocalSource(ctx context.Context, dir string) (SourceMeta, string, error) {
	var meta SourceMeta
	abs, err := filepath.Abs(dir)
	if err != nil {
		return meta, "", err
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return meta, "", fmt.Errorf("no such source checkout: %s", dir)
	}
	top, err := gitOutput(ctx, abs, "rev-parse", "--show-toplevel")
	if err != nil {
		return meta, "", fmt.Errorf("%s is not a Git checkout: %w", dir, err)
	}
	top, err = filepath.Abs(top)
	if err != nil {
		return meta, "", err
	}
	dirty, err := gitOutput(ctx, top, "status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return meta, "", err
	}
	if strings.TrimSpace(dirty) != "" {
		first := strings.SplitN(strings.TrimSpace(dirty), "\n", 2)[0]
		return meta, "", fmt.Errorf("the source checkout %s has uncommitted changes (%s); commit or stash them before bootstrapping", top, first)
	}
	commit, err := gitOutput(ctx, top, "rev-parse", "HEAD")
	if err != nil {
		return meta, "", err
	}
	if !ValidRevision(commit) {
		return meta, "", fmt.Errorf("source HEAD %q is not a full commit SHA", commit)
	}
	epoch, err := gitEpoch(ctx, top)
	if err != nil {
		return meta, "", err
	}
	version, _ := gitVersion(ctx, top)
	if version == "" {
		version = "dev-" + commit[:8]
	}
	meta = SourceMeta{
		Commit:  commit,
		Version: version,
		Epoch:   epoch,
		Source:  "local:" + top,
	}
	return meta, top, nil
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, trimOutput(out))
	}
	return strings.TrimSpace(string(out)), nil
}

func gitEpoch(ctx context.Context, dir string) (int64, error) {
	raw, err := gitOutput(ctx, dir, "show", "-s", "--format=%ct", "HEAD")
	if err != nil {
		return 0, err
	}
	epoch, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || epoch <= 0 {
		return 0, fmt.Errorf("source commit has no usable timestamp")
	}
	return epoch, nil
}

func gitVersion(ctx context.Context, dir string) (string, error) {
	out, err := gitOutput(ctx, dir, "describe", "--tags", "--always", "--match", "v*")
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(out, "v") && len(out) > 1 && out[1] >= '0' && out[1] <= '9' {
		return out, nil
	}
	return "", nil
}

// Builder turns a checkout into the two release binaries.
type Builder interface {
	Build(ctx context.Context, sourceDir, binDir string, meta BuildMeta) error
}

// GoBuilder builds with the required Go toolchain.
type GoBuilder struct {
	Go string
	// CacheDir keeps the Go build cache under the installation's state
	// directory instead of scattering it through the operator's HOME.
	CacheDir string
}

// Build compiles hal and hal-update with reproducible metadata.
func (b GoBuilder) Build(ctx context.Context, sourceDir, binDir string, meta BuildMeta) error {
	goBin := strings.TrimSpace(b.Go)
	if goBin == "" {
		goBin = "go"
	}
	if _, err := exec.LookPath(goBin); err != nil {
		return fmt.Errorf("Go is required to build HAL9001: %w", err)
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	env := withEnv(os.Environ(), "CGO_ENABLED", "0")
	env = withEnv(env, "GOENV", "off")
	env = withEnv(env, "GOTELEMETRY", "off")
	if b.CacheDir != "" {
		if err := EnsureRealDir(b.CacheDir, 0o700); err != nil {
			return fmt.Errorf("cannot prepare the Go build cache: %w", err)
		}
		env = withEnv(env, "GOCACHE", b.CacheDir)
	}
	ldflags := fmt.Sprintf(
		"-X github.com/flowbyte-com/HAL9001/internal/buildinfo.Version=%s "+
			"-X github.com/flowbyte-com/HAL9001/internal/buildinfo.Commit=%s "+
			"-X github.com/flowbyte-com/HAL9001/internal/buildinfo.BuildEpoch=%d "+
			"-X github.com/flowbyte-com/HAL9001/internal/buildinfo.Dirty=false",
		meta.Version, meta.Commit, meta.Epoch,
	)
	builds := []struct {
		name   string
		target string
	}{
		{HalName, "./cmd/hal9001"},
		{UpdaterName, "./cmd/hal-update"},
	}
	for _, build := range builds {
		out := filepath.Join(binDir, build.name)
		cmd := exec.CommandContext(ctx, goBin, "build",
			"-trimpath", "-buildvcs=false", "-mod=readonly",
			"-ldflags", ldflags, "-o", out, build.target)
		cmd.Dir = sourceDir
		cmd.Env = env
		combined, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("the build of %s failed: %v: %s", build.name, err, trimOutput(combined))
		}
	}
	return nil
}

func withEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			out = append(out, entry)
		}
	}
	return append(out, prefix+value)
}

// gitEnv prevents an unattended check or update from blocking on a
// credential prompt. Missing credentials are a clear failure instead.
func gitEnv() []string {
	return withEnv(os.Environ(), "GIT_TERMINAL_PROMPT", "0")
}

func trimOutput(out []byte) string {
	text := strings.TrimSpace(string(out))
	const max = 4000
	if len(text) > max {
		return text[len(text)-max:]
	}
	return text
}
