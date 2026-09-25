package permafrost

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// refServer is a minimal Permafrost server written straight from
// docs/PERMAFROST.md. If the client and the doc drift apart, these tests
// break.
type refServer struct {
	token    string
	pageSize int

	mu   sync.Mutex
	objs map[string][]byte
	// failNext makes the next N requests return 503.
	failNext int
	requests int
}

var keyRE = regexp.MustCompile(`^[a-z0-9._/-]+$`)

func validKey(k string) bool {
	if len(k) > 1024 || !keyRE.MatchString(k) || strings.HasPrefix(k, "/") {
		return false
	}
	for _, seg := range strings.Split(k, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

func apiError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

func (s *refServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests++

	if s.failNext > 0 {
		s.failNext--
		w.Header().Set("Retry-After", "0")
		apiError(w, 503, "unavailable", "try again")
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+s.token {
		apiError(w, 401, "unauthorized", "bad token")
		return
	}

	if r.URL.Path == "/v1/objects" && r.Method == http.MethodGet {
		s.list(w, r)
		return
	}
	key, ok := strings.CutPrefix(r.URL.Path, "/v1/objects/")
	if !ok {
		apiError(w, 404, "not_found", "no such endpoint")
		return
	}
	if !validKey(key) {
		apiError(w, 400, "invalid_key", "bad key")
		return
	}

	switch r.Method {
	case http.MethodPut:
		body, _ := io.ReadAll(io.LimitReader(r.Body, 16<<20+1))
		if len(body) > 16<<20 {
			apiError(w, 413, "too_large", "too large")
			return
		}
		sum := sha256.Sum256(body)
		if r.Header.Get("X-Content-SHA256") != hex.EncodeToString(sum[:]) {
			apiError(w, 400, "checksum_mismatch", "checksum mismatch")
			return
		}
		s.objs[key] = body
		w.WriteHeader(204)
	case http.MethodGet:
		body, ok := s.objs[key]
		if !ok {
			apiError(w, 404, "not_found", "object not found")
			return
		}
		sum := sha256.Sum256(body)
		w.Header().Set("X-Content-SHA256", hex.EncodeToString(sum[:]))
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(body)
	case http.MethodDelete:
		delete(s.objs, key)
		w.WriteHeader(204)
	default:
		apiError(w, 405, "method_not_allowed", "method not allowed")
	}
}

func (s *refServer) list(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")
	var keys []string
	for k := range s.objs {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	start, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
	end := min(start+s.pageSize, len(keys))
	next := ""
	if end < len(keys) {
		next = strconv.Itoa(end)
	}
	json.NewEncoder(w).Encode(map[string]any{"keys": keys[start:end], "next_cursor": next})
}
