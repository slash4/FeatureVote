package hosttoken

// This file carries, verbatim, the stdlib-only minting function published in
// docs/INTEGRATION.md so that the documented host code is proven to produce
// tokens this service accepts. Keep the two in sync.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- begin INTEGRATION.md snippet ---

// MintFeatureVoteToken returns a short-lived HS256 JWT identifying the
// current user to FeatureVote. sub must be an opaque, stable user id (never an
// email); voter is the host's eligibility decision. audience is the
// instance's FV_AUDIENCE (FEATURE_VOTE_AUDIENCE on the host); "" omits aud.
func MintFeatureVoteToken(secret, issuer, audience, sub string, voter bool, ttl time.Duration) string {
	enc := base64.RawURLEncoding
	now := time.Now()
	header := enc.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload := map[string]any{
		"iss":   issuer,
		"sub":   sub,
		"voter": voter,
		"iat":   now.Unix(),
		"exp":   now.Add(ttl).Unix(),
	}
	if audience != "" {
		payload["aud"] = audience
	}
	claims, _ := json.Marshal(payload)
	signingInput := header + "." + enc.EncodeToString(claims)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signingInput))
	return signingInput + "." + enc.EncodeToString(mac.Sum(nil))
}

// --- end INTEGRATION.md snippet ---

func TestIntegrationDocGoSnippet(t *testing.T) {
	v := &Verifier{Secret: []byte(secret), Issuer: issuer, Skew: 30 * time.Second}
	for _, voter := range []bool{true, false} {
		tok := MintFeatureVoteToken(secret, issuer, "", "usr_8f2c", voter, 10*time.Minute)
		c, err := v.Verify(tok)
		if err != nil {
			t.Fatalf("doc snippet token rejected: %v", err)
		}
		if c.Subject != "usr_8f2c" || c.Voter != voter {
			t.Fatalf("claims = %+v", c)
		}
	}
	// With an audience on both sides the token is accepted; a token minted
	// for another instance is not.
	v.Audience = "feedback.okokumo.com"
	if _, err := v.Verify(MintFeatureVoteToken(secret, issuer, "feedback.okokumo.com", "usr_8f2c", true, 10*time.Minute)); err != nil {
		t.Fatalf("doc snippet token with aud rejected: %v", err)
	}
	if _, err := v.Verify(MintFeatureVoteToken(secret, issuer, "feedback.getdoloop.com", "usr_8f2c", true, 10*time.Minute)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("foreign aud: err = %v, want ErrInvalid", err)
	}
	// And the doc-recommended TTL must never exceed the service maximum.
	if 10*time.Minute > MaxLifetime {
		t.Fatal("recommended TTL exceeds MaxLifetime")
	}
}

// TestIntegrationDocContainsGoSnippet keeps docs/INTEGRATION.md honest: the
// published function must be byte-identical to the one tested above.
func TestIntegrationDocContainsGoSnippet(t *testing.T) {
	src, err := os.ReadFile("example_test.go")
	if err != nil {
		t.Fatal(err)
	}
	const begin, end = "// --- begin INTEGRATION.md snippet ---\n\n", "\n// --- end INTEGRATION.md snippet ---"
	s := string(src)
	i, j := strings.Index(s, begin), strings.Index(s, end)
	if i < 0 || j < i {
		t.Fatal("snippet markers not found")
	}
	snippet := s[i+len(begin) : j]
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "INTEGRATION.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doc), snippet) {
		t.Fatalf("docs/INTEGRATION.md does not contain the tested MintFeatureVoteToken snippet verbatim:\n%s", snippet)
	}
}
