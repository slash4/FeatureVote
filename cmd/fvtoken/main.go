// Command fvtoken mints (or verifies) FeatureVote host tokens for local
// development, scripts and tests.
//
//	FV_HOST_SECRET=... FV_HOST_ISSUER=okokumo fvtoken -sub user-1 -voter
//	FV_HOST_SECRET=... FV_HOST_ISSUER=okokumo fvtoken -verify <jwt>
//
// A negative -ttl mints an already-expired token (useful for testing).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/slash4/featurevote/internal/hosttoken"
)

func main() {
	sub := flag.String("sub", "", "opaque, stable user id (required when minting)")
	voter := flag.Bool("voter", false, "voting eligibility claim")
	ttl := flag.Duration("ttl", 10*time.Minute, "token lifetime (max accepted by the service: 15m)")
	verify := flag.String("verify", "", "verify this token instead of minting; prints its claims as JSON")
	skew := flag.Duration("skew", 30*time.Second, "clock skew allowance used by -verify")
	flag.Parse()

	secret, issuer := os.Getenv("FV_HOST_SECRET"), os.Getenv("FV_HOST_ISSUER")
	if secret == "" || issuer == "" {
		fail("FV_HOST_SECRET and FV_HOST_ISSUER must be set")
	}

	if *verify != "" {
		v := hosttoken.Verifier{Secret: []byte(secret), Issuer: issuer, Skew: *skew}
		c, err := v.Verify(*verify)
		if err != nil {
			fail("verify: " + err.Error())
		}
		out, _ := json.MarshalIndent(c, "", "  ")
		fmt.Println(string(out))
		return
	}

	if *sub == "" {
		fail("-sub is required")
	}
	if *ttl > hosttoken.MaxLifetime {
		fmt.Fprintf(os.Stderr, "fvtoken: warning: ttl %s exceeds %s; the service will reject this token\n", *ttl, hosttoken.MaxLifetime)
	}
	fmt.Println(hosttoken.Mint(secret, issuer, *sub, *voter, *ttl))
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "fvtoken: "+msg)
	os.Exit(1)
}
