package cli

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/minio/minio-go/v7"

	"github.com/rhymeswithlimo/frost/internal/config"
	"github.com/rhymeswithlimo/frost/internal/crypto"
	"github.com/rhymeswithlimo/frost/internal/crypto/bip39"
	"github.com/rhymeswithlimo/frost/internal/repo"
	"github.com/rhymeswithlimo/frost/internal/storage"
	"github.com/rhymeswithlimo/frost/internal/storage/permafrost"
	"github.com/rhymeswithlimo/frost/internal/tui"
)

// connectTimeout caps how long setup waits on storage before giving up.
const connectTimeout = 45 * time.Second

// connect checks the storage in s is reachable and writable, and works out
// what's there relative to local, the key on this machine (nil if none).
// Errors come back in plain words.
func connect(ctx context.Context, s config.Storage, local *crypto.Key) (storage.Backend, tui.RepoState, error) {
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	b, err := newBackend(s)
	if err != nil {
		return nil, 0, err
	}
	if err := probe(ctx, b); err != nil {
		return nil, 0, explainConnect(err)
	}
	if local != nil {
		_, err := repo.Open(ctx, b, local)
		switch {
		case err == nil:
			return b, tui.RepoLocalOK, nil
		case errors.Is(err, repo.ErrNotInitialized):
			return b, tui.RepoNew, nil
		case errors.Is(err, repo.ErrWrongKey):
			return b, tui.RepoLocalWrong, nil
		}
		return nil, 0, explainConnect(err)
	}
	exists, err := repo.Exists(ctx, b)
	if err != nil {
		return nil, 0, explainConnect(err)
	}
	if exists {
		return b, tui.RepoNeedsPhrase, nil
	}
	return b, tui.RepoNew, nil
}

// probe checks the backend is reachable and writable.
func probe(ctx context.Context, b storage.Backend) error {
	const k = "frost.probe"
	if err := b.Put(ctx, k, []byte("ok")); err != nil {
		return err
	}
	if _, err := b.Get(ctx, k); err != nil {
		return err
	}
	return b.Delete(ctx, k)
}

// unlock checks phrase is a valid recovery phrase that opens the repository
// in s.
func unlock(ctx context.Context, s config.Storage, phrase string) (*crypto.Key, error) {
	key, err := phraseKey(phrase)
	if err != nil {
		return nil, err
	}
	b, err := newBackend(s)
	if err != nil {
		return nil, err
	}
	return key, opensRepo(ctx, b, key)
}

// opensRepo checks key opens the repository in b.
func opensRepo(ctx context.Context, b storage.Backend, key *crypto.Key) error {
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	_, err := repo.Open(ctx, b, key)
	switch {
	case errors.Is(err, repo.ErrWrongKey):
		return errors.New("that's a valid phrase, but not the one for these backups")
	case err != nil:
		return explainConnect(err)
	}
	return nil
}

// explainConnect turns a storage error into something a person can act on.
// A *tui.ConnectError says which answer caused it. Anything it doesn't
// recognise comes back unchanged.
func explainConnect(err error) error {
	about := func(what, msg string) error { return &tui.ConnectError{About: what, Msg: msg} }
	var api *permafrost.APIError
	if errors.As(err, &api) {
		switch api.Status {
		case 401:
			return about("key", "Permafrost didn't accept that access key. Check you copied all of it.")
		case 403:
			return about("key", "That access key can't store backups. Check its permissions in your Permafrost account.")
		case 507:
			return errors.New("your Permafrost storage is full")
		}
	}
	var s3err minio.ErrorResponse
	if errors.As(err, &s3err) {
		switch s3err.Code {
		case "InvalidAccessKeyId":
			return about("key", "That access key ID wasn't recognised. Check you copied all of it.")
		case "SignatureDoesNotMatch":
			return about("secret", "The secret key doesn't match the access key ID. Check you copied all of it.")
		case "NoSuchBucket":
			return about("bucket", "There's no bucket with that name. Check the name, or create the bucket first.")
		case "AccessDenied":
			return about("key", "That key can't read and write this bucket. Give it read and write access to the bucket.")
		case "AuthorizationHeaderMalformed", "InvalidRegion", "PermanentRedirect":
			return about("address", "The bucket is in a different region. Check the region or endpoint.")
		}
	}
	var dns *net.DNSError
	var cert x509.UnknownAuthorityError
	var host x509.HostnameError
	switch {
	case errors.As(err, &dns):
		return about("address", "Can't find "+dns.Name+". Check the address and your internet connection.")
	case errors.Is(err, syscall.ECONNREFUSED):
		return about("address", "Nothing answered at that address. Check it's right.")
	case errors.As(err, &cert), errors.As(err, &host):
		return about("address", "The server's certificate isn't valid for that address.")
	case errors.Is(err, context.DeadlineExceeded):
		return errors.New("the storage didn't answer in time, check your internet connection and try again")
	}
	return err
}

// phraseKey turns a typed recovery phrase into a key, with errors that say
// what's wrong with it.
func phraseKey(phrase string) (*crypto.Key, error) {
	words := strings.Fields(strings.ToLower(phrase))
	if len(words) != 24 {
		return nil, fmt.Errorf("that's %d words, a recovery phrase has 24", len(words))
	}
	for i, w := range words {
		if !slices.Contains(bip39.Words, w) {
			return nil, fmt.Errorf("word %d, %q, isn't a recovery phrase word. Check its spelling", i+1, w)
		}
	}
	key, err := crypto.KeyFromPhrase(strings.Join(words, " "))
	if errors.Is(err, bip39.ErrChecksum) {
		return nil, errors.New("all the words are real, but they don't make a valid phrase. Check their order and spelling")
	}
	return key, err
}
