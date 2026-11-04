package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/slash4/featurevote/internal/store"
	"github.com/slash4/featurevote/internal/testdb"
)

func init() { pingBackoff = []time.Duration{time.Millisecond, time.Millisecond} }

const (
	secretTitle  = "TITLE-zq7Xk2-do-not-leak"
	secretBody   = "BODY-p9Lm4w-do-not-leak"
	secretAuthor = "AUTHOR-r3Tn8v-do-not-leak"
)

// seed inserts an idea created `ago` before the database's now() and returns its id.
func seed(t *testing.T, pool *pgxpool.Pool, state string, ago time.Duration, mergedInto *int64) int64 {
	t.Helper()
	var id int64
	err := pool.QueryRow(context.Background(), `INSERT INTO ideas (title, body, author_sub, moderation_state, merged_into_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, now() - make_interval(secs => $6), now()) RETURNING id`,
		secretTitle, secretBody, secretAuthor, state, mergedInto, ago.Seconds()).Scan(&id)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	return id
}

type seeded struct{ p5d, p49h, p47h, p10d int64 }

func seedMix(t *testing.T, pool *pgxpool.Pool) seeded {
	t.Helper()
	var s seeded
	// p5d is inserted first so that neither id order matches created_at order.
	s.p5d = seed(t, pool, store.ModPending, 5*24*time.Hour, nil)
	s.p49h = seed(t, pool, store.ModPending, 49*time.Hour, nil)
	s.p47h = seed(t, pool, store.ModPending, 47*time.Hour, nil)
	approved := seed(t, pool, store.ModApproved, 30*24*time.Hour, nil)
	seed(t, pool, store.ModRejected, 30*24*time.Hour, nil)
	seed(t, pool, store.ModMerged, 30*24*time.Hour, &approved)
	s.p10d = seed(t, pool, store.ModPending, 10*24*time.Hour, nil)
	return s
}

func near(got, want time.Duration) bool {
	d := got - want
	return d > -time.Minute && d < time.Minute
}

func TestPendingOlderThan(t *testing.T) {
	pool := testdb.Open(t)
	s := seedMix(t, pool)
	got, err := store.New(pool).PendingOlderThan(context.Background(), 48*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].ID != s.p10d || got[1].ID != s.p5d || got[2].ID != s.p49h {
		t.Fatalf("got %+v, want ids [%d %d %d]", got, s.p10d, s.p5d, s.p49h)
	}
	if !near(got[0].Age, 10*24*time.Hour) || !near(got[1].Age, 5*24*time.Hour) || !near(got[2].Age, 49*time.Hour) {
		t.Fatalf("ages %s, %s, %s", got[0].Age, got[1].Age, got[2].Age)
	}
	if !near(time.Since(got[2].CreatedAt), 49*time.Hour) {
		t.Fatalf("created_at %s", got[2].CreatedAt)
	}
	n, err := store.New(pool).PendingCount(context.Background())
	if err != nil || n != 4 {
		t.Fatalf("PendingCount = %d, %v; want 4", n, err)
	}
	for _, age := range []time.Duration{0, -time.Hour} {
		if _, err := store.New(pool).PendingOlderThan(context.Background(), age); err == nil {
			t.Fatalf("age %s: want error", age)
		}
	}
}

func TestPendingOlderThanBoundary(t *testing.T) {
	pool := testdb.Open(t)
	seed(t, pool, store.ModPending, 59*time.Minute, nil)
	old := seed(t, pool, store.ModPending, 61*time.Minute, nil)
	got, err := store.New(pool).PendingOlderThan(context.Background(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != old {
		t.Fatalf("got %+v, want only #%d", got, old)
	}
}

type pingReq struct{ method, path, body, ua, ctype string }

type pingTarget struct {
	*httptest.Server
	mu     sync.Mutex
	reqs   []pingReq
	status int
}

func newTarget(t *testing.T, status int) *pingTarget {
	pt := &pingTarget{status: status}
	pt.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		pt.mu.Lock()
		pt.reqs = append(pt.reqs, pingReq{r.Method, r.URL.Path, string(b), r.UserAgent(), r.Header.Get("Content-Type")})
		pt.mu.Unlock()
		w.WriteHeader(pt.status)
	}))
	t.Cleanup(pt.Close)
	return pt
}

func (pt *pingTarget) requests() []pingReq {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	return append([]pingReq(nil), pt.reqs...)
}

func runWith(vars map[string]string, args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), args, func(k string) string { return vars[k] }, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func dbURL() string { return os.Getenv("FV_TEST_DATABASE_URL") }

func assertNoLeak(t *testing.T, where, s string) {
	t.Helper()
	for _, secret := range []string{secretTitle, secretBody, secretAuthor} {
		if strings.Contains(s, secret) {
			t.Fatalf("%s leaks %q:\n%s", where, secret, s)
		}
	}
}

func TestRunStale(t *testing.T) {
	pool := testdb.Open(t)
	s := seedMix(t, pool)
	pt := newTarget(t, 200)
	code, out, errOut := runWith(map[string]string{
		"FV_DATABASE_URL": dbURL(), "FV_MODERATION_PING_URL": pt.URL + "/ping/abc123",
	}, "-admin-url", "https://feedback.example/admin")
	if code != 1 {
		t.Fatalf("exit %d, stderr %s", code, errOut)
	}
	reqs := pt.requests()
	if len(reqs) != 1 {
		t.Fatalf("%d requests, want 1", len(reqs))
	}
	r := reqs[0]
	if r.method != http.MethodPost || r.path != "/ping/abc123/fail" {
		t.Fatalf("got %s %s", r.method, r.path)
	}
	if r.ua != "fvpending" || r.ctype != "text/plain; charset=utf-8" {
		t.Fatalf("headers: UA %q, Content-Type %q", r.ua, r.ctype)
	}
	for _, id := range []int64{s.p10d, s.p5d, s.p49h} {
		if !strings.Contains(r.body, fmt.Sprintf("#%d ", id)) {
			t.Fatalf("body lacks #%d:\n%s", id, r.body)
		}
	}
	if strings.Contains(r.body, fmt.Sprintf("#%d ", s.p47h)) {
		t.Fatalf("body lists the 47h idea:\n%s", r.body)
	}
	if !strings.HasPrefix(r.body, "3 suggestions waiting for moderation longer than 48h:\n") ||
		!strings.Contains(r.body, "pending 10d 0h  (submitted ") ||
		!strings.HasSuffix(r.body, "Moderate at https://feedback.example/admin\n") {
		t.Fatalf("unexpected body:\n%s", r.body)
	}
	if out != r.body {
		t.Fatalf("stdout differs from ping body:\n%s", out)
	}
	assertNoLeak(t, "ping body", r.body)
	assertNoLeak(t, "stderr", errOut)
}

func TestRunClean(t *testing.T) {
	pool := testdb.Open(t)
	seed(t, pool, store.ModPending, time.Hour, nil)
	seed(t, pool, store.ModApproved, 30*24*time.Hour, nil)
	pt := newTarget(t, 200)
	code, out, errOut := runWith(map[string]string{
		"FV_DATABASE_URL": dbURL(), "FV_MODERATION_PING_URL": pt.URL + "/ping/abc123",
	}, "-admin-url", "") // systemd passes an empty arg when FV_MODERATION_ADMIN_URL is unset
	if code != 0 {
		t.Fatalf("exit %d, stderr %s", code, errOut)
	}
	reqs := pt.requests()
	if len(reqs) != 1 || reqs[0].method != http.MethodPost || reqs[0].path != "/ping/abc123" {
		t.Fatalf("requests %+v", reqs)
	}
	want := "OK: no suggestion has been waiting for moderation longer than 48h.\npending (any age): 1\n"
	if reqs[0].body != want || out != want {
		t.Fatalf("body %q, stdout %q", reqs[0].body, out)
	}
}

func TestRunNoPingURL(t *testing.T) {
	pool := testdb.Open(t)
	id := seed(t, pool, store.ModPending, 3*time.Hour, nil)
	vars := map[string]string{"FV_DATABASE_URL": dbURL()}
	code, out, _ := runWith(vars, "-older-than", "2h")
	if code != 1 || !strings.HasPrefix(out, "1 suggestion waiting for moderation longer than 2h:\n") ||
		!strings.Contains(out, fmt.Sprintf("  #%d  pending 3h 0m  (submitted ", id)) || strings.Contains(out, "Moderate at") {
		t.Fatalf("exit %d, stdout:\n%s", code, out)
	}
	code, out, _ = runWith(vars, "-older-than", "4h")
	if code != 0 || !strings.HasPrefix(out, "OK: ") {
		t.Fatalf("exit %d, stdout:\n%s", code, out)
	}
}

func TestRunPingFails(t *testing.T) {
	testdb.Open(t) // migrated, empty database: the run itself succeeds
	pt := newTarget(t, 500)
	code, _, errOut := runWith(map[string]string{
		"FV_DATABASE_URL": dbURL(), "FV_MODERATION_PING_URL": pt.URL + "/ping/abc123",
	})
	if code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if n := len(pt.requests()); n != 3 {
		t.Fatalf("%d attempts, want 3", n)
	}
	if strings.Contains(errOut, "abc123") || !strings.Contains(errOut, "failed after 3 attempts") {
		t.Fatalf("stderr: %s", errOut)
	}
}

func TestRunDBUnreachable(t *testing.T) {
	pt := newTarget(t, 200)
	code, out, errOut := runWith(map[string]string{
		"FV_DATABASE_URL":        "postgres://u:SECRETPW@127.0.0.1:1/x?connect_timeout=2",
		"FV_MODERATION_PING_URL": pt.URL + "/ping/abc123",
	})
	if code != 2 || out != "" {
		t.Fatalf("exit %d, stdout %q", code, out)
	}
	reqs := pt.requests()
	if len(reqs) != 1 || reqs[0].path != "/ping/abc123/fail" {
		t.Fatalf("requests %+v", reqs)
	}
	if !strings.HasPrefix(reqs[0].body, "fvpending: check could not run:") || !strings.Contains(reqs[0].body, "127.0.0.1") {
		t.Fatalf("body %q", reqs[0].body)
	}
	for where, s := range map[string]string{"body": reqs[0].body, "stderr": errOut} {
		if strings.Contains(s, "SECRETPW") {
			t.Fatalf("%s leaks the password: %s", where, s)
		}
	}
}

// TestPgxErrorHasNoPassword pins the assumption that pgx connect errors do not
// echo the password (query() also scrubs it as a backstop).
func TestPgxErrorHasNoPassword(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, "postgres://u:SECRETPW@127.0.0.1:1/x")
	if err == nil {
		defer pool.Close()
		err = pool.Ping(ctx)
	}
	if err == nil || strings.Contains(err.Error(), "SECRETPW") {
		t.Fatalf("err = %v", err)
	}
	if _, err := pgxpool.ParseConfig("postgres://u:SECRETPW@127.0.0.1:notaport/x"); err == nil || strings.Contains(err.Error(), "SECRETPW") {
		t.Fatalf("parse err = %v", err)
	}
	_, _, err = query(ctx, "postgres://u:SECRETPW@127.0.0.1:notaport/x", time.Hour, 5*time.Second)
	if err == nil || strings.Contains(err.Error(), "SECRETPW") {
		t.Fatalf("query err = %v", err)
	}
}

func TestRunConfigErrors(t *testing.T) {
	pt := newTarget(t, 200)
	for _, bad := range []string{"ftp://x", "https://h/ping/t/fail", "https://h/ping/t/start/", "https://h/p?x=1",
		"https://h/p?", "https://h/p#frag", "/ping/t", "h/ping/t", "https:///ping/t", pt.URL + "/ping/abc123/fail"} {
		code, _, errOut := runWith(map[string]string{"FV_DATABASE_URL": "postgres://u@127.0.0.1:1/x", "FV_MODERATION_PING_URL": bad})
		if code != 2 {
			t.Errorf("%q: exit %d, want 2", bad, code)
		}
		if strings.Contains(errOut, bad) {
			t.Errorf("%q: stderr echoes the URL: %s", bad, errOut)
		}
	}
	if n := len(pt.requests()); n != 0 {
		t.Fatalf("invalid ping URLs caused %d requests", n)
	}

	// Bad flags with a valid ping URL: exit 2 and the check is told.
	for _, args := range [][]string{{"-older-than", "0"}, {"-older-than", "-1h"}, {"-older-than", "soon"}} {
		code, _, _ := runWith(map[string]string{"FV_DATABASE_URL": "postgres://u@127.0.0.1:1/x", "FV_MODERATION_PING_URL": pt.URL + "/p"}, args...)
		if code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
	code, _, _ := runWith(map[string]string{"FV_MODERATION_PING_URL": pt.URL + "/p"})
	if code != 2 {
		t.Errorf("missing FV_DATABASE_URL: exit %d", code)
	}
	reqs := pt.requests()
	if len(reqs) != 4 {
		t.Fatalf("%d requests, want 4", len(reqs))
	}
	for _, r := range reqs {
		if r.path != "/p/fail" || !strings.HasPrefix(r.body, "fvpending: check could not run: ") {
			t.Fatalf("request %+v", r)
		}
	}
	// Without a ping URL: still exit 2.
	if code, _, _ := runWith(map[string]string{"FV_DATABASE_URL": "postgres://u@127.0.0.1:1/x"}, "-older-than", "0"); code != 2 {
		t.Errorf("no ping URL, -older-than 0: exit %d", code)
	}
}

func TestPingRetry(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	defer srv.Close()
	if err := ping(context.Background(), srv.Client(), srv.URL+"/ping/abc123", false, "x", []time.Duration{0, 0}); err != nil {
		t.Fatalf("third attempt should succeed: %v", err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d", calls)
	}

	// Network error: the message must not carry the token.
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL + "/ping/abc123"
	dead.Close()
	err := ping(context.Background(), http.DefaultClient, deadURL, true, "x", []time.Duration{0, 0})
	if err == nil || strings.Contains(err.Error(), "abc123") || !strings.Contains(err.Error(), "failed after 3 attempts") {
		t.Fatalf("err = %v", err)
	}
}

func TestParsePingURL(t *testing.T) {
	u, err := parsePingURL("https://app.okokumo.com/ping/tok/")
	if err != nil || u.String() != "https://app.okokumo.com/ping/tok" {
		t.Fatalf("got %v, %v", u, err)
	}
	if got := redact("https://app.okokumo.com/ping/tok"); got != "https://app.okokumo.com" {
		t.Fatalf("redact = %q", got)
	}
}

func TestFormatAge(t *testing.T) {
	for d, want := range map[time.Duration]string{
		16*24*time.Hour + 2*time.Hour + 59*time.Minute: "16d 2h",
		24 * time.Hour:                "1d 0h",
		23*time.Hour + 59*time.Minute: "23h 59m",
		49 * time.Minute:              "0h 49m",
		-time.Second:                  "0h 0m",
	} {
		if got := formatAge(d); got != want {
			t.Errorf("formatAge(%s) = %q, want %q", d, got, want)
		}
	}
}

func TestFormatReport(t *testing.T) {
	created := time.Date(2026, 10, 19, 8, 37, 0, 0, time.FixedZone("CEST", 2*3600))
	var stale []store.PendingIdea
	for i := int64(1); i <= 23; i++ {
		stale = append(stale, store.PendingIdea{ID: i, CreatedAt: created, Age: 16*24*time.Hour + 2*time.Hour})
	}
	got := formatReport(stale, 0, 48*time.Hour, "https://feedback.okokumo.com/admin")
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) != 23 {
		t.Fatalf("%d lines:\n%s", len(lines), got)
	}
	if lines[0] != "23 suggestions waiting for moderation longer than 48h:" ||
		lines[1] != "  #1  pending 16d 2h  (submitted 2026-10-19 06:37 UTC)" ||
		lines[20] != "  #20  pending 16d 2h  (submitted 2026-10-19 06:37 UTC)" ||
		lines[21] != "  ... and 3 more" ||
		lines[22] != "Moderate at https://feedback.okokumo.com/admin" {
		t.Fatalf("report:\n%s", got)
	}
	if got := formatReport(stale[:1], 0, time.Hour, ""); got != "1 suggestion waiting for moderation longer than 1h:\n  #1  pending 16d 2h  (submitted 2026-10-19 06:37 UTC)\n" {
		t.Fatalf("singular report:\n%s", got)
	}
}

func TestShortDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		48 * time.Hour:                  "48h",
		90 * time.Minute:                "1h30m",
		2 * time.Minute:                 "2m",
		10 * time.Second:                "10s",
		time.Hour + 5*time.Second:       "1h0m5s",
		time.Second:                     "1s",
		30*time.Minute + 10*time.Second: "30m10s",
	} {
		if got := shortDuration(d); got != want {
			t.Errorf("shortDuration(%s) = %q, want %q", d, got, want)
		}
	}
}

// TestPingTruncatesToValidUTF8: the receiving check stores the body in a
// Postgres TEXT column, which rejects a rune split by the byte cap.
func TestPingTruncatesToValidUTF8(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
	}))
	defer srv.Close()
	body := strings.Repeat("a", maxBody-1) + "é" + "tail" // é straddles the cap
	if err := ping(context.Background(), srv.Client(), srv.URL+"/p", true, body, nil); err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(got) || len(got) != maxBody-1 {
		t.Fatalf("len %d, valid %v", len(got), utf8.ValidString(got))
	}
}
