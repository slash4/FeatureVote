package api

import (
	"net/url"
	"strconv"

	"github.com/slash4/featurevote/internal/store"
)

// Admin flash messages travel in the PRG redirect as a fixed code (?m=) plus
// typed parameters (?id=, ?into=, ?n=, ?k=, ?status=). The page renders the
// text from this table only, so a crafted /admin?... link can never put
// attacker-chosen text on the admin page: unknown codes or parameters that
// fail validation render no flash at all.

type flash struct {
	code   string
	id     int64  // idea id
	into   int64  // merge target id
	n, k   int64  // counts
	status string // status label
}

// Flash codes.
const (
	flCreated       = "created"
	flApproved      = "approved"
	flRejected      = "rejected"
	flMerged        = "merged"
	flStatus        = "status"
	flSaved         = "saved"
	flDeleted       = "deleted"
	flUserDeleted   = "user_deleted"
	flErrNotFound   = "err_not_found"
	flErrInternal   = "err_internal"
	flErrBadID      = "err_bad_id"
	flErrBadStatus  = "err_bad_status"
	flErrSubMissing = "err_sub_missing"
	flErrTitle      = "err_title"
	flErrBody       = "err_body"
	flErrHasMerged  = "err_has_merged"
	flErrTransition = "err_transition"
	flErrSelfMerge  = "err_self_merge"
	flErrMerged     = "err_already_merged"
	flErrBadTarget  = "err_bad_target"
)

// needs lists which parameters each code requires.
type needs struct{ id, into, n, k, status bool }

var flashNeeds = map[string]needs{
	flCreated: {id: true}, flApproved: {id: true}, flRejected: {id: true},
	flMerged: {id: true, into: true, n: true, k: true}, flStatus: {id: true, status: true},
	flSaved: {id: true}, flDeleted: {id: true}, flUserDeleted: {n: true, k: true},
	flErrNotFound: {}, flErrInternal: {}, flErrBadID: {}, flErrBadStatus: {}, flErrSubMissing: {},
	flErrTitle: {}, flErrBody: {}, flErrHasMerged: {id: true, n: true},
	flErrTransition: {id: true}, flErrSelfMerge: {}, flErrMerged: {id: true}, flErrBadTarget: {into: true},
}

func (f flash) query() string {
	v := url.Values{"m": {f.code}}
	nd := flashNeeds[f.code]
	if nd.id {
		v.Set("id", strconv.FormatInt(f.id, 10))
	}
	if nd.into {
		v.Set("into", strconv.FormatInt(f.into, 10))
	}
	if nd.n {
		v.Set("n", strconv.FormatInt(f.n, 10))
	}
	if nd.k {
		v.Set("k", strconv.FormatInt(f.k, 10))
	}
	if nd.status {
		v.Set("status", f.status)
	}
	return v.Encode()
}

// parseFlash reads a flash from the /admin query; ok is false for anything
// not produced by flash.query.
func parseFlash(q url.Values) (f flash, ok bool) {
	f.code = q.Get("m")
	nd, known := flashNeeds[f.code]
	if !known {
		return flash{}, false
	}
	num := func(key string, dst *int64, min int64) bool {
		n, err := strconv.ParseInt(q.Get(key), 10, 64)
		*dst = n
		return err == nil && n >= min
	}
	if (nd.id && !num("id", &f.id, 1)) || (nd.into && !num("into", &f.into, 1)) ||
		(nd.n && !num("n", &f.n, 0)) || (nd.k && !num("k", &f.k, 0)) {
		return flash{}, false
	}
	if nd.status {
		f.status = q.Get("status")
		if !store.ValidStatus(f.status) {
			return flash{}, false
		}
	}
	return f, true
}

func plural(n int64, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.FormatInt(n, 10) + " " + many
}

// text renders the message and whether it is an error.
func (f flash) text() (string, bool) {
	ref := "#" + strconv.FormatInt(f.id, 10)
	switch f.code {
	case flCreated:
		return "Created idea " + ref + ".", false
	case flApproved:
		return "Approved " + ref + ".", false
	case flRejected:
		return "Rejected " + ref + ".", false
	case flMerged:
		return "Merged " + ref + " into #" + strconv.FormatInt(f.into, 10) + " (" +
			plural(f.n, "vote", "votes") + " moved, " + strconv.FormatInt(f.k, 10) + " dropped).", false
	case flStatus:
		return "Status of " + ref + " set to " + f.status + ".", false
	case flSaved:
		return "Saved " + ref + ".", false
	case flDeleted:
		return "Deleted " + ref + ".", false
	case flUserDeleted:
		return "User data deleted: " + plural(f.n, "vote", "votes") + " deleted, " +
			plural(f.k, "idea", "ideas") + " anonymised.", false
	case flErrNotFound:
		return "Error: idea not found.", true
	case flErrInternal:
		return "Error: internal error.", true
	case flErrBadID:
		return "Error: invalid idea id.", true
	case flErrBadStatus:
		return "Error: unknown status.", true
	case flErrSubMissing:
		return "Error: sub is required.", true
	case flErrTitle:
		return "Error: the title must be 1 to 120 characters, without control characters.", true
	case flErrBody:
		return "Error: the description must be at most 2000 characters, without control characters.", true
	case flErrHasMerged:
		return "Error: " + ref + " has " + plural(f.n, "merged idea", "merged ideas") +
			"; tick the box to delete them too, or merge them elsewhere first.", true
	case flErrTransition:
		return "Error: " + ref + " cannot make that moderation change from its current state.", true
	case flErrSelfMerge:
		return "Error: an idea cannot be merged into itself.", true
	case flErrMerged:
		return "Error: " + ref + " is already merged.", true
	case flErrBadTarget:
		return "Error: merge target #" + strconv.FormatInt(f.into, 10) + " must be approved or pending.", true
	}
	return "", false
}

// conflictFlash maps a store conflict to its fixed message.
func conflictFlash(ce *store.ConflictError, id, into int64) flash {
	switch ce.Code {
	case store.ConflictSelfMerge:
		return flash{code: flErrSelfMerge}
	case store.ConflictAlreadyMerged:
		return flash{code: flErrMerged, id: id}
	case store.ConflictBadTarget:
		return flash{code: flErrBadTarget, into: into}
	default:
		return flash{code: flErrTransition, id: id}
	}
}
