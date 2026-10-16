// Package core holds the hub's shared runtime: store, index, central client, runtime
// state from setup (spec 6.3), sessions and the per-user access lists.
package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dhis2/facility-hub/internal/access"
	"github.com/dhis2/facility-hub/internal/aggregate"
	"github.com/dhis2/facility-hub/internal/central"
	"github.com/dhis2/facility-hub/internal/compat"
	"github.com/dhis2/facility-hub/internal/config"
	"github.com/dhis2/facility-hub/internal/hubcrypto"
	"github.com/dhis2/facility-hub/internal/store"
	"github.com/dhis2/facility-hub/internal/tracker"
)

// Meta keys (key file, plaintext: needed while the store is locked; no patient or user data).
const (
	MetaHubID         = "hub_id"
	MetaMode          = "mode" // "setup" or "active"
	MetaCentralURL    = "central_url"
	MetaServerVersion = "server_version"
	// MetaServerTimeZone: central's time zone (serverTimeZoneId): DHIS2 gives and
	// expects times in it, without an offset.
	MetaServerTimeZone = "server_time_zone"
	MetaPublicKey      = "hub_public_key"
	MetaUpgrade        = "last_upgrade" // JSON of the last upgrade attempt
	MetaRetired        = "retired"      // "1" while the registry says another hub replaced this one
)

// Retired reports whether the registry marks this hub as replaced. A retired hub stops
// serving patient data from its store but keeps pushing its outbox. It never wipes
// itself: on 2.42 the registry namespace is writable by any user until protected, so
// a forged entry must cost at most offline service, and it ends when the entry is
// active again (REVIEW-2 F9).
func (h *Hub) Retired() bool { return h.Store.Meta(MetaRetired) == "1" }

const (
	ModeSetup  = "setup"
	ModeActive = "active"
)

// Runtime is the encrypted state written at setup and refreshed on sync.
type Runtime struct {
	Facility     string          `json:"facility"`
	FacilityName string          `json:"facilityName"`
	FacilityCode string          `json:"facilityCode"`
	FacilityPath string          `json:"facilityPath"` // "/root/.../facility"; empty before this field existed
	Catalog      *access.Catalog `json:"catalog"`
	DefaultAOC   string          `json:"defaultAttributeOptionCombo"`
	HubToken     string          `json:"hubToken"`
	HubTokenUID  string          `json:"hubTokenUid"`
	HubTokenExp  string          `json:"hubTokenExpires"` // RFC 3339; empty before this field existed
	SetupBy      string          `json:"setupBy"`
	// HubTokenOwner is whose token HubToken is: the setup user, until someone assigned
	// to the facility takes the hub's housekeeping over (TakeOverHubToken).
	HubTokenOwner string `json:"hubTokenOwner,omitempty"`
	SetupAt       string `json:"setupAt"`
	PrivateKey    []byte `json:"hubPrivateKey"` // ed25519 hub signing key
}

// Hub is shared by every package that serves requests or syncs.
type Hub struct {
	// tokenMu serialises changes of the hub token (renewal, take-over): each creates a
	// token over the network before saving it, and one must not overwrite the other.
	tokenMu  sync.Mutex
	zoneMu   sync.Mutex
	zone     *time.Location // central's, loaded from MetaServerTimeZone
	Cfg      config.Config
	Version  string
	Store    *store.Store
	Index    *tracker.Index
	Monitor  *central.Monitor
	Central  *central.Client
	Sessions *Sessions
	ReqLog   *RequestLog
	Started  time.Time

	mu     sync.RWMutex
	rt     *Runtime
	access map[int64]*access.List
	// aggAccess: each user's aggregate access (AggAccess), as access is theirs.
	aggAccess map[int64]*aggregate.Access

	SyncNow   chan struct{}
	Upgrading atomic.Bool
	Stats     SyncStats

	// localWrites is held shared by every write to the store that only the hub holds
	// (offline imports, reserved values handed out) and exclusively to freeze them.
	localWrites sync.RWMutex
	hosts       hostCache
}

// BeginLocalWrite reserves the store for a write that exists only on the hub. It
// returns false while an upgrade copies the store: a write accepted then would land
// in the store the upgrade throws away. Call EndLocalWrite when it returns true.
func (h *Hub) BeginLocalWrite() bool {
	h.localWrites.RLock()
	if h.Upgrading.Load() {
		h.localWrites.RUnlock()
		return false
	}
	return true
}

func (h *Hub) EndLocalWrite() { h.localWrites.RUnlock() }

// FreezeLocalWrites waits for local writes in flight and refuses new ones until
// ThawLocalWrites.
func (h *Hub) FreezeLocalWrites() {
	h.localWrites.Lock()
	h.Upgrading.Store(true)
	h.localWrites.Unlock()
}

func (h *Hub) ThawLocalWrites() { h.Upgrading.Store(false) }

// StuckOutbox counts pending outbox entries that cannot be pushed until their author
// logs in online again (central refused their token). Such an entry may never push,
// for instance when the author has left (REVIEW-2 F3).
func (h *Hub) StuckOutbox() int {
	entries, err := h.Store.PendingOutbox()
	if err != nil {
		return 0
	}
	blocked := map[int64]bool{}
	n := 0
	for _, e := range entries {
		b, seen := blocked[e.UserID]
		if !seen {
			u, _ := h.Store.UserByID(e.UserID)
			b = u == nil || u.NeedsLogin
			blocked[e.UserID] = b
		}
		if b {
			n++
		}
	}
	return n
}

// OutboxWaiting reports whether the outbox holds entries the next push can send: any
// pending entry whose author does not need an online login first. While it does, the
// hub answers tracker requests itself even when central is reachable (PassThrough).
func (h *Hub) OutboxWaiting() bool {
	if !h.Store.Unlocked() {
		return false
	}
	for _, id := range h.Store.PendingAuthors() {
		if u, _ := h.Store.UserByID(id); u != nil && !u.NeedsLogin {
			return true
		}
	}
	return false
}

// PassThrough reports whether tracker requests go to central: it is reachable, and the
// outbox has nothing waiting to be pushed. Until the outbox is pushed after an outage,
// central lacks what was written offline: reads there would not show it, and a write
// that refers to it would fail or overtake it.
func (h *Hub) PassThrough() bool { return h.Online() && !h.OutboxWaiting() }

// MarkOffline flags an answer the hub gave itself because central is unreachable,
// for clients that want to show it (FINDINGS: offline search scope).
func MarkOffline(w http.ResponseWriter) { w.Header().Set("X-Hub-Mode", "offline") }

// UpgradingError answers a local write refused during an upgrade.
func (h *Hub) UpgradingError(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "120")
	h.WebError(w, http.StatusServiceUnavailable, "the hub is upgrading; try again in a few minutes", "")
}

// SyncStats is what /hub/status and the heartbeat report.
type SyncStats struct {
	mu       sync.Mutex
	LastPush string
	LastPull string
	LastSync string
	LastErr  string
	// CycleStart is when the running or last sync cycle began; Syncing whether one runs.
	CycleStart string
	Syncing    bool
}

func (s *SyncStats) Set(fn func(*SyncStats)) {
	s.mu.Lock()
	fn(s)
	s.mu.Unlock()
}

func (s *SyncStats) Get() (lastPush, lastPull, lastSync, lastErr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.LastPush, s.LastPull, s.LastSync, s.LastErr
}

// Cycle returns when the running or last sync cycle began, and whether one runs.
func (s *SyncStats) Cycle() (start string, running bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.CycleStart, s.Syncing
}

// LastPushPull returns the last push and pull times, surviving restarts.
func (h *Hub) LastPushPull() (push, pull string) {
	push, pull, _, _ = h.Stats.Get()
	if push == "" {
		push = h.Store.Private("last_push")
	}
	if pull == "" {
		pull = h.Store.Private("last_pull")
	}
	return
}

func New(cfg config.Config, version string, st *store.Store) *Hub {
	mon := central.NewMonitor(cfg.PingInterval, cfg.PingTimeout)
	h := &Hub{Cfg: cfg, Version: version, Store: st, Index: tracker.NewStoreIndex(st), Monitor: mon,
		Sessions: NewSessions(), ReqLog: NewRequestLog(), Started: time.Now(), access: map[int64]*access.List{},
		aggAccess: map[int64]*aggregate.Access{},
		SyncNow:   make(chan struct{}, 1)}
	h.Sessions.Idle, h.Sessions.Max = cfg.SessionIdle, cfg.SessionMax
	base := st.Meta(MetaCentralURL)
	if base == "" {
		base = cfg.CentralURL
	}
	h.Central = central.NewClient(base, mon)
	return h
}

func (h *Hub) HubID() string { return h.Store.Meta(MetaHubID) }
func (h *Hub) Mode() string  { return h.Store.Meta(MetaMode) }
func (h *Hub) Online() bool  { return h.Monitor.Online() }
func (h *Hub) TriggerSync() {
	select {
	case h.SyncNow <- struct{}{}:
	default:
	}
}
func (h *Hub) ServerVersion() string { return h.Store.Meta(MetaServerVersion) }

// CentralTime is t as central writes times: in central's time zone, without an
// offset. Records the hub creates offline carry it, so they sort and compare with
// central's own (UTC until central's zone is known, or if it cannot be loaded).
func (h *Hub) CentralTime(t time.Time) string {
	return tracker.DHIS2Time(t.In(h.centralZone()))
}

// CentralNow is the current time in central's time zone.
func (h *Hub) CentralNow() time.Time { return time.Now().In(h.centralZone()) }

func (h *Hub) centralZone() *time.Location {
	name := h.Store.Meta(MetaServerTimeZone)
	if name == "" {
		return time.UTC
	}
	h.zoneMu.Lock()
	defer h.zoneMu.Unlock()
	if h.zone == nil || h.zone.String() != name {
		loc, err := time.LoadLocation(name)
		if err != nil {
			return time.UTC
		}
		h.zone = loc
	}
	return h.zone
}

// API returns the version-specific handling for central's version, or nil when this
// hub release does not support it ("hub needs upgrade", spec 12a).
func (h *Hub) API() compat.API { return compat.For(h.ServerVersion()) }

// Runtime returns the decrypted runtime state, or nil while locked or in setup.
func (h *Hub) Runtime() *Runtime {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.rt
}

func (h *Hub) Facility() string {
	if rt := h.Runtime(); rt != nil {
		return rt.Facility
	}
	return ""
}

// FacilityPath is the facility's org unit path, or "/<facility>" until the first sync
// has fetched it.
func (h *Hub) FacilityPath() string {
	rt := h.Runtime()
	switch {
	case rt == nil || rt.Facility == "":
		return ""
	case rt.FacilityPath != "":
		return rt.FacilityPath
	}
	return "/" + rt.Facility
}

func (h *Hub) Catalog() *access.Catalog {
	if rt := h.Runtime(); rt != nil && rt.Catalog != nil {
		return rt.Catalog
	}
	return &access.Catalog{Programs: map[string]*access.ProgramMeta{}, StageProgram: map[string]string{}}
}

// SaveRuntime persists and installs a new runtime state.
func (h *Hub) SaveRuntime(rt *Runtime) error {
	if rt.Catalog != nil {
		rt.Catalog.Index()
	}
	b, err := json.Marshal(rt)
	if err != nil {
		return err
	}
	if err := h.Store.SetState("runtime", b); err != nil {
		return err
	}
	h.mu.Lock()
	h.rt = rt
	h.mu.Unlock()
	return nil
}

// UpdateRuntime applies fn to a copy of the runtime state and saves it.
func (h *Hub) UpdateRuntime(fn func(*Runtime)) error {
	cur := h.Runtime()
	if cur == nil {
		return store.ErrLocked
	}
	n := *cur
	fn(&n)
	return h.SaveRuntime(&n)
}

// Unlock installs the data key, loads runtime state and builds the index (spec 9.3).
func (h *Hub) Unlock(dk hubcrypto.Key) error {
	if err := h.Store.SetDK(dk); err != nil {
		return err
	}
	b, err := h.Store.State("runtime")
	if err != nil {
		h.Store.SetDK(nil)
		return err
	}
	if b != nil {
		rt := &Runtime{}
		if err := json.Unmarshal(b, rt); err != nil {
			return err
		}
		if rt.Catalog != nil {
			rt.Catalog.Index()
		}
		h.mu.Lock()
		h.rt = rt
		h.mu.Unlock()
	}
	t := time.Now()
	if err := h.Index.Load(h.Store); err != nil {
		return fmt.Errorf("build index: %w", err)
	}
	slog.Info("store unlocked", "objects", h.Index.Len(), "index_ms", time.Since(t).Milliseconds())
	h.TriggerSync()
	return nil
}

// Lock forgets the data key and all decrypted state.
func (h *Hub) Lock() {
	h.Store.SetDK(nil)
	h.Index.Clear()
	h.mu.Lock()
	h.rt = nil
	h.access = map[int64]*access.List{}
	h.aggAccess = map[int64]*aggregate.Access{}
	h.mu.Unlock()
	h.Sessions.Clear()
}

// Access returns the stored access list for a user (nil if none or locked).
func (h *Hub) Access(userID int64) *access.List {
	h.mu.RLock()
	l := h.access[userID]
	h.mu.RUnlock()
	if l != nil {
		return l
	}
	u, err := h.Store.UserByID(userID)
	if err != nil || u == nil {
		return nil
	}
	b, err := h.Store.Access(u)
	if err != nil {
		return nil
	}
	l, _ = access.Parse(b)
	if l != nil {
		h.mu.Lock()
		h.access[userID] = l
		h.mu.Unlock()
	}
	return l
}

func (h *Hub) SetAccess(userID int64, l *access.List) error {
	b, _ := json.Marshal(l)
	if err := h.Store.SetAccess(userID, b); err != nil {
		return err
	}
	h.mu.Lock()
	h.access[userID] = l
	h.mu.Unlock()
	return nil
}

// Viewer builds the visibility context for a user.
func (h *Hub) Viewer(userID int64) *tracker.Viewer {
	acc := h.Access(userID)
	var path string
	if rt := h.Runtime(); rt != nil {
		path = rt.FacilityPath
	}
	return &tracker.Viewer{UserID: userID, Access: acc, Facility: h.Facility(), Catalog: h.Catalog(),
		AccessibleAsFacility: h.Cfg.OfflineSearchFacility, SearchScope: path != "" && acc.SearchesIn(path)}
}

// UpstreamAuth is how calls to central authenticate for a stored user: their PAT, or
// their stored password where PAT creation was not possible (spec 7.1 fallback).
func (h *Hub) UpstreamAuth(userID int64) (central.Auth, error) {
	u, err := h.Store.UserByID(userID)
	if err != nil || u == nil {
		return central.Auth{}, fmt.Errorf("unknown user %d", userID)
	}
	if pat, err := h.Store.PAT(u); err != nil {
		return central.Auth{}, err
	} else if pat != "" {
		return central.Auth{PAT: pat}, nil
	}
	if pw, err := h.Store.Password(u); err != nil {
		return central.Auth{}, err
	} else if pw != "" {
		return central.Auth{Username: u.Username, Password: pw}, nil
	}
	return central.Auth{}, errors.New("no credentials for " + u.Username)
}

// HubAuth is the hub-level token used for issue reports, heartbeat and metadata
// (spec 11.2, the one exception to per-user tokens).
func (h *Hub) HubAuth() central.Auth {
	if rt := h.Runtime(); rt != nil {
		return central.Auth{PAT: rt.HubToken}
	}
	return central.Auth{}
}

// WriteJSON writes v as JSON with status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json;charset=UTF-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// WebError writes an error in the target version's format (falls back to 2.42).
func (h *Hub) WebError(w http.ResponseWriter, status int, msg, code string) {
	api := h.API()
	if api == nil {
		api = compat.For("2.42")
	}
	WriteJSON(w, status, api.WebMessage(status, msg, code))
}

// NeedsConnection is the fail-closed answer (spec 2).
func (h *Hub) NeedsConnection(w http.ResponseWriter, what string) {
	h.WebError(w, http.StatusServiceUnavailable, "needs connection: "+what, "")
}

// Ctx returns a context for background calls to central.
func Ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 2*time.Minute)
}
