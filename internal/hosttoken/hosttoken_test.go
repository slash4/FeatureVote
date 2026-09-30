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
	exp := t0.Add(time.Minute).Unix()
	for _, tc := range []struct {
		voter string
		want  bool
	}{{`,"voter":true`, true}, {`,"voter":false`, false}, {``, false}, {`,"voter":"true"`, false}, {`,"voter":1`, false}} {
		payload := `{"iss":"okokumo","sub":"u"` + tc.voter + `,"exp":` + fmtInt(exp) + `}`
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
	good := `{"iss":"okokumo","sub":"u","voter":true,"exp":` + exp + `}`
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
		{"missing iss", Sign(secret, hs, []byte(`{"sub":"u","exp":`+exp+`}`)), ErrInvalid},
		{"missing sub", Sign(secret, hs, []byte(`{"iss":"okokumo","exp":`+exp+`}`)), ErrInvalid},
		{"empty sub", MintAt(t0, secret, issuer, "", true, time.Minute), ErrInvalid},
		{"numeric sub", Sign(secret, hs, []byte(`{"iss":"okokumo","sub":42,"exp":`+exp+`}`)), ErrInvalid},
		{"long sub", MintAt(t0, secret, issuer, strings.Repeat("s", 256), true, time.Minute), ErrInvalid},
		{"missing exp", Sign(secret, hs, []byte(`{"iss":"okokumo","sub":"u"}`)), ErrInvalid},
		{"string exp", Sign(secret, hs, []byte(`{"iss":"okokumo","sub":"u","exp":"`+exp+`"}`)), ErrInvalid},
		{"expired beyond skew", MintAt(t0.Add(-10*time.Minute), secret, issuer, "u", true, 10*time.Minute-31*time.Second), ErrExpired},
		{"lifetime too long", MintAt(t0, secret, issuer, "u", true, 16*time.Minute), ErrInvalid},
		{"iat in future", MintAt(t0.Add(time.Minute), secret, issuer, "u", true, time.Minute), ErrInvalid},
		{"nbf in future", Sign(secret, hs, []byte(`{"iss":"okokumo","sub":"u","exp":`+exp+`,"nbf":`+fmtInt(t0.Add(time.Minute).Unix())+`}`)), ErrInvalid},
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
	// Lifetime of exactly 15m + skew is still accepted.
	if _, err := v.Verify(MintAt(t0, secret, issuer, "u", true, MaxLifetime+30*time.Second)); err != nil {
		t.Fatalf("max lifetime: %v", err)
	}
	// iat slightly in the future (host clock ahead) inside skew => accepted.
	if _, err := v.Verify(MintAt(t0.Add(20*time.Second), secret, issuer, "u", true, time.Minute)); err != nil {
		t.Fatalf("iat inside skew: %v", err)
	}
}
