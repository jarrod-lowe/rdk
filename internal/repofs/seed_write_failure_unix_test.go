//go:build unix

package repofs

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestSeedLeavesNothingBehindWhenTheWriteFails is the test the bug this PR
// fixes demanded: a write that fails partway must not leave a half-written
// file for the next Seed call to mistake for an already-seeded one.
//
// The failure is injected honestly, not mocked: RLIMIT_FSIZE caps how large a
// file this process may write, so the seed data (deliberately longer than the
// cap) triggers a genuine short write followed by EFBIG from the kernel — the
// same failure a full disk produces, without needing an actual full disk. The
// cap is set above the scratch .gitignore's 2-byte content, so that write
// (unconditional on every Seed call, via ensureScratchDir) succeeds and the
// failure lands where the bug lived: the seed's own temp-file write. Go's
// runtime does not terminate the process on the SIGXFSZ this raises — that
// signal defaults to ignored — so the write simply returns a short count and
// an error, exactly like any other write failure Seed has to handle.
//
// This is Unix-only because RLIMIT_FSIZE has no Windows equivalent; there is
// no other test in this package that depends on the platform.
func TestSeedLeavesNothingBehindWhenTheWriteFails(t *testing.T) {
	s, root := newTestStore(t)

	var old syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &old); err != nil {
		t.Fatalf("Getrlimit: %v", err)
	}
	// 8 bytes clears the gitignore's "*\n" but not the 64-byte seed payload
	// below, so the write that overruns it is the seed's, not the
	// gitignore's.
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: 8, Max: old.Max}); err != nil {
		t.Fatalf("Setrlimit: %v", err)
	}
	data := []byte("this seed payload is well over the eight byte rlimit cap")
	err := s.Seed("rdk/config.yaml", data)
	// Restored before any assertion below: t.Fatalf after this point would
	// otherwise leave the low limit in effect for whatever the test binary
	// runs next.
	if resetErr := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &old); resetErr != nil {
		t.Fatalf("restoring RLIMIT_FSIZE: %v", resetErr)
	}

	if err == nil {
		t.Fatal("Seed reported success despite the write failing")
	}

	if _, statErr := os.Lstat(filepath.Join(root, "rdk", "config.yaml")); !os.IsNotExist(statErr) {
		t.Errorf("rdk/config.yaml exists after a failed seed (statErr=%v) — a half-written file was left behind", statErr)
	}

	// Not just the target: the scratch temp file the failed write landed in
	// must be cleaned up too, or every failed seed leaks one into .rdk.
	entries, readErr := os.ReadDir(filepath.Join(root, ScratchDir))
	if readErr != nil {
		t.Fatalf("reading %s: %v", ScratchDir, readErr)
	}
	for _, e := range entries {
		if e.Name() != ".gitignore" {
			t.Errorf("%s still contains %s after a failed seed", ScratchDir, e.Name())
		}
	}
}
