// Package hosttoken verifies and mints the short-lived HS256 JWTs that a host
// product hands to the FeatureVote widget to identify the current user.
//
// Only the compact JWS form with {"alg":"HS256"} is accepted; the
// implementation is deliberately small and does not depend on a JWT library.
package hosttoken

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

// MaxLifetime is the longest token lifetime the service accepts, checked
// both as exp - iat (what the host minted) and as exp - now (plus skew).
const MaxLifetime = 15 * time.Minute

// maxSubLen bounds the opaque subject identifier.
const maxSubLen = 255

// ValidSubject reports whether sub is a usable subject: 1 to 255 bytes of
// valid UTF-8 with no C0 control characters (NUL in particular, which
// Postgres text columns reject). Verify enforces it on tokens; the admin
// GDPR endpoint uses it to reject subs no valid token could carry.
func ValidSubject(sub string) bool {
	if sub == "" || len(sub) > maxSubLen || !utf8.ValidString(sub) {
		return false
	}
	for i := 0; i < len(sub); i++ {
		if sub[i] < 0x20 {
			return false
		}
	}
	return true
}

// maxTokenLen bounds the raw token size before any decoding work.
const maxTokenLen = 4096

var (
	// ErrInvalid is returned for any malformed, unsigned, mis-signed or
	// otherwise unacceptable token.
	ErrInvalid = errors.New("invalid token")
	// ErrExpired is returned when exp (plus the clock skew) is in the past.
	ErrExpired = errors.New("token expired")
)

// Claims are the verified claims of a host token.
type Claims struct {
	Issuer    string    `json:"iss"`
	Subject   string    `json:"sub"`
	Voter     bool      `json:"voter"`
	ExpiresAt time.Time `json:"exp"`
	IssuedAt  time.Time `json:"iat"`
	// Audience is the aud value matched against Verifier.Audience (empty
	// when no audience is configured). Encode writes it as the aud claim,
	// omitted when empty.
	Audience string `json:"aud,omitempty"`
}

// Verifier checks host tokens.
type Verifier struct {
	Secret []byte
	Issuer string
	// Audience, when non-empty, must appear in the token's aud claim (a
	// string or an array of strings); tokens without aud are then rejected.
	// When empty, aud is not checked, so hosts can start sending it first.
	Audience string
	Skew     time.Duration
	// Now returns the current time; nil means time.Now.
	Now func() time.Time
}

var b64 = base64.RawURLEncoding.Strict()

// Verify parses and validates token. Errors wrap ErrInvalid or ErrExpired and
// never contain the token itself.
func (v *Verifier) Verify(token string) (Claims, error) {
	if len(v.Secret) == 0 || len(token) == 0 || len(token) > maxTokenLen {
		return Claims{}, ErrInvalid
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, ErrInvalid
	}

	// Header: alg must be exactly HS256; typ, if present, must be JWT.
	hb, err := b64.DecodeString(parts[0])
	if err != nil {
		return Claims{}, ErrInvalid
	}
	var header map[string]json.RawMessage
	if err := json.Unmarshal(hb, &header); err != nil {
		return Claims{}, ErrInvalid
	}
	var alg string
	if raw, ok := header["alg"]; !ok || json.Unmarshal(raw, &alg) != nil || alg != "HS256" {
		return Claims{}, ErrInvalid
	}
	if raw, ok := header["typ"]; ok {
		var typ string
		if json.Unmarshal(raw, &typ) != nil || typ != "JWT" {
			return Claims{}, ErrInvalid
		}
	}
	if _, ok := header["crit"]; ok { // we understand no extensions
		return Claims{}, ErrInvalid
	}

	// Signature over "<header>.<payload>".
	sig, err := b64.DecodeString(parts[2])
	if err != nil {
		return Claims{}, ErrInvalid
	}
	mac := hmac.New(sha256.New, v.Secret)
	mac.Write([]byte(parts[0] + "." + parts[1]))
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return Claims{}, ErrInvalid
	}

	// Payload.
	pb, err := b64.DecodeString(parts[1])
	if err != nil {
		return Claims{}, ErrInvalid
	}
	dec := json.NewDecoder(bytes.NewReader(pb))
	dec.UseNumber()
	var payload map[string]any
	if err := dec.Decode(&payload); err != nil || payload == nil {
		return Claims{}, ErrInvalid
	}

	var c Claims
	iss, ok := payload["iss"].(string)
	if !ok || iss != v.Issuer {
		return Claims{}, ErrInvalid
	}
	c.Issuer = iss
	sub, ok := payload["sub"].(string)
	if !ok || !ValidSubject(sub) {
		return Claims{}, ErrInvalid
	}
	c.Subject = sub
	c.Voter, _ = payload["voter"].(bool) // missing or non-bool => false

	now := time.Now()
	if v.Now != nil {
		now = v.Now()
	}
	exp, ok := numericDate(payload["exp"])
	if !ok {
		return Claims{}, ErrInvalid
	}
	c.ExpiresAt = exp
	if now.After(exp.Add(v.Skew)) {
		return Claims{}, ErrExpired
	}
	if exp.After(now.Add(MaxLifetime + v.Skew)) {
		return Claims{}, ErrInvalid // host minted a too long-lived token
	}
	// iat is required: without it a leaked token's lifetime is bounded only
	// by exp - now, which an attacker holding the secret could keep fresh.
	// exp - iat is host-controlled (no clock involved), so no skew applies.
	iat, ok := numericDate(payload["iat"])
	if !ok || iat.After(now.Add(v.Skew)) || exp.Sub(iat) > MaxLifetime {
		return Claims{}, ErrInvalid
	}
	c.IssuedAt = iat
	if raw, present := payload["nbf"]; present {
		nbf, ok := numericDate(raw)
		if !ok || nbf.After(now.Add(v.Skew)) {
			return Claims{}, ErrInvalid
		}
	}
	if v.Audience != "" {
		if !audienceContains(payload["aud"], v.Audience) {
			return Claims{}, ErrInvalid
		}
		c.Audience = v.Audience
	}
	return c, nil
}

// audienceContains reports whether the aud claim (RFC 7519 section 4.1.3: a
// string or an array of strings) contains want. Any other shape fails.
func audienceContains(aud any, want string) bool {
	switch a := aud.(type) {
	case string:
		return a == want
	case []any:
		for _, e := range a {
			if s, ok := e.(string); ok && s == want {
				return true
			}
		}
	}
	return false
}

// numericDate converts a JSON number (seconds since the epoch, possibly
// fractional) to a time.
func numericDate(v any) (time.Time, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return time.Time{}, false
	}
	f, err := n.Float64()
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f > 1e11 {
		return time.Time{}, false
	}
	sec, frac := math.Modf(f)
	return time.Unix(int64(sec), int64(frac*1e9)), true
}

// Mint creates a signed host token valid for ttl from now.
func Mint(secret, issuer, sub string, voter bool, ttl time.Duration) string {
	return MintAt(time.Now(), secret, issuer, sub, voter, ttl)
}

// MintAt is Mint with an explicit issue time (for tests and tools).
func MintAt(now time.Time, secret, issuer, sub string, voter bool, ttl time.Duration) string {
	return Encode(secret, Claims{Issuer: issuer, Subject: sub, Voter: voter, IssuedAt: now, ExpiresAt: now.Add(ttl)})
}

// Encode signs c as a host token with unix-second iat/exp and, when
// c.Audience is set, an aud claim. It performs no validation.
func Encode(secret string, c Claims) string {
	header := `{"alg":"HS256","typ":"JWT"}`
	payload, _ := json.Marshal(struct {
		Iss   string `json:"iss"`
		Sub   string `json:"sub"`
		Aud   string `json:"aud,omitempty"`
		Voter bool   `json:"voter"`
		Iat   int64  `json:"iat"`
		Exp   int64  `json:"exp"`
	}{c.Issuer, c.Subject, c.Audience, c.Voter, c.IssuedAt.Unix(), c.ExpiresAt.Unix()})
	return Sign(secret, []byte(header), payload)
}

// Sign builds a compact JWS from raw header and payload JSON using
// HMAC-SHA256. It performs no validation; tests use it to craft bad tokens.
func Sign(secret string, header, payload []byte) string {
	signing := b64.EncodeToString(header) + "." + b64.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signing))
	return signing + "." + b64.EncodeToString(mac.Sum(nil))
}
