package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/slash4/featurevote/internal/store"
)

// adminTokenOK compares a presented admin token with FV_ADMIN_TOKEN in
// constant time (hashing first so the length is not leaked either).
func (s *Server) adminTokenOK(tok string) bool {
	a := sha256.Sum256([]byte(tok))
	b := sha256.Sum256([]byte(s.cfg.AdminToken))
	return tok != "" && subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

// adminAPI requires Authorization: Bearer $FV_ADMIN_TOKEN.
//
// Failed attempts share the per-IP bucket of /admin/login (5 per minute), so
// the API is not a side door for guessing the token. Successful requests are
// not counted: a slot is reserved before the check (concurrent guesses cannot
// overshoot) and given back when the token matches. Once the bucket is full,
// every request from that IP gets 429 until it drains, the right token
// included; otherwise 200-vs-429 would still tell a guesser when it hit.
func (s *Server) adminAPI(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip, now := s.clientIP(r), s.now()
		if ok, retry := s.loginLimit.allow(ip, now); !ok {
			s.log.Warn("admin api throttled", "ip", ip)
			w.Header().Set("Retry-After", retryAfterSeconds(retry))
			writeError(w, http.StatusTooManyRequests, codeRateLimited, "too many failed admin token attempts; retry later")
			return
		}
		scheme, tok, _ := strings.Cut(r.Header.Get("Authorization"), " ")
		if !strings.EqualFold(scheme, "Bearer") || !s.adminTokenOK(strings.TrimSpace(tok)) {
			s.log.Warn("admin api token rejected", "ip", ip)
			writeError(w, http.StatusUnauthorized, codeUnauthorized, "admin token required")
			return
		}
		s.loginLimit.undo(ip, now)
		next(w, r)
	}
}

// adminStoreError maps store errors for admin endpoints; true if handled.
func (s *Server) adminStoreError(w http.ResponseWriter, r *http.Request, err error) bool {
	var ce *store.ConflictError
	switch {
	case err == nil:
		return false
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "idea not found")
	case errors.As(err, &ce):
		writeError(w, http.StatusBadRequest, codeInvalidInput, ce.Msg)
	default:
		s.internalError(w, r, err)
	}
	return true
}

func (s *Server) handleAdminList(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("moderation_state")
	if state == "" {
		state = store.ModPending
	}
	if state != "all" && !store.ValidModerationState(state) {
		writeError(w, http.StatusBadRequest, codeInvalidInput, "moderation_state must be pending, approved, rejected, merged or all")
		return
	}
	ideas, err := s.store.ListByModeration(r.Context(), state)
	if s.adminStoreError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ideas": mapSlice(ideas, adminIdea)})
}

func (s *Server) handleAdminCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title  string `json:"title"`
		Body   string `json:"body"`
		Status string `json:"status"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	title, msg := cleanTitle(in.Title)
	if msg == "" {
		in.Body, msg = cleanBody(in.Body)
	}
	if in.Status == "" {
		in.Status = store.StatusUnderReview
	}
	if msg == "" && !store.ValidStatus(in.Status) {
		msg = "unknown status"
	}
	if msg != "" {
		writeError(w, http.StatusBadRequest, codeInvalidInput, msg)
		return
	}
	idea, err := s.store.CreateApproved(r.Context(), title, in.Body, in.Status)
	if s.adminStoreError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, adminIdea(idea))
}

func (s *Server) adminIDAction(w http.ResponseWriter, r *http.Request, f func(id int64) (store.Idea, error)) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusNotFound, codeNotFound, "idea not found")
		return
	}
	idea, err := f(id)
	if s.adminStoreError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, adminIdea(idea))
}

func (s *Server) handleAdminApprove(w http.ResponseWriter, r *http.Request) {
	s.adminIDAction(w, r, func(id int64) (store.Idea, error) { return s.store.Approve(r.Context(), id) })
}

func (s *Server) handleAdminReject(w http.ResponseWriter, r *http.Request) {
	s.adminIDAction(w, r, func(id int64) (store.Idea, error) { return s.store.Reject(r.Context(), id) })
}

func (s *Server) handleAdminMerge(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusNotFound, codeNotFound, "idea not found")
		return
	}
	var in struct {
		IntoID int64 `json:"into_id"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.IntoID <= 0 {
		writeError(w, http.StatusBadRequest, codeInvalidInput, "into_id is required")
		return
	}
	res, err := s.store.Merge(r.Context(), id, in.IntoID)
	if s.adminStoreError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"idea": adminIdea(res.Target), "moved": res.Moved, "dropped": res.Dropped})
}

func (s *Server) handleAdminStatus(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Status string `json:"status"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if !store.ValidStatus(in.Status) {
		writeError(w, http.StatusBadRequest, codeInvalidInput, "unknown status")
		return
	}
	s.adminIDAction(w, r, func(id int64) (store.Idea, error) { return s.store.SetStatus(r.Context(), id, in.Status) })
}

func (s *Server) handleAdminEdit(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title *string `json:"title"`
		Body  *string `json:"body"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Title != nil {
		t, msg := cleanTitle(*in.Title)
		if msg != "" {
			writeError(w, http.StatusBadRequest, codeInvalidInput, msg)
			return
		}
		in.Title = &t
	}
	if in.Body != nil {
		b, msg := cleanBody(*in.Body)
		if msg != "" {
			writeError(w, http.StatusBadRequest, codeInvalidInput, msg)
			return
		}
		in.Body = &b
	}
	s.adminIDAction(w, r, func(id int64) (store.Idea, error) { return s.store.Edit(r.Context(), id, in.Title, in.Body) })
}

func (s *Server) handleAdminDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusNotFound, codeNotFound, "idea not found")
		return
	}
	cascade := false
	if v := r.URL.Query().Get("cascade"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidInput, "cascade must be true or false")
			return
		}
		cascade = b
	}
	err := s.store.Delete(r.Context(), id, cascade)
	var hm *store.HasMergedError
	if errors.As(err, &hm) {
		writeError(w, http.StatusConflict, codeHasMergedIdeas, fmt.Sprintf(
			"idea %d has %d merged idea(s); merge them elsewhere first or retry with ?cascade=true to delete them too",
			hm.ID, hm.Merged))
		return
	}
	if s.adminStoreError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAdminDeleteUser(w http.ResponseWriter, r *http.Request) {
	sub := r.PathValue("sub")
	if sub == "" {
		writeError(w, http.StatusBadRequest, codeInvalidInput, "sub is required")
		return
	}
	votes, ideas, err := s.store.DeleteUserData(r.Context(), sub)
	if s.adminStoreError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"votes_deleted": votes, "ideas_anonymised": ideas})
}
