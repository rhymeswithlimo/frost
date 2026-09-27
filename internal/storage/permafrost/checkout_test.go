package permafrost

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// startTest starts a checkout and returns it with the redirect_uri and
// state the checkout page would be given.
func startTest(t *testing.T) (*Checkout, string, string) {
	t.Helper()
	c, err := StartCheckout("https://frost.test/checkout.html?ref=cli")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	u, err := url.Parse(c.URL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Host != "frost.test" || u.Path != "/checkout.html" || q.Get("ref") != "cli" {
		t.Fatalf("checkout url %s", c.URL)
	}
	if len(q.Get("state")) < 40 || !strings.HasPrefix(q.Get("redirect_uri"), "http://127.0.0.1:") {
		t.Fatalf("checkout url %s", c.URL)
	}
	return c, q.Get("redirect_uri"), q.Get("state")
}

// visit is the browser coming back, without following redirects.
func visit(t *testing.T, u string) int {
	t.Helper()
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := cl.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

func TestCheckoutGetsToken(t *testing.T) {
	c, back, state := startTest(t)
	// Stray requests don't count: favicons, the bare callback.
	root := strings.TrimSuffix(back, "/callback")
	visit(t, root+"/favicon.ico")
	visit(t, back)
	if got := visit(t, back+"?"+url.Values{"state": {state}, "token": {"pf_abc123"}}.Encode()); got != 200 {
		t.Errorf("callback answered %d", got)
	}
	token, err := c.Wait(context.Background())
	if err != nil || token != "pf_abc123" {
		t.Fatalf("Wait = %q, %v", token, err)
	}
	if _, err := http.Get(back); err == nil {
		t.Error("still listening after the key came back")
	}
}

func TestCheckoutStateMismatch(t *testing.T) {
	c, back, state := startTest(t)
	visit(t, back+"?"+url.Values{"state": {"someone-else"}, "token": {"pf_evil"}}.Encode())
	visit(t, back+"?"+url.Values{"state": {state}, "token": {"pf_good"}}.Encode())
	if token, err := c.Wait(context.Background()); err != nil || token != "pf_good" {
		t.Fatalf("Wait = %q, %v, want original checkout to remain usable", token, err)
	}
}

func TestCheckoutCancelled(t *testing.T) {
	c, back, state := startTest(t)
	visit(t, back+"?"+url.Values{"state": {state}, "error": {"cancelled"}}.Encode())
	if _, err := c.Wait(context.Background()); !errors.Is(err, ErrCheckoutCancelled) {
		t.Fatalf("Wait err = %v, want cancelled", err)
	}
}

func TestCheckoutTimeout(t *testing.T) {
	defer func(d time.Duration) { CheckoutTimeout = d }(CheckoutTimeout)
	CheckoutTimeout = 50 * time.Millisecond
	c, back, _ := startTest(t)
	if _, err := c.Wait(context.Background()); !errors.Is(err, ErrCheckoutTimeout) {
		t.Fatalf("Wait err = %v, want a timeout", err)
	}
	if _, err := http.Get(back); err == nil {
		t.Error("still listening after timing out")
	}
}

func TestUnauthorized(t *testing.T) {
	b, _ := newTest(t)
	b.token = "expired"
	_, err := b.Get(context.Background(), "frost.repo")
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

// TestCheckoutAgainstReference gets a key from the reference server, the
// way a browser would, and uses it.
func TestCheckoutAgainstReference(t *testing.T) {
	ref := &refServer{token: "new", pageSize: 10, objs: map[string][]byte{}}
	srv := httptest.NewServer(ref)
	defer srv.Close()
	c, err := StartCheckout(srv.URL + "/checkout")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(c.URL) // follows checkout back to the callback
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	token, err := c.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(srv.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Put(context.Background(), "frost.probe", []byte("ok")); err != nil {
		t.Fatalf("the new key doesn't work: %v", err)
	}
}
