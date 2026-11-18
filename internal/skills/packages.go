package skills

import (
	"context"
	"errors"
	"fmt"
)

// Progressive package reads.
//
// A skill read returns the entrypoint projection only: the body HAL may
// use without loading anything else. The supporting files of a package
// stay unread unless the operator (never the model, never the router)
// asks for one explicitly, and then only through bounded chunk reads
// defined here. MPM's read-only package operations (tracked upstream as
// P8) do not exist yet, so the production client does not implement
// PackageSource: the real P8 adapter is a separate, clearly identified
// integration step. Until then, test doubles stand in for it.
//
// The security posture is structural, not advisory:
//   - this interface has no write, execute, materialise or auto-load
//     operation, and HAL must never add one through it;
//   - package bytes may only flow into the same single-request guidance
//     block ordinary skill bodies use: never into the persona, never
//     into tool policy, never onto the filesystem;
//   - every read is bounded, and totals are bounded across chunks.

const (
	// PackageChunkDefault is the size of one file chunk read. It is
	// also the largest single chunk HAL will ask for: a source that
	// returns more than this is still bounded by the total below.
	PackageChunkDefault int64 = 32 << 10
	// MaxPackageReadTotal bounds the total bytes ReadBounded assembles
	// for one file. Anything larger must be narrowed by the operator,
	// never streamed into a request in full.
	MaxPackageReadTotal int64 = 256 << 10
)

// ErrPackageUnsupported reports that the connected store does not offer
// package operations. The skill itself may still be usable through its
// entrypoint projection; only the progressive read is unavailable.
var ErrPackageUnsupported = errors.New("skills: package reads are not supported by this MPM")

// ErrPackageUnavailable reports that a package read failed against a
// store that is otherwise reachable (missing file, changed package,
// I/O failure). Unlike ErrUnavailable, it is not about the store as a
// whole, so callers must not treat it as "no skills".
var ErrPackageUnavailable = errors.New("skills: the package could not be read")

// PackageDigest is the expected integrity record of a package: the
// digests a previous read or the migration receipt attested to.
type PackageDigest struct {
	// PackageHash is the expected digest of the package as a whole.
	PackageHash string
	// Files maps supporting-file paths to their expected digests.
	Files map[string]string
}

// PackageSource reads the supporting files of a skill package in
// bounded pieces. Implementations speak MPM's read-only package
// operations once those exist; until then only test doubles implement
// it.
type PackageSource interface {
	// ListFiles lists supporting files without loading any bytes.
	ListFiles(ctx context.Context, skillID string) ([]PackageFile, error)
	// ReadChunk reads at most limit bytes of one file starting at
	// offset. It returns the bytes read and the file's total size, so
	// callers can page without ever holding the whole file.
	ReadChunk(ctx context.Context, skillID, path string, offset, limit int64) (chunk []byte, total int64, err error)
	// Verify checks a package against known digests without returning
	// content.
	Verify(ctx context.Context, skillID string, want PackageDigest) error
}

// ReadBounded assembles one supporting file through bounded chunk reads
// and refuses anything larger than maxTotal instead of truncating it
// silently: a half-read script must never look like a whole one.
func ReadBounded(ctx context.Context, src PackageSource, skillID, path string, maxTotal int64) ([]byte, error) {
	if src == nil {
		return nil, ErrPackageUnsupported
	}
	if maxTotal <= 0 || maxTotal > MaxPackageReadTotal {
		maxTotal = MaxPackageReadTotal
	}
	var out []byte
	var offset int64
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		chunk, total, err := src.ReadChunk(ctx, skillID, path, offset, PackageChunkDefault)
		if err != nil {
			return nil, err
		}
		if total >= 0 && total > MaxPackageReadTotal {
			return nil, fmt.Errorf("skills: package file %q reports %d bytes, over the %d-byte read budget", path, total, MaxPackageReadTotal)
		}
		out = append(out, chunk...)
		if int64(len(out)) > maxTotal {
			return nil, fmt.Errorf("skills: package file %q exceeds the %d-byte read budget", path, maxTotal)
		}
		offset += int64(len(chunk))
		if len(chunk) == 0 || (total >= 0 && offset >= total) {
			return out, nil
		}
	}
}

// VerifyPackageDigest compares listed files against expected digests.
// It reports tampering, unexpected files and missing files alike, and
// says nothing on success.
func VerifyPackageDigest(got []PackageFile, want PackageDigest) error {
	if want.PackageHash == "" && len(want.Files) == 0 {
		return fmt.Errorf("skills: nothing to verify against")
	}
	seen := make(map[string]bool, len(got))
	for _, f := range got {
		seen[f.Path] = true
		wantSum, ok := want.Files[f.Path]
		if !ok {
			return fmt.Errorf("skills: unexpected package file %q", f.Path)
		}
		if wantSum == "" {
			continue
		}
		if f.SHA256 == "" {
			return fmt.Errorf("skills: package file %q carries no digest to check", f.Path)
		}
		if f.SHA256 != wantSum {
			return fmt.Errorf("skills: package file %q digest mismatch", f.Path)
		}
	}
	for path := range want.Files {
		if !seen[path] {
			return fmt.Errorf("skills: expected package file %q is missing", path)
		}
	}
	return nil
}
