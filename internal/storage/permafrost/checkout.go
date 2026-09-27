package permafrost

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// CheckoutURL is where people get an access key for the default server: a
// page on the frost website. A custom server has its own, at /checkout.
// Placeholder until the website's address is decided.
const CheckoutURL = "https://frost.example.com/checkout.html"

// CheckoutLink is the short address people type to get a key by hand, when
// the browser didn't open.
const CheckoutLink = "getfro.st/perma"

// CheckoutTimeout is how long a checkout waits for the browser to come back.
var CheckoutTimeout = 25 * time.Minute

var (
	// ErrCheckoutTimeout means nothing came back from the browser in time.
	ErrCheckoutTimeout = errors.New("checkout timed out")
	// ErrStateMismatch means the browser came back with someone else's state.
	ErrStateMismatch = errors.New("checkout state doesn't match")
	// ErrCheckoutCancelled means the checkout page said the person cancelled.
	ErrCheckoutCancelled = errors.New("checkout cancelled")
)

// Checkout is one go at getting a key in the browser. It listens on a loopback port for
// the checkout page to send the person back with their access key.
type Checkout struct {
	// URL is the checkout page, to open in the browser.
	URL string

	srv    *http.Server
	state  string
	result chan checkoutResult
	once   sync.Once
}

type checkoutResult struct {
	token string
	err   error
}

// StartCheckout starts listening for the checkout page at pageURL to send
// the person back with a key. Call Wait for the key, or Close to give up.
func StartCheckout(pageURL string) (*Checkout, error) {
	page, err := url.Parse(pageURL)
	if err != nil || page.Host == "" {
		return nil, fmt.Errorf("permafrost: invalid checkout url %q", pageURL)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	// 127.0.0.1 and not localhost, which can resolve to ::1.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("permafrost: can't listen for checkout: %w", err)
	}
	local := "http://" + ln.Addr().String()
	c := &Checkout{
		state:  base64.RawURLEncoding.EncodeToString(raw),
		result: make(chan checkoutResult, 1),
	}
	q := page.Query()
	q.Set("redirect_uri", local+"/callback")
	q.Set("state", c.state)
	page.RawQuery = q.Encode()
	c.URL = page.String()

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", c.callback)
	c.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go c.srv.Serve(ln)
	return c, nil
}

// callback is where the checkout page sends the browser. Only a request
// with a state counts: anything else, like a favicon, is ignored.
func (c *Checkout) callback(w http.ResponseWriter, r *http.Request) {
	state := r.FormValue("state")
	if state == "" {
		http.Error(w, "missing state", http.StatusBadRequest)
		return
	}
	token := strings.TrimSpace(r.FormValue("token"))
	var res checkoutResult
	switch {
	case subtle.ConstantTimeCompare([]byte(state), []byte(c.state)) != 1:
		res.err = ErrStateMismatch
		donePage(w, http.StatusBadRequest, "This doesn't match the checkout frost started.", "frost has stopped waiting. Go back to your terminal to try again.")
	case r.FormValue("error") != "":
		res.err = ErrCheckoutCancelled
		donePage(w, http.StatusOK, "Checkout cancelled.", "Go back to your terminal to try again or paste a key.")
	case token == "" || strings.ContainsAny(token, " \t\r\n"):
		res.err = errors.New("the checkout page didn't send back an access key")
		donePage(w, http.StatusBadRequest, "No access key came back.", "Go back to your terminal and paste your key from your Permafrost account.")
	default:
		res.token = token
		donePage(w, http.StatusOK, "You're all set.", "Your access key is with frost. You can close this tab and go back to your terminal.")
	}
	c.once.Do(func() { c.result <- res })
}

func donePage(w http.ResponseWriter, status int, title, msg string) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>frost</title>
<body style="margin:0;min-height:100vh;display:grid;place-items:center;background:#1926c4;color:#f2efe7;font:16px/1.5 ui-monospace,Menlo,Consolas,monospace">
<main style="max-width:32rem;padding:2rem;border:1px solid #f2efe7"><p style="margin:0 0 1rem;font-weight:bold">%s</p><p style="margin:0;color:#b1aea9">%s</p></main>`,
		html.EscapeString(title), html.EscapeString(msg))
}

// Wait waits for the browser to come back, for CheckoutTimeout at most, and
// returns the access key. The local server is shut down either way.
func (c *Checkout) Wait(ctx context.Context) (string, error) {
	defer c.Close()
	t := time.NewTimer(CheckoutTimeout)
	defer t.Stop()
	select {
	case res := <-c.result:
		return res.token, res.err
	case <-t.C:
		return "", ErrCheckoutTimeout
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Close stops listening. It lets the last page finish sending first.
func (c *Checkout) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c.srv.Shutdown(ctx)
}
