package main

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/jarrod-lowe/rdk/internal/apply"
	"github.com/jarrod-lowe/rdk/internal/repofs"
)

var update = flag.Bool("update", false, "rewrite golden 'want' trees from current output")

// readTree loads every file under root into relpath->string.
func readTree(t *testing.T, root string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		tree[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	for rel, content := range readTree(t, src) {
		p := filepath.Join(dst, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGolden(t *testing.T) {
	cases, err := os.ReadDir("testdata/golden")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Name(), func(t *testing.T) {
			caseDir := filepath.Join("testdata", "golden", c.Name())
			work := t.TempDir()
			if err := os.MkdirAll(filepath.Join(work, "rdk"), 0o755); err != nil {
				t.Fatal(err)
			}
			copyDir(t, filepath.Join(caseDir, "rdk"), filepath.Join(work, "rdk"))

			// Golden output must not depend on the dev's build, so the
			// version is fixed.
			store, err := repofs.New(work)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := apply.Run(store, "golden"); err != nil {
				t.Fatalf("apply: %v", err)
			}
			got := readTree(t, filepath.Join(work, apply.ManagedDir))

			wantDir := filepath.Join(caseDir, "want")
			if *update {
				if err := os.RemoveAll(wantDir); err != nil {
					t.Fatal(err)
				}
				for rel, content := range got {
					p := filepath.Join(wantDir, filepath.FromSlash(rel))
					if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				t.Logf("updated %s", wantDir)
				return
			}
			want := readTree(t, wantDir)
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("generated tree differs from golden (-want +got):\n%s\n(review the change, then: go test -run TestGolden -update)", diff)
			}

			// Idempotence (DD-1): applying again must change nothing.
			if _, err := apply.Run(store, "golden"); err != nil {
				t.Fatalf("second apply: %v", err)
			}
			again := readTree(t, filepath.Join(work, apply.ManagedDir))
			if diff := cmp.Diff(got, again); diff != "" {
				t.Errorf("apply is not idempotent (-first +second):\n%s", diff)
			}
		})
	}
}

// TestTerraformValidate is a local smoke test; skipped when terraform (or
// tofu) isn't installed. It needs network for provider download, so it is
// not part of the deterministic core suite.
func TestTerraformValidate(t *testing.T) {
	tf, err := exec.LookPath("tofu")
	if err != nil {
		tf, err = exec.LookPath("terraform")
	}
	if err != nil {
		t.Fatal("neither tofu nor terraform is installed; rdk's suite validates generated Terraform and must not pass without it — install OpenTofu or Terraform")
	}
	work := t.TempDir()
	os.MkdirAll(filepath.Join(work, "rdk"), 0o755)
	copyDir(t, "testdata/golden/basic/rdk", filepath.Join(work, "rdk"))
	store, err := repofs.New(work)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := apply.Run(store, "golden"); err != nil {
		t.Fatal(err)
	}
	tfDir := filepath.Join(work, apply.ManagedDir, "terraform")
	for _, args := range [][]string{{"init", "-backend=false"}, {"validate"}} {
		cmd := exec.Command(tf, args...)
		cmd.Dir = tfDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v\n%s", tf, args, err, out)
		}
	}
}
