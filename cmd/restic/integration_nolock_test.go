package main

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/restic/restic/internal/backend"
	"github.com/restic/restic/internal/errors"
	"github.com/restic/restic/internal/global"
	rtest "github.com/restic/restic/internal/test"
)

// wormBackend emulates write-once storage, e.g. a GCS bucket without delete
// permission: files can be created, but never removed. It additionally records
// whether a lock file was written, as such a lock could never be removed again.
type wormBackend struct {
	backend.Backend
	locksSaved *atomic.Int32
}

func (b *wormBackend) Save(ctx context.Context, h backend.Handle, rd backend.RewindReader) error {
	if h.Type == backend.LockFile {
		b.locksSaved.Add(1)
	}
	return b.Backend.Save(ctx, h, rd)
}

func (b *wormBackend) Remove(_ context.Context, h backend.Handle) error {
	return errors.Errorf("removing %v is not allowed on write-once storage", h)
}

// withWORMBackend returns a copy of gopts whose repository behaves like
// write-once storage, and a counter of the lock files written to it.
func withWORMBackend(gopts global.Options) (global.Options, *atomic.Int32) {
	locksSaved := &atomic.Int32{}
	inner := gopts.BackendTestHook
	gopts.BackendTestHook = func(r backend.Backend) (backend.Backend, error) {
		if inner != nil {
			var err error
			if r, err = inner(r); err != nil {
				return nil, err
			}
		}
		return &wormBackend{Backend: r, locksSaved: locksSaved}, nil
	}
	return gopts, locksSaved
}

// TestBackupWORMWritesLock is the counterpart to the --no-lock tests below and
// ensures that wormBackend detects lock files at all: without --no-lock, backup
// writes a lock file which then cannot be removed anymore.
func TestBackupWORMWritesLock(t *testing.T) {
	env, cleanup := withTestEnvironment(t)
	defer cleanup()
	testSetupBackupData(t, env)

	wormGopts, locksSaved := withWORMBackend(env.gopts)
	_ = testRunBackupAssumeFailure(t, "", []string{filepath.Join(env.testdata, "0", "0", "9")}, BackupOptions{}, wormGopts)
	rtest.Assert(t, locksSaved.Load() > 0, "expected backup without --no-lock to write a lock file")
	rtest.Equals(t, 1, len(testRunList(t, env.gopts, "locks")), "expected a stale lock file")
}

func TestBackupNoLockWORM(t *testing.T) {
	env, cleanup := withTestEnvironment(t)
	defer cleanup()
	testSetupBackupData(t, env)
	target := []string{filepath.Join(env.testdata, "0", "0", "9")}

	wormGopts, locksSaved := withWORMBackend(env.gopts)
	wormGopts.NoLock = true
	testRunBackup(t, "", target, BackupOptions{}, wormGopts)
	rtest.Equals(t, int32(0), locksSaved.Load(), "backup --no-lock wrote a lock file")

	// --dry-run must still not write anything, also not a snapshot
	testRunBackup(t, "", target, BackupOptions{DryRun: true}, wormGopts)
	rtest.Equals(t, int32(0), locksSaved.Load(), "backup --no-lock --dry-run wrote a lock file")

	testListSnapshots(t, env.gopts, 1)
	testRunCheck(t, env.gopts)
}

func TestKeyAddNoLockWORM(t *testing.T) {
	env, cleanup := withTestEnvironment(t)
	defer cleanup()
	testRunInit(t, env.gopts)
	// key add lists the keys more than once
	env.gopts.BackendTestHook = nil

	wormGopts, locksSaved := withWORMBackend(env.gopts)
	wormGopts.NoLock = true
	testRunKeyAddNewKey(t, "john's geheimnis", wormGopts)
	rtest.Equals(t, int32(0), locksSaved.Load(), "key add --no-lock wrote a lock file")

	rtest.Equals(t, 1, len(testRunKeyListOtherIDs(t, env.gopts)))
	env.gopts.Password = "john's geheimnis"
	testRunCheck(t, env.gopts)
}

func TestCopyNoLockWORM(t *testing.T) {
	env, cleanup := withTestEnvironment(t)
	defer cleanup()
	env2, cleanup2 := withTestEnvironment(t)
	defer cleanup2()

	testSetupBackupData(t, env)
	testRunBackup(t, "", []string{filepath.Join(env.testdata, "0", "0", "9")}, BackupOptions{}, env.gopts)
	testRunInit(t, env2.gopts)

	// testRunCopy takes all options except the destination repository from
	// the source options, thus the WORM wrapper applies to both repositories.
	wormGopts, locksSaved := withWORMBackend(env.gopts)
	wormGopts.NoLock = true
	testRunCopy(t, wormGopts, env2.gopts)
	rtest.Equals(t, int32(0), locksSaved.Load(), "copy --no-lock wrote a lock file")

	testListSnapshots(t, env2.gopts, 1)
	testRunCheck(t, env2.gopts)
}
