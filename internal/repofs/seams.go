package repofs

// afterLockOwnershipRecorded is a test seam. Nil in production, so it costs
// one nil check on a path that already does filesystem work.
//
// It exists because the ordering it verifies — this store records a lock as
// its own before the lock file becomes visible on disk — lives in the gap
// between two syscalls: a test that tried to hit that gap by racing
// goroutines would be timing-dependent, and a timing-dependent test for a
// timing bug is one that passes on the machine where the bug is worst. This
// makes the ordering itself the assertion instead. It is a package-level var
// rather than a field on osStore so that no production caller can reach it:
// nothing outside this package can set it, and nothing inside sets it except
// a test.
//
// Fires inside acquireLock once this store has recorded the lock as its own
// and before the lock becomes visible via Link — and it fires with lockMu
// still held, since holding it across exactly that span is the fix (see
// acquireLock's doc comment). A callback that itself takes lockMu will
// deadlock. The only test that sets this today does an Lstat and nothing
// else, which is safe; a future callback needs to keep that constraint in
// mind rather than assume the mutex is free.
var afterLockOwnershipRecorded func()

// afterStaging is a test seam. Nil in production, so it costs one nil check
// on a path that already does filesystem work.
//
// It exists because the window it lets a test hit — this run's transaction
// lock broken or replaced after staging but before the destructive renames —
// is, by construction, between two syscalls: a test that tried to race it
// with goroutines would be timing-dependent, and a timing-dependent test for
// a timing bug is one that passes on the machine where the bug is worst. This
// makes the window itself reachable on demand instead. It is a package-level
// var rather than a field on osStore for the same reason as
// afterLockOwnershipRecorded: nothing outside this package can set it, and
// nothing inside sets it except a test.
//
// Fires inside Materialize once the new tree is fully staged in .rdk/new and
// before checkStillLocked's first call. A test uses it to break (or break and
// replace) this run's lock at the exact point where doing so used to produce
// a mixed tree in silence.
var afterStaging func()

// afterApplyLockHeldByHoldLock is a test seam. Nil in production, so it costs
// one nil check on a path that already does filesystem work.
//
// It exists because the property it lets a test assert — that for the whole
// span in which HoldLock is creating the held lock, a concurrent Materialize
// is genuinely excluded — lives between two syscalls (acquiring the
// transaction lock and creating the held lock under it): a test that tried to
// hit that window by racing goroutines would be timing-dependent, and a
// timing-dependent test for a timing bug is one that passes on the machine
// where the bug is worst. This makes the window itself reachable on demand
// instead. It is a package-level var rather than a field on osStore for the
// same reason as the other seams in this file: nothing outside this package
// can set it, and nothing inside sets it except a test.
//
// Fires inside HoldLock once it holds the transaction lock and before it
// creates the held lock. A test uses it to run a concurrent Materialize from
// inside the callback and assert that it blocks on ErrLocked — the property
// that stops rdk lock from ever returning success while an apply is
// genuinely running underneath it.
var afterApplyLockHeldByHoldLock func()

// afterPublish is a test seam. Nil in production, so it costs one nil check
// on a path that already does filesystem work.
//
// It exists because the point it marks — the tree is already published, but
// the sweep of .rdk/old and the lock release still to come — lives, by
// construction, between two syscalls: a test that tried to reach it by
// racing goroutines would be timing-dependent, and a timing-dependent test
// for a timing bug is one that passes on the machine where the bug is
// worst. This makes that point reachable on demand instead. It is a
// package-level var rather than a field on osStore for the same reason as
// the other seams in this file: nothing outside this package can set it,
// and nothing inside sets it except a test.
//
// Fires inside Materialize once the publishing rename has succeeded and
// before checkStillLocked's second call (the one guarding the sweep). A test
// uses it two ways: to break this run's lock from inside the callback, so
// that second checkStillLocked call — otherwise unreachable through
// Materialize, since nothing else fires between publish and the sweep —
// refuses the sweep instead of silently clearing what may be another run's
// only remaining copy; or to make .rdk unwritable, so the deferred
// ReleaseLock that runs after Materialize returns fails, reaching the path
// where the apply itself succeeded but its lock is still on disk.
var afterPublish func()
