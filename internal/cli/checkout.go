package cli

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/rhymeswithlimo/frost/internal/config"
	"github.com/rhymeswithlimo/frost/internal/storage/permafrost"
)

// checkout opens the page for getting a Permafrost key in the browser, and
// returns an address to show in case it didn't open. wait returns the key
// once it comes back, and saves it to config.toml straight away, so quitting
// setup doesn't lose it. The key comes back whenever there is one, even if
// err says it couldn't be saved.
func checkout(ctx context.Context, s config.Storage) (page string, wait func() (string, error), err error) {
	open, page := permafrost.CheckoutURL, permafrost.CheckoutLink
	if s.Permafrost.URL != "" {
		open = strings.TrimSuffix(s.Permafrost.URL, "/") + "/checkout"
		page = strings.TrimPrefix(open, "https://")
	}
	co, err := permafrost.StartCheckout(open)
	if err != nil {
		return "", nil, err
	}
	openBrowser(co.URL) // if it fails, the screen shows page, to get a key and paste it
	return page, func() (string, error) {
		token, err := co.Wait(ctx)
		if err != nil {
			return "", explainCheckout(err)
		}
		if err := saveToken(token); err != nil {
			return token, fmt.Errorf("got your access key but couldn't save it yet (%w). It's saved when you finish setup", err)
		}
		return token, nil
	}, nil
}

// saveToken writes a Permafrost access key into config.toml, creating it if
// needed. A backend that's already set stays until setup finishes, so
// scheduled backups keep going where they went.
func saveToken(token string) error {
	cfg, err := config.LoadFile()
	if err != nil && !errors.Is(err, config.ErrNoConfig) {
		return err
	}
	cfg.Storage.Permafrost.Token = token
	if cfg.Storage.Backend == "" {
		cfg.Storage.Backend = "permafrost"
	}
	return config.Save(cfg)
}

func explainCheckout(err error) error {
	switch {
	case errors.Is(err, permafrost.ErrCheckoutTimeout):
		return fmt.Errorf("nothing came back from checkout within %d minutes, so frost stopped waiting. If you did pay, your access key is on the checkout page and in your Permafrost account", int(permafrost.CheckoutTimeout.Minutes()))
	case errors.Is(err, permafrost.ErrCheckoutCancelled):
		return errors.New("checkout was cancelled in the browser")
	}
	return err
}

// openBrowser opens url in the default browser. It's a variable so tests
// never open a real one.
var openBrowser = openDefaultBrowser

func openDefaultBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		// Not `cmd /c start`, which cuts the URL at the first &.
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
