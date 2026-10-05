package hosttoken

import (
	"errors"
	"strings"
	"testing"
	"time"
)

const (
	secret = "0123456789abcdef0123456789abcdef-host"
	issuer = "okokumo"
)

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func verifier() *Verifier {
	return &Verifier{Secret: []byte(secret), Issuer: issuer, Skew: 30 * time.Second, Now: func() time.Time { return t0 }}
}

func TestVerifyRoundTrip(t *testing.T) {
	tok := MintAt(t0, secret, issuer, "user-42", true, 10*time.Minute)
	c, err := verifier().Verify(tok)
	if err != nil {
		t.Fatal(err)
	}
	if c.Subject != "user-42" || !c.Voter || c.Issuer != issuer ||
		!c.ExpiresAt.Equal(t0.Add(10*time.Minute)) || !c.IssuedAt.Equal(t0) {
		t.Fatalf("claims = %+v", c)
	}
}

func TestVerifyVoterClaim(t *testing.T) {
	iat, exp := t0.Unix(), t0.Add(time.Minute).Unix()
	for _, tc := range []struct {
		voter string
		want  bool
	}{{`,"voter":true`, true}, {`,"voter":false`, false}, {``, false}, {`,"voter":"true"`, false}, {`,"voter":1`, false}} {
		payload := `{"iss":"okokumo","sub":"u"` + tc.voter + `,"iat":` + fmtInt(iat) + `,"exp":` + fmtInt(exp) + `}`
		c, err := verifier().Verify(Sign(secret, []byte(`{"alg":"HS256"}`), []byte(payload)))
		if err != nil {
			t.Fatalf("%s: %v", tc.voter, err)
		}
		if c.Voter != tc.want {
			t.Errorf("voter %q => %v, want %v", tc.voter, c.Voter, tc.want)
		}
	}
}

func fmtInt(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

func TestVerifyFailures(t *testing.T) {
	exp := fmtInt(t0.Add(5 * time.Minute).Unix())
	iat := `,"iat":` + fmtInt(t0.Unix())
	good := `{"iss":"okokumo","sub":"u","voter":true` + iat + `,"exp":` + exp + `}`
	hs := []byte(`{"alg":"HS256","typ":"JWT"}`)
	cases := []struct {
		name  string
		token string
		want  error
	}{
		{"empty", "", ErrInvalid},
		{"garbage", "not-a-jwt", ErrInvalid},
		{"two parts", "a.b", ErrInvalid},
		{"bad base64", "!!!.???.***", ErrInvalid},
		{"bad signature", Sign("another-secret-another-secret-xx", hs, []byte(good)), ErrInvalid},
		{"alg none", Sign(secret, []byte(`{"alg":"none"}`), []byte(good)), ErrInvalid},
		{"alg none unsigned", b64.EncodeToString([]byte(`{"alg":"none"}`)) + "." + b64.EncodeToString([]byte(good)) + ".", ErrInvalid},
		{"alg HS512", Sign(secret, []byte(`{"alg":"HS512"}`), []byte(good)), ErrInvalid},
		{"alg missing", Sign(secret, []byte(`{"typ":"JWT"}`), []byte(good)), ErrInvalid},
		{"typ wrong", Sign(secret, []byte(`{"alg":"HS256","typ":"JWS"}`), []byte(good)), ErrInvalid},
		{"crit header", Sign(secret, []byte(`{"alg":"HS256","crit":["x"]}`), []byte(good)), ErrInvalid},
		{"wrong iss", MintAt(t0, secret, "doloop", "u", true, time.Minute), ErrInvalid},
		{"missing iss", Sign(secret, hs, []byte(`{"sub":"u"`+iat+`,"exp":`+exp+`}`)), ErrInvalid},
		{"missing sub", Sign(secret, hs, []byte(`{"iss":"okokumo"`+iat+`,"exp":`+exp+`}`)), ErrInvalid},
		{"empty sub", MintAt(t0, secret, issuer, "", true, time.Minute), ErrInvalid},
		{"numeric sub", Sign(secret, hs, []byte(`{"iss":"okokumo","sub":42`+iat+`,"exp":`+exp+`}`)), ErrInvalid},
		{"long sub", MintAt(t0, secret, issuer, strings.Repeat("s", 256), true, time.Minute), ErrInvalid},
		{"NUL in sub", MintAt(t0, secret, issuer, "u\x00", true, time.Minute), ErrInvalid},
		{"tab in sub", MintAt(t0, secret, issuer, "u\tv", true, time.Minute), ErrInvalid},
		{"escape in sub", MintAt(t0, secret, issuer, "\x1b[31mu", true, time.Minute), ErrInvalid},
		{"missing exp", Sign(secret, hs, []byte(`{"iss":"okokumo","sub":"u"`+iat+`}`)), ErrInvalid},
		{"string exp", Sign(secret, hs, []byte(`{"iss":"okokumo","sub":"u"`+iat+`,"exp":"`+exp+`"}`)), ErrInvalid},
		{"expired beyond skew", MintAt(t0.Add(-10*time.Minute), secret, issuer, "u", true, 10*time.Minute-31*time.Second), ErrExpired},
		{"lifetime too long", MintAt(t0, secret, issuer, "u", true, 16*time.Minute), ErrInvalid},
		{"missing iat", Sign(secret, hs, []byte(`{"iss":"okokumo","sub":"u","voter":true,"exp":`+exp+`}`)), ErrInvalid},
		{"string iat", Sign(secret, hs, []byte(`{"iss":"okokumo","sub":"u","iat":"`+fmtInt(t0.Unix())+`","exp":`+exp+`}`)), ErrInvalid},
		{"null iat", Sign(secret, hs, []byte(`{"iss":"okokumo","sub":"u","iat":null,"exp":`+exp+`}`)), ErrInvalid},
		// exp - now is fine (5m) but the host minted it for 15m + 1s: an
		// old long-lived token must not become acceptable as it ages.
		{"exp minus iat > 15m", MintAt(t0.Add(-10*time.Minute-time.Second), secret, issuer, "u", true, 15*time.Minute+time.Second), ErrInvalid},
		{"iat in future", MintAt(t0.Add(time.Minute), secret, issuer, "u", true, time.Minute), ErrInvalid},
		{"nbf in future", Sign(secret, hs, []byte(`{"iss":"okokumo","sub":"u"`+iat+`,"exp":`+exp+`,"nbf":`+fmtInt(t0.Add(time.Minute).Unix())+`}`)), ErrInvalid},
		{"payload not object", Sign(secret, hs, []byte(`[1,2]`)), ErrInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := verifier().Verify(tc.token)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestVerifySkewBoundaries(t *testing.T) {
	v := verifier()
	// Expired 29s ago: inside the 30s skew => accepted.
	if _, err := v.Verify(MintAt(t0.Add(-5*time.Minute), secret, issuer, "u", true, 5*time.Minute-29*time.Second)); err != nil {
		t.Fatalf("inside skew: %v", err)
	}
	// A minted lifetime (exp - iat) of exactly 15m is accepted; 15m + 1s is
	// not, even though exp - now would still fit inside 15m + skew.
	if _, err := v.Verify(MintAt(t0, secret, issuer, "u", true, MaxLifetime)); err != nil {
		t.Fatalf("max lifetime: %v", err)
	}
	if _, err := v.Verify(MintAt(t0, secret, issuer, "u", true, MaxLifetime+time.Second)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("max lifetime + 1s: err = %v, want ErrInvalid", err)
	}
	// Host clock 20s ahead minting a full 15m token: exp - now = 15m20s,
	// still inside 15m + skew => accepted.
	if _, err := v.Verify(MintAt(t0.Add(20*time.Second), secret, issuer, "u", true, MaxLifetime)); err != nil {
		t.Fatalf("host clock ahead, full lifetime: %v", err)
	}
	// iat slightly in the future (host clock ahead) inside skew => accepted.
	if _, err := v.Verify(MintAt(t0.Add(20*time.Second), secret, issuer, "u", true, time.Minute)); err != nil {
		t.Fatalf("iat inside skew: %v", err)
	}
}

func TestVerifyAudience(t *testing.T) {
	iat, exp := fmtInt(t0.Unix()), fmtInt(t0.Add(5*time.Minute).Unix())
	hs := []byte(`{"alg":"HS256","typ":"JWT"}`)
	tok := func(aud string) string {
		return Sign(secret, hs, []byte(`{"iss":"okokumo","sub":"u","voter":true`+aud+`,"iat":`+iat+`,"exp":`+exp+`}`))
	}
	const want = "feedback.okokumo.com"
	cases := []struct {
		name string
		aud  string
		ok   bool
	}{
		{"string match", `,"aud":"feedback.okokumo.com"`, true},
		{"array contains", `,"aud":["other.example","feedback.okokumo.com"]`, true},
		{"missing", ``, false},
		{"other instance", `,"aud":"feedback.getdoloop.com"`, false},
		{"array without", `,"aud":["feedback.getdoloop.com"]`, false},
		{"empty array", `,"aud":[]`, false},
		{"empty string", `,"aud":""`, false},
		{"case differs", `,"aud":"Feedback.okokumo.com"`, false},
		{"number", `,"aud":42`, false},
		{"null", `,"aud":null`, false},
		{"object", `,"aud":{"x":"feedback.okokumo.com"}`, false},
		{"nested array", `,"aud":[["feedback.okokumo.com"]]`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := verifier()
			v.Audience = want
			c, err := v.Verify(tok(tc.aud))
			if tc.ok {
				if err != nil || c.Audience != want {
					t.Fatalf("got %+v, %v; want accepted with aud %q", c, err, want)
				}
				return
			}
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
	// Backward compatible: without a configured audience, aud is not
	// checked at all, whatever its shape.
	for _, tc := range cases {
		if c, err := verifier().Verify(tok(tc.aud)); err != nil || c.Audience != "" {
			t.Fatalf("no audience configured, aud %s: %+v, %v", tc.aud, c, err)
		}
	}
	// Encode round-trips aud and omits it when empty.
	v := verifier()
	v.Audience = want
	c := Claims{Issuer: issuer, Subject: "u", Voter: true, IssuedAt: t0, ExpiresAt: t0.Add(time.Minute), Audience: want}
	if _, err := v.Verify(Encode(secret, c)); err != nil {
		t.Fatalf("Encode with aud: %v", err)
	}
	c.Audience = ""
	if _, err := v.Verify(Encode(secret, c)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Encode without aud vs configured audience: err = %v, want ErrInvalid", err)
	}
}
