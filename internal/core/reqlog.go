package core

import (
	"cmp"
	"fmt"
	"github.com/dhis2/facility-hub/internal/logfile"
	"log/slog"
	"maps"
	"net/http"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// The request log (spec 13.1): which endpoints and parameters clients call, for
// /hub/ui/endpoints and requests.log.

// apiWords are 11-character API path words that look like UIDs.
var apiWords = map[string]bool{"loginConfig": true, "programType": true, "periodTypes": true, "lockExceptions": true}

var uidSegment = regexp.MustCompile(`/[a-zA-Z][a-zA-Z0-9]{10}(/|$)`)

type RequestLog struct {
	mu      sync.Mutex
	counts  map[string]int
	unsupp  map[string]int
	misses  map[string]int
	file    *logfile.File
	enabled bool
}

func NewRequestLog() *RequestLog {
	return &RequestLog{counts: map[string]int{}, unsupp: map[string]int{}, misses: map[string]int{}}
}

// Open starts appending one line per request to dir/requests.log.
func (l *RequestLog) Open(dir string) {
	// Capped like hub.log (internal/logfile): log_requests may stay on for months.
	f, err := logfile.Open(filepath.Join(dir, "requests.log"), 10<<20)
	if err != nil {
		slog.Warn("request log", "err", err)
		return
	}
	l.mu.Lock()
	l.file, l.enabled = f, true
	l.mu.Unlock()
}

// Template turns "/api/tracker/events/AbCdEfGhIjK" into "/api/tracker/events/{uid}".
func Template(path string) string {
	for {
		n := uidSegment.ReplaceAllStringFunc(path, func(seg string) string {
			if apiWords[strings.Trim(seg, "/")] { // "loginConfig" is a word, not a UID
				return seg
			}
			if strings.HasSuffix(seg, "/") {
				return "/{uid}/"
			}
			return "/{uid}"
		})
		if n == path {
			return n
		}
		path = n
	}
}

// Record logs method, path template, parameter names, client and how it was served.
func (l *RequestLog) Record(r *http.Request, client, servedBy string, status int) {
	var params []string
	for k := range r.URL.Query() {
		params = append(params, k)
	}
	slices.Sort(params)
	k := key(r, client)
	l.mu.Lock()
	defer l.mu.Unlock()
	bump(l.counts, k)
	if l.enabled {
		// Parameter names only: values can hold search terms (patient names).
		fmt.Fprintf(l.file, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n", time.Now().UTC().Format(time.RFC3339), client, r.Method,
			trunc(Template(r.URL.Path)), status, servedBy, trunc(strings.Join(params, ",")))
	}
}

// key is a request as the log and its page show it: method, path template, parameter
// names (values can hold search terms) and client.
func key(r *http.Request, client string) string {
	var params []string
	for k := range r.URL.Query() {
		params = append(params, k)
	}
	slices.Sort(params)
	return fmt.Sprintf("%s %s ?%s [%s]", trunc(r.Method), trunc(Template(r.URL.Path)), trunc(strings.Join(params, ",")), client)
}

// The request log's maps hold at most maxKeys keys of at most a few hundred bytes:
// anyone on the network can send requests with made-up paths (review R-06).
const (
	maxKeys    = 4000
	maxKeyPart = 200
	otherKey   = "other"
)

// bump counts k, or "other" once the map is full.
func bump(m map[string]int, k string) {
	if _, ok := m[k]; !ok && len(m) >= maxKeys {
		k = otherKey
	}
	m[k]++
}

func trunc(s string) string {
	if len(s) <= maxKeyPart {
		return s
	}
	return strings.ToValidUTF8(s[:maxKeyPart], "") + "..."
}

// Miss counts a read the hub answered "needs connection" offline: a screen nobody had
// opened online, an unsupported query. Since the hub started; the heartbeat carries
// the top ones (OfflineMisses).
func (l *RequestLog) Miss(r *http.Request) {
	k := key(r, ClientKind(r))
	l.mu.Lock()
	bump(l.misses, k)
	l.mu.Unlock()
}

// OfflineMisses returns the n most frequent misses, most first, and how many there
// were in all.
func (l *RequestLog) OfflineMisses(n int) (top []map[string]any, total int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	keys := slices.Collect(maps.Keys(l.misses))
	slices.SortFunc(keys, func(a, b string) int { return cmp.Or(cmp.Compare(l.misses[b], l.misses[a]), cmp.Compare(a, b)) })
	for i, k := range keys {
		total += l.misses[k]
		if i < n {
			top = append(top, map[string]any{"request": k, "count": l.misses[k]})
		}
	}
	return top, total
}

// Unsupported records an offline parameter the hub could not serve (spec 10.1).
func (l *RequestLog) Unsupported(path, param string) {
	slog.Warn("not supported offline", "path", path, "param", param)
	l.mu.Lock()
	bump(l.unsupp, trunc(Template(path))+" "+trunc(param))
	l.mu.Unlock()
}

func (l *RequestLog) Snapshot() (counts, unsupported map[string]int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	counts, unsupported = map[string]int{}, map[string]int{}
	for k, v := range l.counts {
		counts[k] = v
	}
	for k, v := range l.unsupp {
		unsupported[k] = v
	}
	return
}

// SafeQuery renders a query with the values of free-text parameters (filter, query)
// replaced, for logs.
func SafeQuery(q map[string][]string) string {
	var parts []string
	for k, vs := range q {
		for _, v := range vs {
			if k == "filter" || k == "query" {
				v = "***"
			}
			parts = append(parts, k+"="+v)
		}
	}
	slices.Sort(parts)
	return strings.Join(parts, "&")
}

// ClientKind guesses web vs Android from the User-Agent.
func ClientKind(r *http.Request) string {
	ua := r.Header.Get("User-Agent")
	switch {
	case strings.Contains(ua, "okhttp") || strings.Contains(ua, "Dhis2") || strings.Contains(ua, "Android"):
		return "android"
	case strings.Contains(ua, "Mozilla"):
		return "web"
	}
	return "other"
}

// Close closes the request log file.
func (l *RequestLog) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		l.file.Close()
		l.file = nil
	}
}

// Opened reports whether the request log file is open.
func (l *RequestLog) Opened() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file != nil
}
