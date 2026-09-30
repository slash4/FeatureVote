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
)

// MaxLifetime is the longest token lifetime (exp - now) the service accepts.
const MaxLifetime = 15 * time.Minute

// maxSubLen bounds the opaque subject identifier.
const maxSubLen = 255

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
	// IssuedAt is zero when the token has no iat claim.
	IssuedAt time.Time `json:"iat,omitzero"`
}

// Verifier checks host tokens.
type Verifier struct {
	Secret []byte
	Issuer string
	Skew   time.Duration
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
	if !ok || sub == "" || len(sub) > maxSubLen {
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
	if raw, present := payload["iat"]; present {
		iat, ok := numericDate(raw)
		if !ok || iat.After(now.Add(v.Skew)) {
			return Claims{}, ErrInvalid
		}
		c.IssuedAt = iat
	}
	if raw, present := payload["nbf"]; present {
		nbf, ok := numericDate(raw)
		if !ok || nbf.After(now.Add(v.Skew)) {
			return Claims{}, ErrInvalid
		}
	}
	return c, nil
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
	header := `{"alg":"HS256","typ":"JWT"}`
	payload, _ := json.Marshal(struct {
		Iss   string `json:"iss"`
		Sub   string `json:"sub"`
		Voter bool   `json:"voter"`
		Iat   int64  `json:"iat"`
		Exp   int64  `json:"exp"`
	}{issuer, sub, voter, now.Unix(), now.Add(ttl).Unix()})
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
