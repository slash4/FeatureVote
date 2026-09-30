package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/slash4/featurevote/internal/hosttoken"
	"github.com/slash4/featurevote/internal/store"
)

func (s *Server) handleListIdeas(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p := store.ListParams{Sort: "top", Limit: 50}
	if v := q.Get("sort"); v != "" {
		if v != "top" && v != "new" {
			writeError(w, http.StatusBadRequest, codeInvalidInput, "sort must be top or new")
			return
		}
		p.Sort = v
	}
	if v := q.Get("status"); v != "" {
		if !store.ValidStatus(v) {
			writeError(w, http.StatusBadRequest, codeInvalidInput, "unknown status")
			return
		}
		p.Status = v
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			writeError(w, http.StatusBadRequest, codeInvalidInput, "limit must be 1 to 100")
			return
		}
		p.Limit = n
	}
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, codeInvalidInput, "offset must be a non-negative integer")
			return
		}
		p.Offset = n
	}
	ideas, total, err := s.store.ListApproved(r.Context(), p)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ideas": mapSlice(ideas, publicIdea), "total": total})
}

func (s *Server) handleGetIdea(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusNotFound, codeNotFound, "idea not found")
		return
	}
	idea, err := s.store.GetIdea(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && idea.ModerationState != store.ModApproved) {
		writeError(w, http.StatusNotFound, codeNotFound, "idea not found")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, publicIdea(idea))
}

// --- participant endpoints ---

type claimsKey struct{}

func claimsFrom(r *http.Request) hosttoken.Claims {
	c, _ := r.Context().Value(claimsKey{}).(hosttoken.Claims)
	return c
}

// withToken requires a valid host token (Authorization: Bearer <jwt>).
func (s *Server) withToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		scheme, tok, found := strings.Cut(auth, " ")
		if auth == "" || !found || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(tok) == "" {
			writeError(w, http.StatusUnauthorized, codeUnauthorized, "missing bearer token")
			return
		}
		claims, err := s.verifier.Verify(strings.TrimSpace(tok))
		if err != nil {
			if errors.Is(err, hosttoken.ErrExpired) {
				writeError(w, http.StatusUnauthorized, codeTokenExpired, "host token expired")
			} else {
				writeError(w, http.StatusUnauthorized, codeInvalidToken, "host token invalid")
			}
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), claimsKey{}, claims)))
	}
}

// requireVoter rejects participants the host marked as not eligible.
func (s *Server) requireVoter(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !claimsFrom(r).Voter {
			writeError(w, http.StatusForbidden, codeNotEligible, "host token has voter=false")
			return
		}
		next(w, r)
	}
}

func (s *Server) voteRateLimited(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ok, retry := s.voteLimit.allow(claimsFrom(r).Subject, s.now())
		if !ok {
			w.Header().Set("Retry-After", retryAfterSeconds(retry))
			writeError(w, http.StatusTooManyRequests, codeRateLimited, "too many votes; retry later")
			return
		}
		next(w, r)
	}
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	c := claimsFrom(r)
	votes, err := s.store.VotesBySub(r.Context(), c.Subject)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	ideas, err := s.store.IdeasBySub(r.Context(), c.Subject)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"voter": c.Voter, "votes": votes, "ideas": mapSlice(ideas, ownIdea)})
}

func (s *Server) handleMyVotes(w http.ResponseWriter, r *http.Request) {
	votes, err := s.store.VotesBySub(r.Context(), claimsFrom(r).Subject)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"votes": votes})
}

func (s *Server) handleMyIdeas(w http.ResponseWriter, r *http.Request) {
	ideas, err := s.store.IdeasBySub(r.Context(), claimsFrom(r).Subject)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ideas": mapSlice(ideas, ownIdea)})
}

func (s *Server) handleSubmitIdea(w http.ResponseWriter, r *http.Request) {
	c := claimsFrom(r)
	var in struct {
		Title *string `json:"title"`
		Body  *string `json:"body"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Title == nil {
		writeError(w, http.StatusBadRequest, codeInvalidInput, "title is required")
		return
	}
	title, msg := cleanTitle(*in.Title)
	if msg != "" {
		writeError(w, http.StatusBadRequest, codeInvalidInput, msg)
		return
	}
	body := ""
	if in.Body != nil {
		if body, msg = cleanBody(*in.Body); msg != "" {
			writeError(w, http.StatusBadRequest, codeInvalidInput, msg)
			return
		}
	}
	idea, err := s.store.SubmitIdea(r.Context(), c.Subject, title, body, s.cfg.SubmitLimitPerDay, s.now())
	var rl *store.RateLimitedError
	if errors.As(err, &rl) {
		w.Header().Set("Retry-After", retryAfterSeconds(rl.RetryAfter))
		writeError(w, http.StatusTooManyRequests, codeRateLimited, "daily idea submission limit reached")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, ownIdea(idea))
}

func (s *Server) handleCastVote(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusNotFound, codeNotFound, "idea not found")
		return
	}
	var in struct {
		Value *int `json:"value"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Value == nil || (*in.Value != 1 && *in.Value != -1) {
		writeError(w, http.StatusBadRequest, codeInvalidInput, "value must be 1 or -1")
		return
	}
	idea, err := s.store.CastVote(r.Context(), id, claimsFrom(r).Subject, *in.Value)
	if s.voteError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"idea": publicIdea(idea), "my_vote": *in.Value})
}

func (s *Server) handleRemoveVote(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusNotFound, codeNotFound, "idea not found")
		return
	}
	idea, err := s.store.RemoveVote(r.Context(), id, claimsFrom(r).Subject)
	if s.voteError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"idea": publicIdea(idea), "my_vote": 0})
}

func (s *Server) voteError(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "idea not found")
	case errors.Is(err, store.ErrOwnIdea):
		writeError(w, http.StatusForbidden, codeOwnIdea, "you cannot vote on your own idea")
	case errors.Is(err, store.ErrVotingClosed):
		writeError(w, http.StatusConflict, codeVotingClosed, "voting is closed for shipped or declined ideas")
	default:
		s.internalError(w, r, err)
	}
	return true
}

func (s *Server) internalError(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("internal error", "method", r.Method, "path", r.URL.Path, "err", err)
	writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
}
