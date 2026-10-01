package api

import (
	"crypto/hmac"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/slash4/featurevote/internal/store"
)

const (
	adminCookie     = "fv_admin"
	adminSessionTTL = 12 * time.Hour
	// adminLoginPerMinute caps /admin/login attempts per client IP.
	adminLoginPerMinute = 5
)

//go:embed admin.html.tmpl
var adminTemplateSrc string

var adminTemplate = template.Must(template.New("admin").Funcs(template.FuncMap{
	"statuses": func() []string {
		return []string{store.StatusUnderReview, store.StatusPlanned, store.StatusInProgress, store.StatusShipped, store.StatusDeclined}
	},
	"deref": func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	},
	"date": func(t time.Time) string { return t.UTC().Format("2006-01-02 15:04") },
	// del bundles what the shared delete form needs.
	"del": func(id int64, csrf string, merged int) deleteForm {
		return deleteForm{ID: id, CSRF: csrf, Merged: merged}
	},
}).Parse(adminTemplateSrc))

type deleteForm struct {
	ID     int64
	CSRF   string
	Merged int
}

func (s *Server) registerAdminPages(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin", s.adminPage)
	mux.HandleFunc("POST /admin/login", s.adminHTML(s.adminLogin))
	mux.HandleFunc("POST /admin/logout", s.adminHTML(s.adminAction(s.adminLogout)))
	mux.HandleFunc("POST /admin/ideas", s.adminHTML(s.adminAction(s.adminFormCreate)))
	mux.HandleFunc("POST /admin/ideas/{id}/{action}", s.adminHTML(s.adminAction(s.adminFormIdeaAction)))
	mux.HandleFunc("POST /admin/users/delete", s.adminHTML(s.adminAction(s.adminFormDeleteUser)))
}

func setAdminHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
}

// adminHTML adds security headers and rejects cross-origin POSTs: an Origin
// header, when present, must match the request's own host.
func (s *Server) adminHTML(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		setAdminHeaders(w)
		if o := r.Header.Get("Origin"); o != "" {
			u, err := url.Parse(o)
			if err != nil || u.Host == "" || !strings.EqualFold(u.Host, r.Host) {
				http.Error(w, "cross-origin request rejected", http.StatusForbidden)
				return
			}
		}
		next(w, r)
	}
}

func (s *Server) mac(msg string) string {
	m := hmac.New(sha256.New, []byte(s.cfg.AdminToken))
	m.Write([]byte(msg))
	return hex.EncodeToString(m.Sum(nil))
}

func (s *Server) newSessionValue() (string, time.Time) {
	exp := s.now().Add(adminSessionTTL)
	e := strconv.FormatInt(exp.Unix(), 10)
	return e + "." + s.mac("admin-session:"+e), exp
}

// session returns the valid session cookie value, or "".
func (s *Server) session(r *http.Request) string {
	c, err := r.Cookie(adminCookie)
	if err != nil {
		return ""
	}
	e, sig, ok := strings.Cut(c.Value, ".")
	if !ok {
		return ""
	}
	exp, err := strconv.ParseInt(e, 10, 64)
	if err != nil || s.now().Unix() >= exp {
		return ""
	}
	if !hmac.Equal([]byte(sig), []byte(s.mac("admin-session:"+e))) {
		return ""
	}
	return c.Value
}

func (s *Server) csrfToken(session string) string { return s.mac("csrf:" + session) }

// adminAction requires a valid session and CSRF token, parses the form and
// redirects back to /admin (PRG) with the flash the action returns, encoded
// as a fixed code plus typed parameters (see admin_flash.go). An action that
// returns the zero flash has written its own response.
func (s *Server) adminAction(next func(w http.ResponseWriter, r *http.Request) flash) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess := s.session(r)
		if sess == "" {
			http.Error(w, "not logged in", http.StatusUnauthorized)
			return
		}
		if err := r.ParseForm(); err != nil {
			if isTooLarge(err) {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		csrf := r.PostForm.Get("csrf")
		if csrf == "" || !hmac.Equal([]byte(csrf), []byte(s.csrfToken(sess))) {
			http.Error(w, "invalid CSRF token", http.StatusForbidden)
			return
		}
		f := next(w, r)
		if f.code == "" {
			return
		}
		http.Redirect(w, r, "/admin?"+f.query(), http.StatusSeeOther)
	}
}

// result returns ok when err is nil, otherwise the fixed error flash for err.
func (s *Server) result(r *http.Request, err error, ok flash) flash {
	var ce *store.ConflictError
	var hm *store.HasMergedError
	switch {
	case err == nil:
		return ok
	case errors.Is(err, store.ErrNotFound):
		return flash{code: flErrNotFound}
	case errors.As(err, &hm):
		return flash{code: flErrHasMerged, id: hm.ID, n: int64(hm.Merged)}
	case errors.As(err, &ce):
		return conflictFlash(ce, ok.id, ok.into)
	default:
		s.log.Error("admin action failed", "path", r.URL.Path, "err", err)
		return flash{code: flErrInternal}
	}
}

type adminView struct {
	LoggedIn bool
	Error    string
	Flash    string
	// FlashError styles the flash message as an error.
	FlashError bool
	CSRF       string
	Pending    []store.Idea
	Approved   []store.Idea
	Rejected   []store.Idea
	Merged     []store.Idea
	// MergedInto counts, per merge target, the ideas merged into it (the
	// delete form warns and asks for confirmation when it is non-zero).
	MergedInto map[int64]int
}

func (s *Server) renderAdmin(w http.ResponseWriter, status int, v adminView) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := adminTemplate.Execute(w, v); err != nil {
		s.log.Error("admin template", "err", err)
	}
}

func (s *Server) adminPage(w http.ResponseWriter, r *http.Request) {
	setAdminHeaders(w)
	sess := s.session(r)
	if sess == "" {
		s.renderAdmin(w, http.StatusOK, adminView{})
		return
	}
	all, err := s.store.ListByModeration(r.Context(), "all")
	var counts map[int64]int
	if err == nil {
		counts, err = s.store.MergedCounts(r.Context())
	}
	if err != nil {
		s.log.Error("admin page", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	v := adminView{LoggedIn: true, CSRF: s.csrfToken(sess), MergedInto: counts}
	if f, ok := parseFlash(r.URL.Query()); ok {
		v.Flash, v.FlashError = f.text()
	}
	for _, i := range all {
		switch i.ModerationState {
		case store.ModPending:
			v.Pending = append(v.Pending, i)
		case store.ModApproved:
			v.Approved = append(v.Approved, i)
		case store.ModRejected:
			v.Rejected = append(v.Rejected, i)
		case store.ModMerged:
			v.Merged = append(v.Merged, i)
		}
	}
	s.renderAdmin(w, http.StatusOK, v)
}

func (s *Server) adminLogin(w http.ResponseWriter, r *http.Request) {
	// Throttle before looking at the token: every attempt counts, so a
	// guesser gets 5 tries per minute per IP whatever the outcome.
	ip := s.clientIP(r)
	if ok, retry := s.loginLimit.allow(ip, s.now()); !ok {
		s.log.Warn("admin login throttled", "ip", ip)
		w.Header().Set("Retry-After", retryAfterSeconds(retry))
		s.renderAdmin(w, http.StatusTooManyRequests, adminView{Error: "Too many login attempts. Wait a minute and try again."})
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.adminTokenOK(r.PostForm.Get("token")) {
		s.log.Warn("admin login failed", "ip", ip)
		s.renderAdmin(w, http.StatusUnauthorized, adminView{Error: "Wrong admin token."})
		return
	}
	val, exp := s.newSessionValue()
	http.SetCookie(w, &http.Cookie{
		Name: adminCookie, Value: val, Path: "/admin", Expires: exp, MaxAge: int(adminSessionTTL / time.Second),
		HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (s *Server) adminLogout(w http.ResponseWriter, r *http.Request) flash {
	http.SetCookie(w, &http.Cookie{
		Name: adminCookie, Value: "", Path: "/admin", MaxAge: -1,
		HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
	return flash{}
}

func formID(v string) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	return id, err == nil && id > 0
}

// formTitleBody validates the title and body form fields; on failure it
// returns the error flash.
func formTitleBody(r *http.Request) (title, body string, bad flash) {
	title, msg := cleanTitle(r.PostForm.Get("title"))
	if msg != "" {
		return "", "", flash{code: flErrTitle}
	}
	body, msg = cleanBody(r.PostForm.Get("body"))
	if msg != "" {
		return "", "", flash{code: flErrBody}
	}
	return title, body, flash{}
}

func (s *Server) adminFormCreate(w http.ResponseWriter, r *http.Request) flash {
	title, body, bad := formTitleBody(r)
	if bad.code != "" {
		return bad
	}
	status := r.PostForm.Get("status")
	if status == "" {
		status = store.StatusUnderReview
	}
	if !store.ValidStatus(status) {
		return flash{code: flErrBadStatus}
	}
	idea, err := s.store.CreateApproved(r.Context(), title, body, status)
	return s.result(r, err, flash{code: flCreated, id: idea.ID})
}

func (s *Server) adminFormIdeaAction(w http.ResponseWriter, r *http.Request) flash {
	id, ok := formID(r.PathValue("id"))
	if !ok {
		return flash{code: flErrBadID}
	}
	ctx := r.Context()
	switch r.PathValue("action") {
	case "approve":
		_, err := s.store.Approve(ctx, id)
		return s.result(r, err, flash{code: flApproved, id: id})
	case "reject":
		_, err := s.store.Reject(ctx, id)
		return s.result(r, err, flash{code: flRejected, id: id})
	case "merge":
		into, ok := formID(r.PostForm.Get("into_id"))
		if !ok {
			return flash{code: flErrBadID}
		}
		res, err := s.store.Merge(ctx, id, into)
		return s.result(r, err, flash{code: flMerged, id: id, into: into, n: res.Moved, k: res.Dropped})
	case "status":
		status := r.PostForm.Get("status")
		if !store.ValidStatus(status) {
			return flash{code: flErrBadStatus}
		}
		_, err := s.store.SetStatus(ctx, id, status)
		return s.result(r, err, flash{code: flStatus, id: id, status: status})
	case "edit":
		title, body, bad := formTitleBody(r)
		if bad.code != "" {
			return bad
		}
		_, err := s.store.Edit(ctx, id, &title, &body)
		return s.result(r, err, flash{code: flSaved, id: id})
	case "delete":
		err := s.store.Delete(ctx, id, r.PostForm.Get("cascade") == "1")
		return s.result(r, err, flash{code: flDeleted, id: id})
	}
	http.NotFound(w, r)
	return flash{}
}

func (s *Server) adminFormDeleteUser(w http.ResponseWriter, r *http.Request) flash {
	sub := strings.TrimSpace(r.PostForm.Get("sub"))
	if sub == "" {
		return flash{code: flErrSubMissing}
	}
	votes, ideas, err := s.store.DeleteUserData(r.Context(), sub)
	return s.result(r, err, flash{code: flUserDeleted, n: votes, k: ideas})
}
