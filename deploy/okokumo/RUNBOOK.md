# RUNBOOK — put feedback.okokumo.com live (kanban t_8d857924)

Everything that does not need Scaleway credentials is DONE: service running on
the okokumo prod host at 127.0.0.1:8095 from sha f543065, own DB/role, env
file, admin token file, GitHub prod secret/vars, Caddy apply/rollback scripts
staged on the host and dry-run validated. The agent has no Scaleway API key,
so the DNS record is the one step left for a human; step 2 is one script.

## 1. DNS (needs Scaleway credentials — Alex's machine)

    scw dns record add okokumo.com name=feedback type=A data=163.172.163.71 ttl=300
    scw dns record list okokumo.com name=feedback type=A -o json | jq -r '.[].id'   # note the id

Wait until it resolves publicly. The zone's negative-cache TTL is 3600 s and
Cloudflare already cached NXDOMAIN for this name, so allow up to an hour:

    dig +short A feedback.okokumo.com @ns0.dom.scw.cloud     # 163.172.163.71
    curl -s 'https://cloudflare-dns.com/dns-query?name=feedback.okokumo.com&type=A' -H 'accept: application/dns-json'; echo
    dig +short A feedback.okokumo.com @8.8.8.8               # 163.172.163.71

Then put the record id into the Terraform import block (okokumo repo, branch
`deploy/feature-vote`) so the next `terraform apply` adopts the record instead
of creating a duplicate — or paste the id on the card and the dev agent does it:

    cd ~/Documents/Okokumo && git fetch && git worktree add /tmp/okk-fv deploy/feature-vote && cd /tmp/okk-fv
    sed -i 's#okokumo.com/REPLACE_WITH_RECORD_ID#okokumo.com/<ID>#' infra/envs/prod/dns.tf
    git commit -am "dns: feedback record id for the import block (t_8d857924)" && git push

## 2. Caddy (root on the prod host)

    ssh root@163.172.163.71 /root/featurevote-apply-caddy.sh 2>&1 | tee fv-go-live.out

The script (source: `deploy/okokumo/apply-caddy.sh`, branch `deploy/okokumo`
of slash4/FeatureVote) does nothing until the record answers 163.172.163.71 at
ns0, ns1, 1.1.1.1 and 8.8.8.8 — running it early just exits 1. When DNS is
ready it records the existing sites' status codes, backs up the Caddyfile,
inserts the `feedback.okokumo.com` block above the Tower catch-all, runs
`caddy validate` on the new file (live file untouched on failure), reloads,
waits for the certificate, prints the definition-of-done checks, and diffs
before/after codes (exit 2 if any existing site changed). Paste
fv-go-live.out on card t_8d857924 and unblock it.

Validate only, no change: `ssh root@163.172.163.71 DRY_RUN=1 /root/featurevote-apply-caddy.sh`

Baseline taken 2026-10-05 ~10:35 CEST (path `/`):

    okokumo.com 200 | www.okokumo.com 301 | app.okokumo.com 200
    status.okokumo.com 302 | admin.okokumo.com 200

## Rollback

    ssh root@163.172.163.71 /root/featurevote-rollback-caddy.sh          # Caddy only
    ssh root@163.172.163.71 /root/featurevote-rollback-caddy.sh --stop   # + stop the unit
    scw dns record delete okokumo.com <ID>                                # optional

Nothing in the okokumo app references the service until FEATURE_VOTE_* ship
in a prod tag, so rollback has no app impact.

## Founder hand-off

- Admin token for https://feedback.okokumo.com/admin: `/root/featurevote-admin-token` on the prod host (root-only, 0400).
- Host secret for the okokumo integration: `FV_HOST_SECRET` in `/etc/featurevote/env`; already stored as GitHub `slash4/Okokumo` → environment `prod` → secret `FEATURE_VOTE_SECRET`, with variables `FEATURE_VOTE_URL=https://feedback.okokumo.com`, `FEATURE_VOTE_ISSUER=okokumo`.

Full install notes: `docs/DEPLOY-okokumo.md`.
