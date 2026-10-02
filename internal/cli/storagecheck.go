package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/rhymeswithlimo/frost/internal/config"
	"github.com/rhymeswithlimo/frost/internal/crypto"
	"github.com/rhymeswithlimo/frost/internal/repo"
	"github.com/rhymeswithlimo/frost/internal/storage"
)

// checkStorageChange looks at where storage settings point before frost
// starts using them, so a typo or a half-moved folder doesn't quietly stop
// backups or start a second set of them. It refuses the change with an
// error, or with warnOnly (the file is already saved) it prints a warning.
// Credentials that change without changing where backups are aren't
// checked: rotating keys takes two settings, and the first would fail.
func checkStorageChange(ctx context.Context, out io.Writer, was, now config.Storage, warnOnly bool) error {
	problem := func(msg string) error {
		if warnOnly {
			fmt.Fprintln(out, caution("warning: ")+msg)
			return nil
		}
		return fmt.Errorf("%s\n\nNothing was saved. To change it without this check, use `frost config edit`", msg)
	}
	newB, err := newBackend(now)
	oldB, oldErr := newBackend(was)
	switch {
	case err != nil && oldErr != nil:
		return nil // not set up before either, so nothing stops working
	case err != nil:
		return problem("with that change, storage settings are incomplete: " + err.Error())
	case oldErr == nil && storage.Location(newB) == storage.Location(oldB):
		return nil
	}
	key, err := loadKey()
	if err != nil {
		return nil // nothing set up on this machine to keep track of
	}
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	k := loadKnown()
	where := newB.String()

	r, err := repo.Open(ctx, newB, key)
	switch {
	case errors.Is(err, repo.ErrNotInitialized):
		if hasBackupObjects(ctx, newB) {
			return problem(where + " has frost backups but no frost.repo. If you moved them, frost.repo didn't come along.")
		}
		msg := "There are no backups in " + where + "."
		if k.Shown != "" && k.Shown != where {
			msg += " Yours are in " + k.Shown + "."
		}
		return problem(msg + " To use them there, " + moveHint(now, where) + " first. To start a separate set of backups there, run `frost init`.")
	case errors.Is(err, repo.ErrWrongKey):
		return problem(where + " has backups made with a different key. To use them, run `frost key import` with their recovery phrase.")
	case err != nil:
		return problem("couldn't check " + where + ": " + explainConnect(err).Error())
	}
	ids, err := r.SnapshotIDs(ctx)
	if err != nil {
		return problem("couldn't check " + where + ": " + explainConnect(err).Error())
	}
	if len(ids) == 0 && oldErr == nil && hasSnapshots(ctx, oldB, key, r.Info.ID) {
		return problem(where + " has your frost.repo but none of your snapshots. Move chunks/, snapshots/ and trees/ there too.")
	}
	if k.RepoID != "" && r.Info.ID != k.RepoID {
		fmt.Fprintln(out, caution("note: ")+"those are different backups from the ones in "+k.Shown+". frost will show their snapshots instead. Yours stay where they are.")
	}
	return nil
}

// hasBackupObjects reports whether b holds frost snapshots or file lists.
// Chunks aren't listed: there can be millions.
func hasBackupObjects(ctx context.Context, b storage.Backend) bool {
	for _, prefix := range []string{"snapshots/", "trees/"} {
		if keys, err := b.List(ctx, prefix); err == nil && len(keys) > 0 {
			return true
		}
	}
	return false
}

// hasSnapshots reports whether b holds repository id, with snapshots in it.
func hasSnapshots(ctx context.Context, b storage.Backend, key *crypto.Key, id string) bool {
	r, err := repo.Open(ctx, b, key)
	if err != nil || r.Info.ID != id {
		return false
	}
	ids, err := r.SnapshotIDs(ctx)
	return err == nil && len(ids) > 0
}
