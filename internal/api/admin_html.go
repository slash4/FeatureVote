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
}).Parse(adminTemplateSrc))

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
// redirects back to /admin (PRG) with the flash message the action returns.
func (s *Server) adminAction(next func(w http.ResponseWriter, r *http.Request) (string, error)) http.HandlerFunc {
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
		msg, err := next(w, r)
		if err != nil {
			var ce *store.ConflictError
			switch {
			case errors.Is(err, store.ErrNotFound):
				msg = "Error: idea not found."
			case errors.As(err, &ce):
				msg = "Error: " + ce.Msg + "."
			case errors.Is(err, errFormInput):
				msg = "Error: " + err.Error()
			default:
				s.log.Error("admin action failed", "path", r.URL.Path, "err", err)
				msg = "Error: internal error."
			}
		}
		if msg == "" {
			return // the action wrote its own response
		}
		http.Redirect(w, r, "/admin?msg="+url.QueryEscape(msg), http.StatusSeeOther)
	}
}

var errFormInput = errors.New("")

type formError string

func (e formError) Error() string { return string(e) }
func (e formError) Is(target error) bool {
	return target == errFormInput
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
	if err != nil {
		s.log.Error("admin page", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	flash := r.URL.Query().Get("msg")
	v := adminView{LoggedIn: true, CSRF: s.csrfToken(sess), Flash: flash, FlashError: strings.HasPrefix(flash, "Error")}
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
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.adminTokenOK(r.PostForm.Get("token")) {
		s.log.Warn("admin login failed")
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

func (s *Server) adminLogout(w http.ResponseWriter, r *http.Request) (string, error) {
	http.SetCookie(w, &http.Cookie{
		Name: adminCookie, Value: "", Path: "/admin", MaxAge: -1,
		HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
	return "", nil
}

func formID(v string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || id <= 0 {
		return 0, formError("invalid idea id.")
	}
	return id, nil
}

func (s *Server) adminFormCreate(w http.ResponseWriter, r *http.Request) (string, error) {
	title, msg := cleanTitle(r.PostForm.Get("title"))
	body := ""
	if msg == "" {
		body, msg = cleanBody(r.PostForm.Get("body"))
	}
	status := r.PostForm.Get("status")
	if status == "" {
		status = store.StatusUnderReview
	}
	if msg == "" && !store.ValidStatus(status) {
		msg = "unknown status"
	}
	if msg != "" {
		return "", formError(msg + ".")
	}
	idea, err := s.store.CreateApproved(r.Context(), title, body, status)
	if err != nil {
		return "", err
	}
	return "Created idea #" + strconv.FormatInt(idea.ID, 10) + ".", nil
}

func (s *Server) adminFormIdeaAction(w http.ResponseWriter, r *http.Request) (string, error) {
	id, err := formID(r.PathValue("id"))
	if err != nil {
		return "", err
	}
	ctx := r.Context()
	ref := "#" + strconv.FormatInt(id, 10)
	switch r.PathValue("action") {
	case "approve":
		_, err = s.store.Approve(ctx, id)
		return "Approved " + ref + ".", err
	case "reject":
		_, err = s.store.Reject(ctx, id)
		return "Rejected " + ref + ".", err
	case "merge":
		into, err := formID(r.PostForm.Get("into_id"))
		if err != nil {
			return "", err
		}
		res, err := s.store.Merge(ctx, id, into)
		return "Merged " + ref + " into #" + strconv.FormatInt(into, 10) + " (" +
			strconv.FormatInt(res.Moved, 10) + " votes moved, " + strconv.FormatInt(res.Dropped, 10) + " dropped).", err
	case "status":
		status := r.PostForm.Get("status")
		if !store.ValidStatus(status) {
			return "", formError("unknown status.")
		}
		_, err = s.store.SetStatus(ctx, id, status)
		return "Status of " + ref + " set to " + status + ".", err
	case "edit":
		title, msg := cleanTitle(r.PostForm.Get("title"))
		body := ""
		if msg == "" {
			body, msg = cleanBody(r.PostForm.Get("body"))
		}
		if msg != "" {
			return "", formError(msg + ".")
		}
		_, err = s.store.Edit(ctx, id, &title, &body)
		return "Saved " + ref + ".", err
	case "delete":
		return "Deleted " + ref + ".", s.store.Delete(ctx, id)
	}
	http.NotFound(w, r)
	return "", nil
}

func (s *Server) adminFormDeleteUser(w http.ResponseWriter, r *http.Request) (string, error) {
	sub := strings.TrimSpace(r.PostForm.Get("sub"))
	if sub == "" {
		return "", formError("sub is required.")
	}
	votes, ideas, err := s.store.DeleteUserData(r.Context(), sub)
	return "User data deleted: " + strconv.FormatInt(votes, 10) + " votes deleted, " +
		strconv.FormatInt(ideas, 10) + " ideas anonymised.", err
}
