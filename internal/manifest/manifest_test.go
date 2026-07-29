package manifest

import (
	"strings"
	"testing"
)

// disk simulates the repo working tree outside the managed dir.
func disk(files map[string]string) func(string) ([]byte, bool) {
	return func(p string) ([]byte, bool) {
		s, ok := files[p]
		return []byte(s), ok
	}
}

func TestRowNewFileIsWritten(t *testing.T) {
	plan, err := Reconcile(Manifest{}, map[string][]byte{"ci.yml": []byte("a")}, disk(nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Writes) != 1 || plan.Writes[0] != "ci.yml" {
		t.Errorf("writes = %v, want [ci.yml]", plan.Writes)
	}
}

func TestRowCollisionWithForeignFile(t *testing.T) {
	_, err := Reconcile(Manifest{},
		map[string][]byte{"ci.yml": []byte("a")},
		disk(map[string]string{"ci.yml": "theirs"}))
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("want collision error, got %v", err)
	}
}

func TestRowUntouchedIsRewrittenUnconditionally(t *testing.T) {
	prev := Manifest{OutsideFiles: map[string]string{"ci.yml": Hash([]byte("old"))}}
	plan, err := Reconcile(prev,
		map[string][]byte{"ci.yml": []byte("old")}, // same content: still written (rule 13)
		disk(map[string]string{"ci.yml": "old"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Writes) != 1 {
		t.Errorf("writes = %v, want rewrite even when identical", plan.Writes)
	}
}

func TestRowEditedFileIsHardError(t *testing.T) {
	prev := Manifest{OutsideFiles: map[string]string{"ci.yml": Hash([]byte("old"))}}
	_, err := Reconcile(prev,
		map[string][]byte{"ci.yml": []byte("new")},
		disk(map[string]string{"ci.yml": "edited-by-user"}))
	if err == nil || !strings.Contains(err.Error(), "edited") {
		t.Fatalf("want edited error, got %v", err)
	}
}

func TestRowStaleCleanIsDeleted(t *testing.T) {
	prev := Manifest{OutsideFiles: map[string]string{"old.yml": Hash([]byte("x"))}}
	plan, err := Reconcile(prev, map[string][]byte{},
		disk(map[string]string{"old.yml": "x"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Deletes) != 1 || plan.Deletes[0] != "old.yml" {
		t.Errorf("deletes = %v, want [old.yml]", plan.Deletes)
	}
}

func TestRowStaleEditedIsHardError(t *testing.T) {
	prev := Manifest{OutsideFiles: map[string]string{"old.yml": Hash([]byte("x"))}}
	_, err := Reconcile(prev, map[string][]byte{},
		disk(map[string]string{"old.yml": "user-changed"}))
	if err == nil || !strings.Contains(err.Error(), "edited") {
		t.Fatalf("want edited-and-stale error, got %v", err)
	}
}

func TestStaleAlreadyGoneIsNoop(t *testing.T) {
	prev := Manifest{OutsideFiles: map[string]string{"old.yml": Hash([]byte("x"))}}
	plan, err := Reconcile(prev, map[string][]byte{}, disk(nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Deletes) != 0 {
		t.Errorf("deletes = %v, want none for already-absent file", plan.Deletes)
	}
}

