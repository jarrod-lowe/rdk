package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jarrod-lowe/rdk/internal/repofs"
)

func TestVersionCommand(t *testing.T) {
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"version"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out.String(), "rdk version ") {
		t.Errorf("got %q, want it to contain %q", out.String(), "rdk version ")
	}
}

// fakeSignalStore stands in for a repofs.Store for exactly one purpose:
// recording which of ReleaseLock/PrepareShutdown prepareStoreForShutdown
// actually calls. Every other method panics — nothing under test here is
// expected to reach parsing, generation, or any other lock operation, and a
// panic makes an accidental call to one of them fail loudly rather than
// quietly return a zero value that would let a wrong wiring pass.
type fakeSignalStore struct {
	releaseLockCalled     bool
	prepareShutdownCalled bool
}

func (f *fakeSignalStore) Materialize(string, *repofs.FileSet) error {
	panic("fakeSignalStore.Materialize: unexpected call")
}
func (f *fakeSignalStore) Seed(string, []byte) error {
	panic("fakeSignalStore.Seed: unexpected call")
}
func (f *fakeSignalStore) ReadFile(string) ([]byte, error) {
	panic("fakeSignalStore.ReadFile: unexpected call")
}
func (f *fakeSignalStore) ReadDir(string) ([]repofs.Entry, error) {
	panic("fakeSignalStore.ReadDir: unexpected call")
}
func (f *fakeSignalStore) ReleaseLock() error {
	f.releaseLockCalled = true
	return nil
}
func (f *fakeSignalStore) PrepareShutdown() error {
	f.prepareShutdownCalled = true
	return nil
}
func (f *fakeSignalStore) BreakLock(string) (repofs.LockInfo, error) {
	panic("fakeSignalStore.BreakLock: unexpected call")
}
func (f *fakeSignalStore) HoldLock(string) (repofs.LockInfo, error) {
	panic("fakeSignalStore.HoldLock: unexpected call")
}
func (f *fakeSignalStore) Unlock(string) error {
	panic("fakeSignalStore.Unlock: unexpected call")
}
func (f *fakeSignalStore) UseLock(string) (repofs.LockInfo, error) {
	panic("fakeSignalStore.UseLock: unexpected call")
}

// TestSignalHandlerPreparesShutdownRatherThanJustReleasing is a regression
// test for commit a0dd983 shipping repofs.Store.PrepareShutdown inert:
// nothing called it, because handleSignals still called only ReleaseLock,
// leaving open the exact race PrepareShutdown exists to close (see
// handleSignals' own doc comment in root.go). It cannot exercise that race
// itself — a real signal landing in the narrow window between acquireLock
// starting and taking its lock is not something a test can schedule
// deterministically from cmd, and internal/repofs's own suite already covers
// the mechanism (see acquireLock/PrepareShutdown's doc comments and
// TestMaterializeExcludesAHoldLockThatCompletesInTheGapBeforeAcquire's
// siblings in internal/repofs/store_test.go for that coverage). What this
// asserts is narrower but still real: prepareStoreForShutdown — the exact
// call handleSignals' goroutine makes on a signal, split out only so a test
// can reach it without going through the signal channel and os.Exit that
// follows it in production — calls PrepareShutdown on whatever Store
// setStore last recorded, not a bare ReleaseLock. Getting that wrong is
// exactly how the mechanism shipped inert the first time.
func TestSignalHandlerPreparesShutdownRatherThanJustReleasing(t *testing.T) {
	a := &app{}
	fake := &fakeSignalStore{}
	a.setStore(fake)

	a.prepareStoreForShutdown()

	if !fake.prepareShutdownCalled {
		t.Error("prepareStoreForShutdown did not call PrepareShutdown on the registered store")
	}
	if fake.releaseLockCalled {
		t.Error("prepareStoreForShutdown called ReleaseLock directly, bypassing PrepareShutdown's foreclosure of future acquisitions")
	}
}

// TestSignalHandlerWithNoStoreDoesNotPanic covers the version/init path:
// setStore is never called, so getStore returns the interface's nil value,
// and prepareStoreForShutdown must recognise that rather than call a method
// on it.
func TestSignalHandlerWithNoStoreDoesNotPanic(t *testing.T) {
	a := &app{}
	a.prepareStoreForShutdown()
}
