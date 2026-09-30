#!/usr/bin/env ruby
# frozen_string_literal: true

# Proves the Ruby minting code published in docs/INTEGRATION.md produces
# tokens FeatureVote accepts: mints with the `jwt` gem exactly as documented,
# then verifies each token with `go run ./cmd/fvtoken -verify <token>`.
# Also checks the snippet below is still present verbatim in the doc.
#
#   ruby scripts/verify-ruby-mint.rb      (needs: gem install jwt; go on PATH)

require "base64"
require "json"
require "open3"

ROOT = File.expand_path("..", __dir__)

# --- begin INTEGRATION.md ruby snippet ---
require "jwt"

module FeatureVoteToken
  TTL = 600 # seconds; FeatureVote rejects tokens living longer than 15 minutes

  # sub: opaque, stable user id (never an email). voter: the host's eligibility decision.
  def self.mint(secret:, issuer:, sub:, voter:, ttl: TTL)
    now = Time.now.to_i
    payload = { iss: issuer, sub: sub.to_s, voter: voter == true, iat: now, exp: now + ttl }
    JWT.encode(payload, secret, "HS256")
  end
end
# --- end INTEGRATION.md ruby snippet ---

def snippet
  src = File.read(__FILE__)
  src[/# --- begin INTEGRATION.md ruby snippet ---\n(.*?)# --- end INTEGRATION.md ruby snippet ---/m, 1]
end

doc = File.read(File.join(ROOT, "docs/INTEGRATION.md"))
abort "FAIL: Ruby snippet in docs/INTEGRATION.md differs from scripts/verify-ruby-mint.rb" unless doc.include?(snippet)
puts "ok   doc snippet matches (#{snippet.lines.count} lines)"

secret = "ruby-verify-secret-0123456789abcdef"
issuer = "doloop"
env = { "FV_HOST_SECRET" => secret, "FV_HOST_ISSUER" => issuer }

[["usr_7c1e", true], ["usr_d00d", false]].each do |sub, voter|
  token = FeatureVoteToken.mint(secret: secret, issuer: issuer, sub: sub, voter: voter)
  header = JSON.parse(Base64.urlsafe_decode64(token.split(".").first))
  out, err, status = Open3.capture3(env, "go", "run", "./cmd/fvtoken", "-verify", token, chdir: ROOT)
  abort "FAIL: fvtoken rejected Ruby token (#{err.strip})" unless status.success?
  claims = JSON.parse(out)
  unless claims["sub"] == sub && claims["voter"] == voter && claims["iss"] == issuer
    abort "FAIL: unexpected claims #{claims.inspect}"
  end
  puts "ok   jwt #{JWT::VERSION::STRING} header=#{header.to_json} sub=#{sub} voter=#{voter} verified by fvtoken"
end

# Negative control: a wrong secret must be rejected.
bad = FeatureVoteToken.mint(secret: "#{secret}-wrong", issuer: issuer, sub: "x", voter: true)
_, err, status = Open3.capture3(env, "go", "run", "./cmd/fvtoken", "-verify", bad, chdir: ROOT)
abort "FAIL: token with wrong secret was accepted" if status.success?
puts "ok   wrong-secret token rejected (#{err.strip})"
puts "PASS"
