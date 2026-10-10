// Package host embeds CLIProxyAPI behind AO's exact-session routing boundary.
package host

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
)

var ErrBusy = errors.New("a dropped account has a model request in flight")

// Route carries a hashed capability, never a provider credential.
type Route struct {
	TicketHash string `json:"ticket_hash"`
	Provider   string `json:"provider"`
	AuthID     string `json:"auth_id"`
}

// Routes admits session tickets and counts the requests in flight per account.
type Routes struct {
	mu       sync.Mutex
	path     string
	byTicket map[string]Route
	inflight map[string]int
}

// OpenRoutes starts from the restart cache when it is readable; AO's next push replaces it either way.
func OpenRoutes(path string) *Routes {
	r := &Routes{path: path, byTicket: map[string]Route{}, inflight: map[string]int{}}
	data, _ := os.ReadFile(path)
	_ = json.Unmarshal(data, &r.byTicket)
	return r
}
func TicketHash(ticket string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(ticket)))
}

// Apply replaces the table unless it drops an account that has a request in flight.
func (r *Routes) Apply(routes []Route, authIDs []string) error {
	next := make(map[string]Route, len(routes))
	for _, route := range routes {
		next[route.TicketHash] = route
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for id := range r.inflight {
		if !slices.Contains(authIDs, id) {
			return ErrBusy
		}
	}
	if maps.Equal(next, r.byTicket) {
		return nil
	}
	data, _ := json.Marshal(next)
	if err := writePrivate(r.path, data); err != nil {
		return err
	}
	r.byTicket = next
	return nil
}

// Acquire admits one request; the caller runs the returned release exactly once.
func (r *Routes) Acquire(ticket string) (Route, func(), bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	route, ok := r.byTicket[TicketHash(ticket)]
	if !ok || ticket == "" || route.AuthID == "" {
		return Route{}, nil, false
	}
	r.inflight[route.AuthID]++
	return route, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.inflight[route.AuthID]--; r.inflight[route.AuthID] == 0 {
			delete(r.inflight, route.AuthID)
		}
	}, true
}
func writePrivate(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".routes-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	_, err = f.Write(data)
	if err = errors.Join(err, f.Sync(), f.Close()); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
