package apply

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jarrod-lowe/rdk/internal/manifest"
)

func setupRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "rdk"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"config.yaml": "kind: config\nname: demo\n",
		"assets.yaml": "kind: s3-bucket\nname: assets\ndescription: Static assets\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, "rdk", name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestRunWritesManagedTree(t *testing.T) {
	root := setupRepo(t)
	res, err := Run(root, "test-version")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.FilesWritten == 0 {
		t.Error("FilesWritten = 0")
	}
	for _, p := range []string{
		"rdk-managed/README.md",
		"rdk-managed/manifest.json",
		"rdk-managed/terraform/main.tf.json",
		"rdk-managed/terraform/modules/s3-bucket/main.tf",
	} {
		if _, err := os.Stat(filepath.Join(root, p)); err != nil {
			t.Errorf("missing %s: %v", p, err)
		}
	}
}

func TestRunRemovesStrayManagedFiles(t *testing.T) {
	root := setupRepo(t)
	if _, err := Run(root, "test-version"); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(root, "rdk-managed", "stray.txt")
	if err := os.WriteFile(stray, []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(root, "test-version"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Error("stray file survived apply; managed dir must be wiped")
	}
}

func TestRunFailsWithoutDefinitions(t *testing.T) {
	root := t.TempDir()
	if _, err := Run(root, "test-version"); err == nil {
		t.Error("want error when rdk/ is missing")
	}
}

func TestRunDoesNotDeleteThroughSymlinkedDir(t *testing.T) {
	root := setupRepo(t)
	if _, err := Run(root, "v"); err != nil {
		t.Fatal(err)
	}
	// A victim file in an external directory, outside the repo.
	external := t.TempDir()
	victim := filepath.Join(external, "victim")
	if err := os.WriteFile(victim, []byte("precious"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A symlink INSIDE the repo pointing at the external dir.
	if err := os.Symlink(external, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}
	// A manifest that tracks link/victim (lexically clean — passes the ..
	// /absolute guard) with a hash matching the victim's content. Without
	// symlink-safe resolution the stale-delete branch would os.Remove the
	// external victim.
	m := manifest.Manifest{RdkVersion: "v", OutsideFiles: map[string]string{
		"link/victim": manifest.Hash([]byte("precious"))}}
	enc, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "rdk-managed", "manifest.json"), enc, 0o644); err != nil {
		t.Fatal(err)
	}
	// Apply may error or no-op; the ONLY invariant is the external file survives.
	if _, err := Run(root, "v"); err != nil {
		t.Logf("apply returned (acceptable): %v", err)
	}
	if _, statErr := os.Stat(victim); statErr != nil {
		t.Fatalf("external victim was touched through a symlinked dir: %v", statErr)
	}
}

func TestRunSurfacesUnreadableOutsideFile(t *testing.T) {
	root := setupRepo(t)
	// First apply establishes rdk-managed/ and a manifest.
	if _, err := Run(root, "v"); err != nil {
		t.Fatal(err)
	}
	// Seed a manifest that tracks an outside path, then make that path a
	// directory so os.ReadFile returns a non-NotExist error. That real error
	// must surface — not be masked as "absent" (which would silently drop the
	// tracked entry, violating rule 6).
	m := manifest.Manifest{
		RdkVersion:   "v",
		OutsideFiles: map[string]string{"tracked-dir": manifest.Hash([]byte("x"))},
	}
	enc, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "rdk-managed", "manifest.json"), enc, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "tracked-dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(root, "v"); err == nil {
		t.Error("want error when a tracked outside file cannot be read; got nil (masked as absent?)")
	}
}
