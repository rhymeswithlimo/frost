// Package desktop opens URLs and folders in the user's desktop apps: the
// default browser, and Finder, Explorer or the Linux file manager. It also
// shows the system's folder picker.
package desktop

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"

	"github.com/ncruces/zenity"
)

// Open opens a URL in the default browser, or a directory in the file
// manager. It doesn't wait for the app to exit.
func Open(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "windows":
		// Not `cmd /c start`, which cuts a URL at the first &.
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	return start(cmd)
}

// Reveal shows a file selected in its folder. Where the file manager can't
// select it, the folder is opened instead.
func Reveal(path string) error { return reveal(path) }

// Available reports whether there's a desktop to open things on. Over SSH
// there isn't one here: on macOS `open` would pop a window on the remote
// machine's screen instead.
func Available() bool {
	if os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_TTY") != "" {
		return false
	}
	switch runtime.GOOS {
	case "darwin", "windows":
		return true
	}
	return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
}

func start(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// ErrCanceled is returned when the folder picker is closed without a choice.
var ErrCanceled = errors.New("no folder chosen")

// CanPick reports whether PickFolder can show a picker here. On Linux it
// needs zenity, qarma or matedialog installed.
func CanPick() bool { return Available() && zenity.IsAvailable() }

// PickFolder shows the system's folder picker, starting in start, and
// returns the chosen folder. Cancelling ctx closes the picker.
func PickFolder(ctx context.Context, title, start string) (string, error) {
	opts := []zenity.Option{zenity.Context(ctx), zenity.Directory(), zenity.Title(title)}
	if start != "" {
		opts = append(opts, zenity.Filename(start))
	}
	dir, err := zenity.SelectFile(opts...)
	if errors.Is(err, zenity.ErrCanceled) {
		return "", ErrCanceled
	}
	return dir, err
}
