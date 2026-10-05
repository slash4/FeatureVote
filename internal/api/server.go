// Package api implements the FeatureVote HTTP API: public and participant
// JSON endpoints, the admin JSON API, the admin HTML page, the widget bundle
// and health checks, plus CORS, body limits, rate limiting and request logs.
package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"

	"github.com/slash4/featurevote/internal/config"
	"github.com/slash4/featurevote/internal/hosttoken"
	"github.com/slash4/featurevote/internal/store"
)

// maxBodyBytes caps every request body.
const maxBodyBytes = 16 << 10

// Options configures a Server.
type Options struct {
	Store  *store.Store
	Config config.Config
	// WidgetJS is the embedded widget bundle served at /widget.js.
	WidgetJS []byte
	// Logger defaults to slog.Default().
	Logger *slog.Logger
	// Now defaults to time.Now; tests inject a fake clock.
	Now func() time.Time
}

// Server holds the handler dependencies.
type Server struct {
	store      *store.Store
	cfg        config.Config
	log        *slog.Logger
	now        func() time.Time
	verifier   *hosttoken.Verifier
	voteLimit  *slidingWindow
	loginLimit *slidingWindow
	origins    map[string]bool
	widget     []byte
	widgetETag string
}

// NewServer builds a Server from opts.
func NewServer(opts Options) *Server {
	s := &Server{
		store:  opts.Store,
		cfg:    opts.Config,
		log:    opts.Logger,
		now:    opts.Now,
		widget: opts.WidgetJS,
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.now == nil {
		s.now = time.Now
	}
	s.verifier = &hosttoken.Verifier{
		Secret:   []byte(s.cfg.HostSecret),
		Issuer:   s.cfg.HostIssuer,
		Audience: s.cfg.Audience,
		Skew:     s.cfg.ClockSkew,
		Now:      s.now,
	}
	s.voteLimit = newSlidingWindow("vote", s.cfg.VoteLimitPerMinute, time.Minute, limiterMaxKeys, s.log)
	s.loginLimit = newSlidingWindow("admin", adminLoginPerMinute, time.Minute, limiterMaxKeys, s.log)
	s.origins = make(map[string]bool, len(s.cfg.AllowedOrigins))
	for _, o := range s.cfg.AllowedOrigins {
		s.origins[o] = true
	}
	sum := sha256.Sum256(s.widget)
	s.widgetETag = `"` + hex.EncodeToString(sum[:]) + `"`
	return s
}

// Handler returns the root HTTP handler with all middleware applied.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /widget.js", s.handleWidget)

	// Public.
	mux.HandleFunc("GET /v1/ideas", s.handleListIdeas)
	mux.HandleFunc("GET /v1/ideas/{id}", s.handleGetIdea)

	// Participant (host token).
	mux.HandleFunc("POST /v1/ideas", s.withToken(s.requireVoter(s.handleSubmitIdea)))
	mux.HandleFunc("PUT /v1/ideas/{id}/vote", s.withToken(s.requireVoter(s.voteRateLimited(s.handleCastVote))))
	mux.HandleFunc("DELETE /v1/ideas/{id}/vote", s.withToken(s.requireVoter(s.voteRateLimited(s.handleRemoveVote))))
	mux.HandleFunc("GET /v1/me", s.withToken(s.handleMe))
	mux.HandleFunc("GET /v1/me/votes", s.withToken(s.handleMyVotes))
	mux.HandleFunc("GET /v1/me/ideas", s.withToken(s.handleMyIdeas))

	// Admin JSON API.
	mux.HandleFunc("GET /v1/admin/ideas", s.adminAPI(s.handleAdminList))
	mux.HandleFunc("POST /v1/admin/ideas", s.adminAPI(s.handleAdminCreate))
	mux.HandleFunc("POST /v1/admin/ideas/{id}/approve", s.adminAPI(s.handleAdminApprove))
	mux.HandleFunc("POST /v1/admin/ideas/{id}/reject", s.adminAPI(s.handleAdminReject))
	mux.HandleFunc("POST /v1/admin/ideas/{id}/merge", s.adminAPI(s.handleAdminMerge))
	mux.HandleFunc("PUT /v1/admin/ideas/{id}/status", s.adminAPI(s.handleAdminStatus))
	mux.HandleFunc("PATCH /v1/admin/ideas/{id}", s.adminAPI(s.handleAdminEdit))
	mux.HandleFunc("DELETE /v1/admin/ideas/{id}", s.adminAPI(s.handleAdminDelete))
	mux.HandleFunc("DELETE /v1/admin/users/{sub}", s.adminAPI(s.handleAdminDeleteUser))

	// Unmatched /v1 routes get a JSON 404 instead of the mux's plain text.
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, codeNotFound, "no such endpoint")
	})

	// Admin HTML page.
	s.registerAdminPages(mux)

	var h http.Handler = mux
	h = s.cors(h)
	h = limitBody(h)
	h = commonHeaders(h)
	h = s.recoverer(h)
	h = s.logRequests(h)
	return h
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	w.Header().Set("Cache-Control", "no-store")
	if err := s.store.Ping(ctx); err != nil {
		s.log.Warn("healthz: database ping failed", "err", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "db_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleWidget(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "application/javascript; charset=utf-8")
	h.Set("Cache-Control", "public, max-age=300")
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("ETag", s.widgetETag)
	if etagMatches(r.Header.Get("If-None-Match"), s.widgetETag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Write(s.widget) //nolint:errcheck
}
