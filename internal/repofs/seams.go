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
