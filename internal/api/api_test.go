package api_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/slash4/featurevote/internal/api"
	"github.com/slash4/featurevote/internal/config"
	"github.com/slash4/featurevote/internal/hosttoken"
	"github.com/slash4/featurevote/internal/store"
	"github.com/slash4/featurevote/internal/testdb"
)

const (
	hostSecret  = "test-host-secret-0123456789abcdef-xyz"
	hostIssuer  = "okokumo"
	adminToken  = "test-admin-token-0123456789abcdef-xyz"
	allowOrigin = "https://app.okokumo.com"
)

// clock is an injectable fake clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type env struct {
	t     *testing.T
	srv   *httptest.Server
	pool  *pgxpool.Pool
	clock *clock
}

func newEnv(t *testing.T, mutate ...func(*config.Config)) *env {
	t.Helper()
	pool := testdb.Open(t)
	cfg := config.Config{
		DatabaseURL:        "unused",
		HostSecret:         hostSecret,
		HostIssuer:         hostIssuer,
		AdminToken:         adminToken,
		AllowedOrigins:     []string{allowOrigin},
		ListenAddr:         ":0",
		SubmitLimitPerDay:  5,
		VoteLimitPerMinute: 1000,
		ClockSkew:          30 * time.Second,
		CookieSecure:       false,
	}
	for _, m := range mutate {
		m(&cfg)
	}
	clk := &clock{t: time.Now().Truncate(time.Second)}
	srv := httptest.NewServer(api.NewServer(api.Options{
		Store:    store.New(pool),
		Config:   cfg,
		WidgetJS: []byte("/* widget */ console.log('fv');"),
		Now:      clk.Now,
	}).Handler())
	t.Cleanup(srv.Close)
	return &env{t: t, srv: srv, pool: pool, clock: clk}
}

func (e *env) token(sub string, voter bool) string {
	return hosttoken.MintAt(e.clock.Now(), hostSecret, hostIssuer, sub, voter, 10*time.Minute)
}

type resp struct {
	status int
	header http.Header
	body   []byte
}

func (r resp) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.body, &m); err != nil {
		t.Fatalf("response is not JSON (%d): %s", r.status, r.body)
	}
	return m
}

func (r resp) code(t *testing.T) string {
	t.Helper()
	e, _ := r.json(t)["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

// do sends a request. body may be nil, a string (sent raw) or a value
// (JSON-encoded). auth is the full bearer token ("" for none).
func (e *env) do(method, path, auth string, body any, headers ...string) resp {
	e.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	case []byte:
		rd = bytes.NewReader(b)
	default:
		j, _ := json.Marshal(b)
		rd = bytes.NewReader(j)
	}
	req, err := http.NewRequest(method, e.srv.URL+path, rd)
	if err != nil {
		e.t.Fatal(err)
	}
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return resp{status: res.StatusCode, header: res.Header, body: b}
}

func (e *env) must(r resp, status int) resp {
	e.t.Helper()
	if r.status != status {
		e.t.Fatalf("status = %d, want %d; body: %s", r.status, status, r.body)
	}
	return r
}

func (e *env) mustErr(r resp, status int, code string) {
	e.t.Helper()
	e.must(r, status)
	if got := r.code(e.t); got != code {
		e.t.Fatalf("error code = %q, want %q; body: %s", got, code, r.body)
	}
}

// submit creates a pending idea authored by sub and returns its id.
func (e *env) submit(sub, title string) int64 {
	e.t.Helper()
	r := e.must(e.do("POST", "/v1/ideas", e.token(sub, true), map[string]string{"title": title, "body": "b"}), 201)
	return int64(r.json(e.t)["id"].(float64))
}

func (e *env) approve(id int64) {
	e.t.Helper()
	e.must(e.do("POST", fmt.Sprintf("/v1/admin/ideas/%d/approve", id), adminToken, nil), 200)
}

// approvedIdea creates and approves an idea authored by sub.
func (e *env) approvedIdea(sub, title string) int64 {
	e.t.Helper()
	id := e.submit(sub, title)
	e.approve(id)
	return id
}

func (e *env) vote(id int64, sub string, value int) resp {
	e.t.Helper()
	return e.do("PUT", fmt.Sprintf("/v1/ideas/%d/vote", id), e.token(sub, true), map[string]int{"value": value})
}

// counts fetches the public idea and returns up, down, score.
func (e *env) counts(id int64) (int, int, int) {
	e.t.Helper()
	m := e.must(e.do("GET", fmt.Sprintf("/v1/ideas/%d", id), "", nil), 200).json(e.t)
	return int(m["up"].(float64)), int(m["down"].(float64)), int(m["score"].(float64))
}

func (e *env) wantCounts(id int64, up, down, score int) {
	e.t.Helper()
	u, d, s := e.counts(id)
	if u != up || d != down || s != score {
		e.t.Fatalf("idea %d counts = up %d down %d score %d, want %d/%d/%d", id, u, d, s, up, down, score)
	}
}

func (e *env) voteRows(id int64) int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM votes WHERE idea_id = $1`, id).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) listIDs(query string) []int64 {
	e.t.Helper()
	m := e.must(e.do("GET", "/v1/ideas"+query, "", nil), 200).json(e.t)
	var ids []int64
	for _, it := range m["ideas"].([]any) {
		ids = append(ids, int64(it.(map[string]any)["id"].(float64)))
	}
	return ids
}

func contains(ids []int64, id int64) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------

func TestHealthz(t *testing.T) {
	e := newEnv(t)
	r := e.must(e.do("GET", "/healthz", "", nil), 200)
	if r.json(t)["status"] != "ok" {
		t.Fatalf("body = %s", r.body)
	}
	if r.header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing nosniff")
	}
}

func TestWidgetJS(t *testing.T) {
	e := newEnv(t)
	r := e.must(e.do("GET", "/widget.js", "", nil), 200)
	if ct := r.header.Get("Content-Type"); ct != "application/javascript; charset=utf-8" {
		t.Fatalf("content-type = %q", ct)
	}
	if r.header.Get("Access-Control-Allow-Origin") != "*" || r.header.Get("Cache-Control") != "public, max-age=300" {
		t.Fatalf("headers = %v", r.header)
	}
	etag := r.header.Get("ETag")
	if !regexp.MustCompile(`^"[0-9a-f]{64}"$`).MatchString(etag) {
		t.Fatalf("etag = %q", etag)
	}
	e.must(e.do("GET", "/widget.js", "", nil, "If-None-Match", etag), 304)
}

func TestSubmitIdeaIsPendingAndInvisible(t *testing.T) {
	e := newEnv(t)
	r := e.must(e.do("POST", "/v1/ideas", e.token("alice", true),
		map[string]string{"title": "  Dark mode  ", "body": " please "}), 201)
	m := r.json(t)
	if m["moderation_state"] != "pending" || m["title"] != "Dark mode" || m["body"] != "please" || m["status"] != "under_review" {
		t.Fatalf("created = %s", r.body)
	}
	id := int64(m["id"].(float64))

	if contains(e.listIDs(""), id) {
		t.Fatal("pending idea visible in public list")
	}
	e.mustErr(e.do("GET", fmt.Sprintf("/v1/ideas/%d", id), "", nil), 404, "not_found")
	// Voting on a pending idea is a 404 too.
	e.mustErr(e.vote(id, "bob", 1), 404, "not_found")

	// Author sees it as pending in /v1/me and /v1/me/ideas.
	me := e.must(e.do("GET", "/v1/me", e.token("alice", true), nil), 200).json(t)
	ideas := me["ideas"].([]any)
	if len(ideas) != 1 || ideas[0].(map[string]any)["moderation_state"] != "pending" || me["voter"] != true {
		t.Fatalf("/v1/me = %v", me)
	}
	mi := e.must(e.do("GET", "/v1/me/ideas", e.token("alice", true), nil), 200).json(t)
	if len(mi["ideas"].([]any)) != 1 {
		t.Fatalf("/v1/me/ideas = %v", mi)
	}

	e.approve(id)
	if !contains(e.listIDs(""), id) {
		t.Fatal("approved idea not in public list")
	}
	e.wantCounts(id, 0, 0, 0)
}

func TestRejectedAndMergedInvisible(t *testing.T) {
	e := newEnv(t)
	rej := e.submit("alice", "rejected one")
	e.must(e.do("POST", fmt.Sprintf("/v1/admin/ideas/%d/reject", rej), adminToken, nil), 200)
	target := e.approvedIdea("bob", "target")
	src := e.approvedIdea("carol", "source")
	e.must(e.do("POST", fmt.Sprintf("/v1/admin/ideas/%d/merge", src), adminToken, map[string]int64{"into_id": target}), 200)

	ids := e.listIDs("?limit=100")
	for _, id := range []int64{rej, src} {
		if contains(ids, id) {
			t.Fatalf("idea %d visible in list", id)
		}
		e.mustErr(e.do("GET", fmt.Sprintf("/v1/ideas/%d", id), "", nil), 404, "not_found")
	}
	if !contains(ids, target) {
		t.Fatal("target missing")
	}
	// /v1/me excludes rejected and merged own ideas.
	for _, sub := range []string{"alice", "carol"} {
		me := e.must(e.do("GET", "/v1/me/ideas", e.token(sub, true), nil), 200).json(t)
		if len(me["ideas"].([]any)) != 0 {
			t.Fatalf("%s /v1/me/ideas = %v", sub, me)
		}
	}
	// Rejected can be approved again.
	e.approve(rej)
	e.must(e.do("GET", fmt.Sprintf("/v1/ideas/%d", rej), "", nil), 200)
}

func TestVoteUniquenessSwitchRemove(t *testing.T) {
	e := newEnv(t)
	id := e.approvedIdea("alice", "idea")

	r := e.must(e.vote(id, "bob", 1), 200).json(t)
	if r["my_vote"].(float64) != 1 {
		t.Fatalf("my_vote = %v", r["my_vote"])
	}
	e.must(e.vote(id, "bob", 1), 200)
	e.wantCounts(id, 1, 0, 1)
	if n := e.voteRows(id); n != 1 {
		t.Fatalf("vote rows = %d, want 1", n)
	}

	e.must(e.vote(id, "bob", -1), 200)
	e.wantCounts(id, 0, 1, -1)
	if n := e.voteRows(id); n != 1 {
		t.Fatalf("vote rows = %d, want 1", n)
	}

	me := e.must(e.do("GET", "/v1/me/votes", e.token("bob", true), nil), 200).json(t)
	votes := me["votes"].([]any)
	if len(votes) != 1 || votes[0].(map[string]any)["value"].(float64) != -1 {
		t.Fatalf("my votes = %v", me)
	}

	path := fmt.Sprintf("/v1/ideas/%d/vote", id)
	dr := e.must(e.do("DELETE", path, e.token("bob", true), nil), 200).json(t)
	if dr["my_vote"].(float64) != 0 {
		t.Fatalf("my_vote = %v", dr["my_vote"])
	}
	e.wantCounts(id, 0, 0, 0)
	// Idempotent.
	e.must(e.do("DELETE", path, e.token("bob", true), nil), 200)
	if n := e.voteRows(id); n != 0 {
		t.Fatalf("vote rows = %d, want 0", n)
	}
}

func TestInvalidVoteValues(t *testing.T) {
	e := newEnv(t)
	id := e.approvedIdea("alice", "idea")
	path := fmt.Sprintf("/v1/ideas/%d/vote", id)
	for _, body := range []string{`{"value":2}`, `{"value":0}`, `{"value":1.5}`, `{"value":"1"}`, `{}`, `nope`, ``} {
		e.mustErr(e.do("PUT", path, e.token("bob", true), body), 400, "invalid_input")
	}
	e.mustErr(e.do("PUT", "/v1/ideas/999999/vote", e.token("bob", true), `{"value":1}`), 404, "not_found")
	e.mustErr(e.do("PUT", "/v1/ideas/abc/vote", e.token("bob", true), `{"value":1}`), 404, "not_found")
}

func TestConcurrentVotesSameSub(t *testing.T) {
	e := newEnv(t)
	id := e.approvedIdea("alice", "idea")
	tok := e.token("bob", true)
	var wg sync.WaitGroup
	statuses := make(chan int, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v := 1
			if i%2 == 1 {
				v = -1
			}
			req, _ := http.NewRequest("PUT", fmt.Sprintf("%s/v1/ideas/%d/vote", e.srv.URL, id),
				strings.NewReader(fmt.Sprintf(`{"value":%d}`, v)))
			req.Header.Set("Authorization", "Bearer "+tok)
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				statuses <- -1
				return
			}
			res.Body.Close()
			statuses <- res.StatusCode
		}(i)
	}
	wg.Wait()
	close(statuses)
	for s := range statuses {
		if s != 200 {
			t.Fatalf("concurrent vote status %d", s)
		}
	}
	if n := e.voteRows(id); n != 1 {
		t.Fatalf("vote rows = %d, want exactly 1", n)
	}
	u, d, _ := e.counts(id)
	if u+d != 1 {
		t.Fatalf("up+down = %d, want 1", u+d)
	}
}

func TestSelfVoteForbidden(t *testing.T) {
	e := newEnv(t)
	id := e.approvedIdea("alice", "mine")
	e.mustErr(e.vote(id, "alice", 1), 403, "own_idea")
	e.mustErr(e.do("DELETE", fmt.Sprintf("/v1/ideas/%d/vote", id), e.token("alice", true), nil), 403, "own_idea")
	e.wantCounts(id, 0, 0, 0)
}

func TestNotEligible(t *testing.T) {
	e := newEnv(t)
	id := e.approvedIdea("alice", "idea")
	tok := e.token("carol", false)
	e.mustErr(e.do("POST", "/v1/ideas", tok, map[string]string{"title": "x"}), 403, "not_eligible")
	e.mustErr(e.do("PUT", fmt.Sprintf("/v1/ideas/%d/vote", id), tok, map[string]int{"value": 1}), 403, "not_eligible")
	e.mustErr(e.do("DELETE", fmt.Sprintf("/v1/ideas/%d/vote", id), tok, nil), 403, "not_eligible")
	// Read-only /v1/me works.
	me := e.must(e.do("GET", "/v1/me", tok, nil), 200).json(t)
	if me["voter"] != false {
		t.Fatalf("/v1/me = %v", me)
	}
	// Eligibility is checked before input validation.
	e.mustErr(e.do("PUT", fmt.Sprintf("/v1/ideas/%d/vote", id), tok, `{"value":7}`), 403, "not_eligible")
}

func TestTokenValidation(t *testing.T) {
	e := newEnv(t)
	id := e.approvedIdea("alice", "idea")
	now := e.clock.Now()
	exp := strconv.FormatInt(now.Add(5*time.Minute).Unix(), 10)
	good := `{"iss":"okokumo","sub":"bob","voter":true,"exp":` + exp + `}`
	hs := []byte(`{"alg":"HS256","typ":"JWT"}`)
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	path := fmt.Sprintf("/v1/ideas/%d/vote", id)

	cases := []struct {
		name  string
		token string
		code  string
	}{
		{"expired beyond skew", hosttoken.MintAt(now.Add(-11*time.Minute), hostSecret, hostIssuer, "bob", true, 10*time.Minute), "token_expired"},
		{"bad signature", hosttoken.Sign("wrong-secret-wrong-secret-wrong-secret", hs, []byte(good)), "invalid_token"},
		{"wrong iss", hosttoken.MintAt(now, hostSecret, "doloop", "bob", true, time.Minute), "invalid_token"},
		{"alg none", enc(`{"alg":"none"}`) + "." + enc(good) + ".", "invalid_token"},
		{"alg HS512", hosttoken.Sign(hostSecret, []byte(`{"alg":"HS512","typ":"JWT"}`), []byte(good)), "invalid_token"},
		{"lifetime > 15m", hosttoken.MintAt(now, hostSecret, hostIssuer, "bob", true, 16*time.Minute), "invalid_token"},
		{"missing sub", hosttoken.Sign(hostSecret, hs, []byte(`{"iss":"okokumo","voter":true,"exp":`+exp+`}`)), "invalid_token"},
		{"garbage", "garbage", "invalid_token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e.mustErr(e.do("PUT", path, tc.token, `{"value":1}`), 401, tc.code)
			e.mustErr(e.do("GET", "/v1/me", tc.token, nil), 401, tc.code)
		})
	}
	t.Run("missing header", func(t *testing.T) {
		e.mustErr(e.do("PUT", path, "", `{"value":1}`), 401, "unauthorized")
		e.mustErr(e.do("POST", "/v1/ideas", "", `{"title":"x"}`), 401, "unauthorized")
		e.mustErr(e.do("GET", "/v1/me", "", nil), 401, "unauthorized")
		e.mustErr(e.do("GET", "/v1/me", "", nil, "Authorization", "Basic abc"), 401, "unauthorized")
	})
	t.Run("expired inside skew is accepted", func(t *testing.T) {
		tok := hosttoken.MintAt(now.Add(-10*time.Minute-20*time.Second), hostSecret, hostIssuer, "bob", true, 10*time.Minute)
		e.must(e.do("PUT", path, tok, `{"value":1}`), 200)
	})
	t.Run("clock advance expires token", func(t *testing.T) {
		tok := e.token("bob", true)
		e.must(e.do("GET", "/v1/me", tok, nil), 200)
		e.clock.Advance(10*time.Minute + 31*time.Second)
		e.mustErr(e.do("GET", "/v1/me", tok, nil), 401, "token_expired")
	})
}

func TestVotingClosed(t *testing.T) {
	e := newEnv(t)
	id := e.approvedIdea("alice", "idea")
	e.must(e.vote(id, "bob", 1), 200)
	for _, st := range []string{"shipped", "declined"} {
		e.must(e.do("PUT", fmt.Sprintf("/v1/admin/ideas/%d/status", id), adminToken, map[string]string{"status": st}), 200)
		e.mustErr(e.vote(id, "carol", 1), 409, "voting_closed")
	}
	e.must(e.do("PUT", fmt.Sprintf("/v1/admin/ideas/%d/status", id), adminToken, map[string]string{"status": "planned"}), 200)
	e.must(e.vote(id, "carol", 1), 200)
	e.wantCounts(id, 2, 0, 2)
	m := e.must(e.do("GET", fmt.Sprintf("/v1/ideas/%d", id), "", nil), 200).json(t)
	if m["status"] != "planned" {
		t.Fatalf("status = %v", m["status"])
	}
	e.mustErr(e.do("PUT", fmt.Sprintf("/v1/admin/ideas/%d/status", id), adminToken, map[string]string{"status": "done"}), 400, "invalid_input")
}

func TestMergeRecount(t *testing.T) {
	e := newEnv(t)
	target := e.approvedIdea("D", "target idea")
	source := e.approvedIdea("S", "source idea")

	// Source votes: A+1, B-1, C+1, D+1 (D is the target's author).
	e.must(e.vote(source, "A", 1), 200)
	e.must(e.vote(source, "B", -1), 200)
	e.must(e.vote(source, "C", 1), 200)
	e.must(e.vote(source, "D", 1), 200)
	// Target votes: A-1.
	e.must(e.vote(target, "A", -1), 200)

	r := e.must(e.do("POST", fmt.Sprintf("/v1/admin/ideas/%d/merge", source), adminToken,
		map[string]int64{"into_id": target}), 200).json(t)
	if r["moved"].(float64) != 2 || r["dropped"].(float64) != 2 {
		t.Fatalf("moved/dropped = %v/%v, want 2/2", r["moved"], r["dropped"])
	}
	idea := r["idea"].(map[string]any)
	if int64(idea["id"].(float64)) != target || idea["up"].(float64) != 1 || idea["down"].(float64) != 2 || idea["score"].(float64) != -1 {
		t.Fatalf("merge target = %v", idea)
	}
	e.wantCounts(target, 1, 2, -1)
	e.mustErr(e.do("GET", fmt.Sprintf("/v1/ideas/%d", source), "", nil), 404, "not_found")
	if n := e.voteRows(source); n != 0 {
		t.Fatalf("source vote rows = %d", n)
	}

	// A kept their target vote (-1).
	var a int
	e.pool.QueryRow(context.Background(), `SELECT value FROM votes WHERE idea_id=$1 AND voter_sub='A'`, target).Scan(&a)
	if a != -1 {
		t.Fatalf("A's target vote = %d, want -1", a)
	}

	// Admin view shows the source as merged into target.
	list := e.must(e.do("GET", "/v1/admin/ideas?moderation_state=merged", adminToken, nil), 200).json(t)
	merged := list["ideas"].([]any)
	if len(merged) != 1 || int64(merged[0].(map[string]any)["merged_into_id"].(float64)) != target {
		t.Fatalf("merged list = %v", list)
	}

	// Rule checks.
	e.mustErr(e.do("POST", fmt.Sprintf("/v1/admin/ideas/%d/merge", target), adminToken, map[string]int64{"into_id": target}), 400, "invalid_input")
	e.mustErr(e.do("POST", fmt.Sprintf("/v1/admin/ideas/%d/merge", source), adminToken, map[string]int64{"into_id": target}), 400, "invalid_input")
	other := e.approvedIdea("X", "other")
	e.mustErr(e.do("POST", fmt.Sprintf("/v1/admin/ideas/%d/merge", other), adminToken, map[string]int64{"into_id": source}), 400, "invalid_input")
	e.mustErr(e.do("POST", fmt.Sprintf("/v1/admin/ideas/%d/merge", other), adminToken, map[string]int64{"into_id": 999999}), 404, "not_found")
}

func TestChainedMergeRepoints(t *testing.T) {
	e := newEnv(t)
	a := e.approvedIdea("u1", "a")
	b := e.approvedIdea("u2", "b")
	c := e.approvedIdea("u3", "c")
	e.must(e.vote(a, "v1", 1), 200)
	e.must(e.vote(b, "v2", 1), 200)

	// a -> b, then b -> c: a must now point at c and all votes end on c.
	e.must(e.do("POST", fmt.Sprintf("/v1/admin/ideas/%d/merge", a), adminToken, map[string]int64{"into_id": b}), 200)
	e.wantCounts(b, 2, 0, 2)
	e.must(e.do("POST", fmt.Sprintf("/v1/admin/ideas/%d/merge", b), adminToken, map[string]int64{"into_id": c}), 200)
	e.wantCounts(c, 2, 0, 2)

	var into int64
	if err := e.pool.QueryRow(context.Background(), `SELECT merged_into_id FROM ideas WHERE id=$1`, a).Scan(&into); err != nil {
		t.Fatal(err)
	}
	if into != c {
		t.Fatalf("a.merged_into_id = %d, want %d", into, c)
	}
	// Deleting c cascades to the ideas merged into it.
	e.must(e.do("DELETE", fmt.Sprintf("/v1/admin/ideas/%d", c), adminToken, nil), 204)
	var n int
	e.pool.QueryRow(context.Background(), `SELECT count(*) FROM ideas`).Scan(&n)
	if n != 0 {
		t.Fatalf("ideas left = %d, want 0", n)
	}
}

func TestSubmissionRateLimit(t *testing.T) {
	e := newEnv(t)
	tok := func() string { return e.token("alice", true) }
	for i := 0; i < 5; i++ {
		e.must(e.do("POST", "/v1/ideas", tok(), map[string]string{"title": fmt.Sprintf("idea %d", i)}), 201)
		e.clock.Advance(time.Minute)
	}
	r := e.do("POST", "/v1/ideas", tok(), map[string]string{"title": "sixth"})
	e.mustErr(r, 429, "rate_limited")
	// Oldest was submitted 5 minutes ago => it ages out in 24h - 5m.
	want := int((24*time.Hour - 5*time.Minute) / time.Second)
	if got, _ := strconv.Atoi(r.header.Get("Retry-After")); got != want {
		t.Fatalf("Retry-After = %q, want %d", r.header.Get("Retry-After"), want)
	}
	// Another user is unaffected.
	e.must(e.do("POST", "/v1/ideas", e.token("bob", true), map[string]string{"title": "bob idea"}), 201)

	// Once the oldest ages out one slot frees up, and only one.
	e.clock.Advance(24*time.Hour - 5*time.Minute + time.Second)
	e.must(e.do("POST", "/v1/ideas", tok(), map[string]string{"title": "after window"}), 201)
	e.mustErr(e.do("POST", "/v1/ideas", tok(), map[string]string{"title": "again"}), 429, "rate_limited")
}

func TestVoteRateLimit(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.VoteLimitPerMinute = 3 })
	id := e.approvedIdea("alice", "idea")
	e.must(e.vote(id, "bob", 1), 200)
	e.must(e.vote(id, "bob", -1), 200)
	e.must(e.do("DELETE", fmt.Sprintf("/v1/ideas/%d/vote", id), e.token("bob", true), nil), 200)
	r := e.vote(id, "bob", 1)
	e.mustErr(r, 429, "rate_limited")
	if ra, _ := strconv.Atoi(r.header.Get("Retry-After")); ra < 1 || ra > 60 {
		t.Fatalf("Retry-After = %q", r.header.Get("Retry-After"))
	}
	// Other users have their own window.
	e.must(e.vote(id, "carol", 1), 200)
	e.clock.Advance(61 * time.Second)
	e.must(e.vote(id, "bob", 1), 200)
}

func TestCORS(t *testing.T) {
	e := newEnv(t)
	// Allowed origin, simple request.
	r := e.must(e.do("GET", "/v1/ideas", "", nil, "Origin", allowOrigin), 200)
	if r.header.Get("Access-Control-Allow-Origin") != allowOrigin || !strings.Contains(r.header.Get("Vary"), "Origin") {
		t.Fatalf("allowed headers = %v", r.header)
	}
	if r.header.Get("Access-Control-Allow-Credentials") != "" {
		t.Fatal("credentials must not be allowed")
	}
	// Disallowed origin: served, no CORS headers.
	r = e.must(e.do("GET", "/v1/ideas", "", nil, "Origin", "https://evil.example"), 200)
	if r.header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("disallowed origin got ACAO")
	}
	// Preflight allowed.
	r = e.do("OPTIONS", "/v1/ideas/1/vote", "", nil, "Origin", allowOrigin,
		"Access-Control-Request-Method", "PUT", "Access-Control-Request-Headers", "authorization,content-type")
	if r.status != 204 || r.header.Get("Access-Control-Allow-Origin") != allowOrigin ||
		!strings.Contains(r.header.Get("Access-Control-Allow-Methods"), "PUT") ||
		!strings.Contains(r.header.Get("Access-Control-Allow-Headers"), "Authorization") ||
		r.header.Get("Access-Control-Max-Age") != "600" {
		t.Fatalf("preflight = %d %v", r.status, r.header)
	}
	// Preflight disallowed.
	r = e.do("OPTIONS", "/v1/ideas", "", nil, "Origin", "https://evil.example", "Access-Control-Request-Method", "POST")
	e.mustErr(r, 403, "forbidden_origin")
	if r.header.Get("Access-Control-Allow-Origin") != "" || r.header.Get("Access-Control-Allow-Methods") != "" {
		t.Fatal("rejected preflight has CORS headers")
	}
	// No Origin: normal.
	r = e.must(e.do("GET", "/v1/ideas", "", nil), 200)
	if r.header.Get("Access-Control-Allow-Origin") != "" || r.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("no-origin headers = %v", r.header)
	}
	// Admin API never gets CORS headers.
	r = e.must(e.do("GET", "/v1/admin/ideas", adminToken, nil, "Origin", allowOrigin), 200)
	if r.header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("admin API returned CORS headers")
	}
}

func TestAdminAuth(t *testing.T) {
	e := newEnv(t)
	for _, tok := range []string{"", "wrong", hostSecret, adminToken + "x"} {
		e.mustErr(e.do("GET", "/v1/admin/ideas", tok, nil), 401, "unauthorized")
		e.mustErr(e.do("POST", "/v1/admin/ideas/1/approve", tok, nil), 401, "unauthorized")
		e.mustErr(e.do("DELETE", "/v1/admin/users/bob", tok, nil), 401, "unauthorized")
	}
	e.must(e.do("GET", "/v1/admin/ideas", adminToken, nil), 200)
	e.mustErr(e.do("GET", "/v1/admin/ideas?moderation_state=bogus", adminToken, nil), 400, "invalid_input")
}

func TestAdminCRUD(t *testing.T) {
	e := newEnv(t)
	r := e.must(e.do("POST", "/v1/admin/ideas", adminToken, map[string]string{"title": "Roadmap item", "body": "x", "status": "planned"}), 201).json(t)
	id := int64(r["id"].(float64))
	if r["moderation_state"] != "approved" || r["author_sub"] != nil || r["status"] != "planned" {
		t.Fatalf("admin create = %v", r)
	}
	e.must(e.do("GET", fmt.Sprintf("/v1/ideas/%d", id), "", nil), 200)

	r = e.must(e.do("PATCH", fmt.Sprintf("/v1/admin/ideas/%d", id), adminToken, map[string]string{"title": " New title "}), 200).json(t)
	if r["title"] != "New title" || r["body"] != "x" {
		t.Fatalf("patch = %v", r)
	}
	e.mustErr(e.do("PATCH", fmt.Sprintf("/v1/admin/ideas/%d", id), adminToken, map[string]string{"title": ""}), 400, "invalid_input")

	pend := e.submit("alice", "pending")
	all := e.must(e.do("GET", "/v1/admin/ideas", adminToken, nil), 200).json(t)["ideas"].([]any)
	if len(all) != 1 || all[0].(map[string]any)["author_sub"] != "alice" {
		t.Fatalf("default pending list = %v", all)
	}
	all = e.must(e.do("GET", "/v1/admin/ideas?moderation_state=all", adminToken, nil), 200).json(t)["ideas"].([]any)
	if len(all) != 2 {
		t.Fatalf("all list = %v", all)
	}
	r = e.must(e.do("POST", fmt.Sprintf("/v1/admin/ideas/%d/reject", pend), adminToken, nil), 200).json(t)
	if r["moderation_state"] != "rejected" {
		t.Fatalf("reject = %v", r)
	}
	e.must(e.do("DELETE", fmt.Sprintf("/v1/admin/ideas/%d", id), adminToken, nil), 204)
	e.mustErr(e.do("DELETE", fmt.Sprintf("/v1/admin/ideas/%d", id), adminToken, nil), 404, "not_found")
	e.mustErr(e.do("POST", "/v1/admin/ideas/999999/approve", adminToken, nil), 404, "not_found")
}

func TestGDPRDelete(t *testing.T) {
	e := newEnv(t)
	a := e.approvedIdea("alice", "a")
	b := e.approvedIdea("dave", "dave's idea")
	e.must(e.vote(a, "dave", 1), 200)
	e.must(e.vote(a, "bob", 1), 200)
	e.must(e.vote(b, "bob", -1), 200)
	e.wantCounts(a, 2, 0, 2)

	r := e.must(e.do("DELETE", "/v1/admin/users/dave", adminToken, nil), 200).json(t)
	if r["votes_deleted"].(float64) != 1 || r["ideas_anonymised"].(float64) != 1 {
		t.Fatalf("gdpr = %v", r)
	}
	e.wantCounts(a, 1, 0, 1)
	e.wantCounts(b, 0, 1, -1) // idea stays, anonymised
	var author *string
	e.pool.QueryRow(context.Background(), `SELECT author_sub FROM ideas WHERE id=$1`, b).Scan(&author)
	if author != nil {
		t.Fatalf("author_sub = %q, want NULL", *author)
	}
	var n int
	e.pool.QueryRow(context.Background(), `SELECT count(*) FROM votes WHERE voter_sub='dave'`).Scan(&n)
	if n != 0 {
		t.Fatalf("dave votes = %d", n)
	}
	me := e.must(e.do("GET", "/v1/me", e.token("dave", true), nil), 200).json(t)
	if len(me["votes"].([]any)) != 0 || len(me["ideas"].([]any)) != 0 {
		t.Fatalf("dave /v1/me = %v", me)
	}
}

func TestSizeLimits(t *testing.T) {
	e := newEnv(t)
	tok := e.token("alice", true)
	e.mustErr(e.do("POST", "/v1/ideas", tok, map[string]string{"title": strings.Repeat("é", 121)}), 400, "invalid_input")
	e.must(e.do("POST", "/v1/ideas", tok, map[string]string{"title": strings.Repeat("é", 120)}), 201)
	e.mustErr(e.do("POST", "/v1/ideas", tok, map[string]string{"title": "ok", "body": strings.Repeat("ü", 2001)}), 400, "invalid_input")
	e.must(e.do("POST", "/v1/ideas", tok, map[string]string{"title": "ok", "body": strings.Repeat("ü", 2000)}), 201)
	e.mustErr(e.do("POST", "/v1/ideas", tok, map[string]string{"title": "   "}), 400, "invalid_input")
	e.mustErr(e.do("POST", "/v1/ideas", tok, map[string]string{"body": "no title"}), 400, "invalid_input")
	e.mustErr(e.do("POST", "/v1/ideas", tok, map[string]string{"title": "ok", "body": strings.Repeat("x", 20<<10)}), 413, "payload_too_large")
	e.mustErr(e.do("POST", "/v1/ideas", tok, `{"title":`), 400, "invalid_input")
	// Unknown fields are ignored.
	e.must(e.do("POST", "/v1/ideas", tok, `{"title":"fine","extra":true}`), 201)
}

// NUL and other C0 control characters used to reach Postgres (NUL => 500).
// They are now a 400 everywhere a title or body is accepted; LF and TAB are
// allowed and CR/CRLF is normalised to \n (HTML textareas submit CRLF).
func TestControlCharacters(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.SubmitLimitPerDay = 100 })
	tok := e.token("alice", true)
	for _, bad := range []string{"\u0000", "\u0001", "\u0007", "\u0008", "\u000b", "\u001b", "\u001f"} {
		jq, _ := json.Marshal(bad) // JSON escape, e.g. "\u0000"
		esc := string(jq[1 : len(jq)-1])
		e.mustErr(e.do("POST", "/v1/ideas", tok, `{"title":"a`+esc+`b"}`), 400, "invalid_input")
		e.mustErr(e.do("POST", "/v1/ideas", tok, `{"title":"ok","body":"x`+esc+`y"}`), 400, "invalid_input")
		e.mustErr(e.do("POST", "/v1/admin/ideas", adminToken, `{"title":"a`+esc+`b"}`), 400, "invalid_input")
	}
	const tab = "\x09"
	r := e.must(e.do("POST", "/v1/ideas", tok, map[string]string{"title": "tab" + tab + "here", "body": "line1\r\nline2\rline3\n" + tab + "indented"}), 201).json(t)
	if r["title"] != "tab"+tab+"here" || r["body"] != "line1\nline2\nline3\n"+tab+"indented" {
		t.Fatalf("allowed whitespace mangled: %q / %q", r["title"], r["body"])
	}
	id := int64(r["id"].(float64))
	e.mustErr(e.do("PATCH", fmt.Sprintf("/v1/admin/ideas/%d", id), adminToken, `{"body":"x\u0000"}`), 400, "invalid_input")

	// Admin HTML form: rejected with an error flash, nothing stored.
	c, csrf := adminLogin(t, e)
	res, _ := postForm(t, c, e.srv.URL+"/admin/ideas", url.Values{"csrf": {csrf}, "title": {"nul\x00title"}})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("admin form status = %d", res.StatusCode)
	}
	var n int
	e.pool.QueryRow(context.Background(), `SELECT count(*) FROM ideas WHERE title LIKE 'nul%'`).Scan(&n)
	if n != 0 {
		t.Fatalf("control-char title stored")
	}
}

func TestPublicJSONNeverLeaksAuthor(t *testing.T) {
	e := newEnv(t)
	id := e.approvedIdea("alice-secret-sub", "idea")
	e.must(e.vote(id, "bob", 1), 200)
	for _, r := range []resp{
		e.do("GET", "/v1/ideas", "", nil),
		e.do("GET", "/v1/ideas?sort=new", "", nil),
		e.do("GET", fmt.Sprintf("/v1/ideas/%d", id), "", nil),
		e.vote(id, "carol", 1),
		e.do("DELETE", fmt.Sprintf("/v1/ideas/%d/vote", id), e.token("carol", true), nil),
		e.do("GET", "/v1/me", e.token("bob", true), nil),
	} {
		e.must(r, 200)
		if bytes.Contains(r.body, []byte("author_sub")) || bytes.Contains(r.body, []byte("alice-secret-sub")) {
			t.Fatalf("public response leaks author: %s", r.body)
		}
		if r.header.Get("Cache-Control") != "no-store" {
			t.Fatalf("Cache-Control = %q", r.header.Get("Cache-Control"))
		}
	}
	pub := e.must(e.do("GET", fmt.Sprintf("/v1/ideas/%d", id), "", nil), 200).json(t)
	if _, ok := pub["moderation_state"]; ok {
		t.Fatal("public idea exposes moderation_state")
	}
	if _, err := time.Parse(time.RFC3339, pub["created_at"].(string)); err != nil {
		t.Fatalf("created_at not RFC3339: %v", pub["created_at"])
	}
}

func TestListSortFilterPaging(t *testing.T) {
	e := newEnv(t)
	low := e.approvedIdea("u1", "low")
	e.clock.Advance(time.Second)
	high := e.approvedIdea("u2", "high")
	e.clock.Advance(time.Second)
	mid := e.approvedIdea("u3", "mid")
	e.must(e.vote(high, "a", 1), 200)
	e.must(e.vote(high, "b", 1), 200)
	e.must(e.vote(mid, "a", 1), 200)
	e.must(e.vote(low, "a", -1), 200)
	e.must(e.do("PUT", fmt.Sprintf("/v1/admin/ideas/%d/status", mid), adminToken, map[string]string{"status": "planned"}), 200)

	if got := e.listIDs(""); fmt.Sprint(got) != fmt.Sprint([]int64{high, mid, low}) {
		t.Fatalf("top order = %v", got)
	}
	if got := e.listIDs("?sort=new"); fmt.Sprint(got) != fmt.Sprint([]int64{mid, high, low}) {
		t.Fatalf("new order = %v", got)
	}
	if got := e.listIDs("?status=planned"); fmt.Sprint(got) != fmt.Sprint([]int64{mid}) {
		t.Fatalf("planned = %v", got)
	}
	m := e.must(e.do("GET", "/v1/ideas?limit=1&offset=1", "", nil), 200).json(t)
	if m["total"].(float64) != 3 || len(m["ideas"].([]any)) != 1 {
		t.Fatalf("paging = %v", m)
	}
	e.mustErr(e.do("GET", "/v1/ideas?status=bogus", "", nil), 400, "invalid_input")
	e.mustErr(e.do("GET", "/v1/ideas?sort=hot", "", nil), 400, "invalid_input")
	e.mustErr(e.do("GET", "/v1/ideas?limit=101", "", nil), 400, "invalid_input")
	e.mustErr(e.do("GET", "/v1/nope", "", nil), 404, "not_found")
}

// --- admin HTML --------------------------------------------------------------

func adminClient(t *testing.T) *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func postForm(t *testing.T, c *http.Client, u string, vals url.Values, headers ...string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", u, strings.NewReader(vals.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

func getPage(t *testing.T, c *http.Client, u string) (*http.Response, string) {
	t.Helper()
	res, err := c.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

var csrfRe = regexp.MustCompile(`name="csrf" value="([0-9a-f]{64})"`)

func adminLogin(t *testing.T, e *env) (*http.Client, string) {
	t.Helper()
	c := adminClient(t)
	res, _ := postForm(t, c, e.srv.URL+"/admin/login", url.Values{"token": {adminToken}})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("login status = %d", res.StatusCode)
	}
	var sess *http.Cookie
	for _, ck := range res.Cookies() {
		if ck.Name == "fv_admin" {
			sess = ck
		}
	}
	if sess == nil || !sess.HttpOnly || sess.SameSite != http.SameSiteStrictMode || sess.Path != "/admin" {
		t.Fatalf("session cookie = %+v", sess)
	}
	_, body := getPage(t, c, e.srv.URL+"/admin")
	m := csrfRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no csrf token in page")
	}
	return c, m[1]
}

func TestAdminHTMLLoginAndEscaping(t *testing.T) {
	e := newEnv(t)
	id := e.submit("alice", `<script>alert("x")</script>`)

	// Not logged in: login form only, with security headers.
	c := adminClient(t)
	res, body := getPage(t, c, e.srv.URL+"/admin")
	if res.StatusCode != 200 || !strings.Contains(body, `name="token"`) || strings.Contains(body, "alert") {
		t.Fatalf("anonymous page: %d", res.StatusCode)
	}
	if !strings.Contains(res.Header.Get("Content-Security-Policy"), "frame-ancestors 'none'") ||
		res.Header.Get("X-Frame-Options") != "DENY" || res.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("security headers = %v", res.Header)
	}
	res, _ = postForm(t, c, e.srv.URL+"/admin/login", url.Values{"token": {"wrong"}})
	if res.StatusCode != 401 {
		t.Fatalf("wrong login status = %d", res.StatusCode)
	}

	c, csrf := adminLogin(t, e)
	_, body = getPage(t, c, e.srv.URL+"/admin")
	if strings.Contains(body, `<script>alert`) {
		t.Fatal("title not escaped")
	}
	if !strings.Contains(body, `&lt;script&gt;alert(&#34;x&#34;)&lt;/script&gt;`) {
		t.Fatalf("escaped title missing from page")
	}

	// Approve via form (PRG redirect), then it is public.
	res, _ = postForm(t, c, fmt.Sprintf("%s/admin/ideas/%d/approve", e.srv.URL, id), url.Values{"csrf": {csrf}})
	if res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(res.Header.Get("Location"), "/admin") {
		t.Fatalf("approve = %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	e.must(e.do("GET", fmt.Sprintf("/v1/ideas/%d", id), "", nil), 200)

	// Status change and GDPR form.
	postForm(t, c, fmt.Sprintf("%s/admin/ideas/%d/status", e.srv.URL, id), url.Values{"csrf": {csrf}, "status": {"planned"}})
	if m := e.must(e.do("GET", fmt.Sprintf("/v1/ideas/%d", id), "", nil), 200).json(t); m["status"] != "planned" {
		t.Fatalf("status = %v", m["status"])
	}
	res, _ = postForm(t, c, e.srv.URL+"/admin/users/delete", url.Values{"csrf": {csrf}, "sub": {"alice"}})
	if res.StatusCode != http.StatusSeeOther || !strings.Contains(res.Header.Get("Location"), "anonymised") {
		t.Fatalf("gdpr form = %d %s", res.StatusCode, res.Header.Get("Location"))
	}

	// Logout clears the session.
	postForm(t, c, e.srv.URL+"/admin/logout", url.Values{"csrf": {csrf}})
	_, body = getPage(t, c, e.srv.URL+"/admin")
	if !strings.Contains(body, `name="token"`) {
		t.Fatal("still logged in after logout")
	}
}

func TestAdminHTMLCSRF(t *testing.T) {
	e := newEnv(t)
	id := e.submit("alice", "idea")
	c, csrf := adminLogin(t, e)
	u := fmt.Sprintf("%s/admin/ideas/%d/approve", e.srv.URL, id)

	res, _ := postForm(t, c, u, url.Values{})
	if res.StatusCode != 403 {
		t.Fatalf("missing csrf status = %d", res.StatusCode)
	}
	res, _ = postForm(t, c, u, url.Values{"csrf": {strings.Repeat("0", 64)}})
	if res.StatusCode != 403 {
		t.Fatalf("wrong csrf status = %d", res.StatusCode)
	}
	res, _ = postForm(t, c, u, url.Values{"csrf": {csrf}}, "Origin", "https://evil.example")
	if res.StatusCode != 403 {
		t.Fatalf("cross-origin status = %d", res.StatusCode)
	}
	// Without a session cookie at all.
	res, _ = postForm(t, adminClient(t), u, url.Values{"csrf": {csrf}})
	if res.StatusCode != 401 {
		t.Fatalf("no session status = %d", res.StatusCode)
	}
	e.mustErr(e.do("GET", fmt.Sprintf("/v1/ideas/%d", id), "", nil), 404, "not_found")

	// Same-origin with valid token works.
	res, _ = postForm(t, c, u, url.Values{"csrf": {csrf}}, "Origin", e.srv.URL)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("valid status = %d", res.StatusCode)
	}
}

func TestAdminSessionExpires(t *testing.T) {
	e := newEnv(t)
	c, _ := adminLogin(t, e)
	e.clock.Advance(12*time.Hour + time.Second)
	_, body := getPage(t, c, e.srv.URL+"/admin")
	if !strings.Contains(body, `name="token"`) {
		t.Fatal("session did not expire after 12h")
	}
}
