package update

import (
	"fmt"
	"os"
	"time"
)

// replace swaps staged in for exe. Windows won't overwrite or delete a
// running .exe but will rename it, so the old one moves aside first and
// Cleanup deletes it on a later run.
func replace(staged, exe string) error {
	old := exe + ".old"
	if err := os.Remove(old); err != nil && !os.IsNotExist(err) {
		old = fmt.Sprintf("%s.%d.old", exe, time.Now().UnixNano()) // still running
	}
	if err := retry(func() error { return os.Rename(exe, old) }); err != nil {
		return fmt.Errorf("moving the old binary aside: %w", err)
	}
	if err := retry(func() error { return os.Rename(staged, exe) }); err != nil {
		if rerr := retry(func() error { return os.Rename(old, exe) }); rerr != nil {
			return fmt.Errorf("installing the new binary: %w, and putting the old one back failed too: it's at %s", err, old)
		}
		return fmt.Errorf("installing the new binary: %w", err)
	}
	os.Remove(old) // fails while it's running, Cleanup gets it later
	return nil
}

// retry gives antivirus scanners, which briefly lock new files, a moment.
func retry(f func() error) error {
	var err error
	for i := range 5 {
		if err = f(); err == nil {
			return nil
		}
		time.Sleep(time.Duration(i+1) * 200 * time.Millisecond)
	}
	return err
}

func keepOwner(exe, staged string) {}
