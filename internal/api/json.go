package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/slash4/featurevote/internal/store"
)

// Error codes (the widget maps them to localized strings).
const (
	codeUnauthorized    = "unauthorized"
	codeInvalidToken    = "invalid_token"
	codeTokenExpired    = "token_expired"
	codeNotEligible     = "not_eligible"
	codeOwnIdea         = "own_idea"
	codeNotFound        = "not_found"
	codeVotingClosed    = "voting_closed"
	codeInvalidInput    = "invalid_input"
	codePayloadTooLarge = "payload_too_large"
	codeRateLimited     = "rate_limited"
	codeForbiddenOrigin = "forbidden_origin"
	codeInternal        = "internal"
)

const (
	maxTitleRunes = 120
	maxBodyRunes  = 2000
)

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	enc.Encode(v) //nolint:errcheck
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	var b errorBody
	b.Error.Code = code
	b.Error.Message = msg
	writeJSON(w, status, b)
}

// decodeJSON reads a JSON object body into dst. Unknown fields are ignored.
// On failure it writes the error response and returns false.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(r.Body)
	err := dec.Decode(dst)
	if err == nil {
		// Drain so an oversized trailing payload is still reported as 413.
		_, err = io.Copy(io.Discard, r.Body)
		if err == nil {
			return true
		}
	}
	if isTooLarge(err) {
		writeError(w, http.StatusRequestEntityTooLarge, codePayloadTooLarge, "request body exceeds 16 KiB")
		return false
	}
	if errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, codeInvalidInput, "request body must be a JSON object")
		return false
	}
	writeError(w, http.StatusBadRequest, codeInvalidInput, "malformed JSON: "+err.Error())
	return false
}

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

// cleanTitle trims and validates a title (1..120 runes).
func cleanTitle(s string) (string, string) {
	s = strings.TrimSpace(s)
	if !utf8.ValidString(s) {
		return "", "title must be valid UTF-8"
	}
	n := utf8.RuneCountInString(s)
	if n < 1 || n > maxTitleRunes {
		return "", "title must be 1 to 120 characters"
	}
	return s, ""
}

// cleanBody trims and validates a body (0..2000 runes).
func cleanBody(s string) (string, string) {
	s = strings.TrimSpace(s)
	if !utf8.ValidString(s) {
		return "", "body must be valid UTF-8"
	}
	if utf8.RuneCountInString(s) > maxBodyRunes {
		return "", "body must be at most 2000 characters"
	}
	return s, ""
}

// PublicIdea is the anonymous public representation of an idea. It never
// carries author_sub or moderation_state.
type PublicIdea struct {
	ID        int64     `json:"id"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Status    string    `json:"status"`
	Up        int64     `json:"up"`
	Down      int64     `json:"down"`
	Score     int64     `json:"score"`
	CreatedAt time.Time `json:"created_at"`
}

// OwnIdea is a participant's own idea, including its moderation state.
type OwnIdea struct {
	PublicIdea
	ModerationState string `json:"moderation_state"`
}

// AdminIdea is the full admin representation.
type AdminIdea struct {
	PublicIdea
	AuthorSub       *string   `json:"author_sub"`
	ModerationState string    `json:"moderation_state"`
	MergedIntoID    *int64    `json:"merged_into_id"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func publicIdea(i store.Idea) PublicIdea {
	return PublicIdea{
		ID: i.ID, Title: i.Title, Body: i.Body, Status: i.Status,
		Up: i.Up, Down: i.Down, Score: i.Score(), CreatedAt: i.CreatedAt.UTC().Truncate(time.Second),
	}
}

func ownIdea(i store.Idea) OwnIdea {
	return OwnIdea{PublicIdea: publicIdea(i), ModerationState: i.ModerationState}
}

func adminIdea(i store.Idea) AdminIdea {
	return AdminIdea{
		PublicIdea: publicIdea(i), AuthorSub: i.AuthorSub, ModerationState: i.ModerationState,
		MergedIntoID: i.MergedIntoID, UpdatedAt: i.UpdatedAt.UTC().Truncate(time.Second),
	}
}

func mapSlice[T, U any](in []T, f func(T) U) []U {
	out := make([]U, len(in))
	for i, v := range in {
		out[i] = f(v)
	}
	return out
}
