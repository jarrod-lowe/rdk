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

// afterHeldLockConfirmedAbsent is a test seam. Nil in production, so it costs
// one nil check on a path that already does filesystem work.
//
// It exists because the window it lets a test hit — Materialize's
// pre-acquire read of .rdk/lock has found it absent, but this run does not
// yet hold the transaction lock — lives, by construction, between two
// syscalls: a test that tried to reach it by racing goroutines would be
// timing-dependent, and a timing-dependent test for a timing bug is one that
// passes on the machine where the bug is worst. This makes that window
// reachable on demand instead. It is a package-level var rather than a field
// on osStore for the same reason as the other seams in this file: nothing
// outside this package can set it, and nothing inside sets it except a test.
//
// Fires inside Materialize's non-adopted path only (the adopted path has no
// equivalent pre-acquire read to confirm — see Materialize's doc comment)
// once that read has found .rdk/lock absent, and before the
// acquireLock(scratchApplyLock, ...) call that follows. A test uses it to run
// a concurrent HoldLock to completion from inside the callback — acquire the
// transaction lock, create .rdk/lock, release, return success — so that
// Materialize's own acquireLock right after still succeeds (the transaction
// lock is free again by then) and the post-acquire re-check this seam exists
// to exercise gets to run against a held lock that appeared in exactly this
// gap: the mirror, from the other side, of the race
// afterApplyLockHeldByHoldLock's test proves closed.
var afterHeldLockConfirmedAbsent func()

// afterHeldLockLinked is a test seam. Nil in production, so it costs one nil
// check on a path that already does filesystem work.
//
// It exists because the window it lets a test hit — HoldLock's held lock is
// already linked into place, but the transaction lock that protected its
// creation has not yet been released and the directory-entry sync that must
// run before HoldLock reports success has not yet run either — lives, by
// construction, between two syscalls: a test that tried to reach it by
// racing goroutines would be timing-dependent, and a timing-dependent test
// for a timing bug is one that passes on the machine where the bug is worst.
// This makes that window reachable on demand instead. It is a package-level
// var rather than a field on osStore for the same reason as the other seams
// in this file: nothing outside this package can set it, and nothing inside
// sets it except a test.
//
// Fires inside HoldLock immediately after acquireLock(scratchLock, ...) has
// returned success and before HoldLock's own explicit release of
// scratchApplyLock (which now runs before syncHeldLockDir, not after — see
// HoldLock's own doc comment for why the ordering changed). A test uses it
// the same way store_test.go's afterPublish tests use their own callback:
// making .rdk unreadable from inside the callback so the syncHeldLockDir
// call that eventually follows fails for a genuine OS reason — a real
// fsync-equivalent path that cannot succeed — rather than a stubbed one. The
// intervening release still succeeds even with .rdk unreadable this way:
// removing a directory entry needs search-and-write permission on the
// directory, not read, so chmodUnreadable's 0o300 leaves it able to run.
var afterHeldLockLinked func()

// afterHoldLockReleasedTransactionLock is a test seam. Nil in production, so
// it costs one nil check on a path that already does filesystem work.
//
// It exists to make an ordering assertion possible without racing goroutines:
// that HoldLock's directory-entry sync (syncHeldLockDir) runs only after the
// transaction lock it took to create the held lock has actually been
// removed from disk, not merely scheduled for removal by a deferred call
// that has not run yet. This is what closes the durability gap where a crash
// between an unsynced release and the filesystem's own next flush could
// restore .rdk/apply.lock even though rdk lock already reported success — see
// HoldLock's own doc comment for the full argument. It is a package-level
// var rather than a field on osStore for the same reason as the other seams
// in this file: nothing outside this package can set it, and nothing inside
// sets it except a test.
//
// Fires inside HoldLock immediately after its own explicit call to
// ReleaseLock has returned successfully and before syncHeldLockDir runs. A
// test uses it to check, from inside the callback, that scratchApplyLock is
// already gone from disk — proving the sync that runs next captures a
// directory state that no longer contains the transaction lock, rather than
// one that still does.
var afterHoldLockReleasedTransactionLock func()

// afterHeldLockRemoved is a test seam. Nil in production, so it costs one nil
// check on a path that already does filesystem work.
//
// It exists for the same reason as afterHeldLockLinked, mirrored for
// removal: the window between Unlock's Remove of scratchLock succeeding and
// the directory-entry sync that must run before Unlock reports success lives,
// by construction, between two syscalls, and a test that tried to reach it by
// racing goroutines would be timing-dependent for a timing bug — exactly the
// failure mode this file's other seams already avoid the same way. It is a
// package-level var rather than a field on osStore for the same reason as
// the rest: nothing outside this package can set it, and nothing inside sets
// it except a test.
//
// Fires inside Unlock immediately after s.root.Remove(scratchLock) has
// succeeded and before syncHeldLockDir runs. A test uses it the same way
// afterHeldLockLinked's own tests do: making .rdk unreadable from inside the
// callback so the syncHeldLockDir call that follows fails for a genuine OS
// reason rather than a stubbed one.
var afterHeldLockRemoved func()
