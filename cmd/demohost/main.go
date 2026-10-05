// Command demohost is a reference HOST application for local development and
// demos. It serves examples/ statically and exposes a host token endpoint the
// way a cookie-session product would:
//
//	GET /api/login?as=<sub>&voter=true|false  sets demo cookies, redirects to /demo.html
//	GET /api/logout                           clears them
//	GET /api/fv-token                         200 {"token": "<jwt>"} or 401
//
// It trusts a plain cookie for identity. NEVER deploy it.
package main

import (
	"encoding/json"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/slash4/featurevote/internal/hosttoken"
)

const tokenTTL = 10 * time.Minute

func main() {
	addr := flag.String("addr", ":8090", "listen address")
	dir := flag.String("dir", "./examples", "directory served statically")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	secret, issuer := os.Getenv("FV_HOST_SECRET"), os.Getenv("FV_HOST_ISSUER")
	audience := strings.TrimSpace(os.Getenv("FV_AUDIENCE")) // optional, sent as aud
	if secret == "" || issuer == "" {
		log.Error("FV_HOST_SECRET and FV_HOST_ISSUER must be set")
		os.Exit(1)
	}
	log.Warn("DEMO HOST: identity comes from an unauthenticated cookie. For local development only; never deploy.")

	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServer(http.Dir(*dir)))
	mux.HandleFunc("GET /api/login", func(w http.ResponseWriter, r *http.Request) {
		sub := r.URL.Query().Get("as")
		if sub == "" || len(sub) > 255 {
			http.Error(w, "?as=<sub> is required", http.StatusBadRequest)
			return
		}
		voter, _ := strconv.ParseBool(r.URL.Query().Get("voter"))
		setCookie(w, "demo_user", sub, 0)
		setCookie(w, "demo_voter", strconv.FormatBool(voter), 0)
		http.Redirect(w, r, "/demo.html", http.StatusSeeOther)
	})
	mux.HandleFunc("GET /api/logout", func(w http.ResponseWriter, r *http.Request) {
		setCookie(w, "demo_user", "", -1)
		setCookie(w, "demo_voter", "", -1)
		http.Redirect(w, r, "/demo.html", http.StatusSeeOther)
	})
	// The reference host token endpoint (see docs/INTEGRATION.md).
	mux.HandleFunc("GET /api/fv-token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		user, err := r.Cookie("demo_user")
		if err != nil || user.Value == "" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"not logged in"}`)) //nolint:errcheck
			return
		}
		voter := false
		if c, err := r.Cookie("demo_voter"); err == nil {
			voter, _ = strconv.ParseBool(c.Value)
		}
		now := time.Now()
		json.NewEncoder(w).Encode(map[string]string{ //nolint:errcheck
			"token": hosttoken.Encode(secret, hosttoken.Claims{
				Issuer: issuer, Subject: user.Value, Audience: audience, Voter: voter,
				IssuedAt: now, ExpiresAt: now.Add(tokenTTL),
			}),
		})
	})

	log.Info("demohost listening", "addr", *addr, "dir", *dir, "issuer", issuer)
	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		log.Error("demohost", "err", err)
		os.Exit(1)
	}
}

func setCookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: maxAge, HttpOnly: true, SameSite: http.SameSiteLaxMode})
}
