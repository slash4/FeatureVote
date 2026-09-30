// Token handling. The widget never verifies the host JWT (only the service can); it decodes the
// payload solely to read `exp` so it can refresh the token shortly before it expires.

export type TokenProvider = () => Promise<string | null | undefined> | string | null | undefined;

const REFRESH_MARGIN_MS = 60_000;
const UNKNOWN_EXP_TTL_MS = 120_000;
const ANONYMOUS_TTL_MS = 60_000;

export function decodeExp(jwt: string): number | null {
  const parts = jwt.split('.');
  if (parts.length !== 3) return null;
  try {
    let b64 = parts[1].replace(/-/g, '+').replace(/_/g, '/');
    while (b64.length % 4) b64 += '=';
    const payload = JSON.parse(atob(b64));
    return typeof payload.exp === 'number' && isFinite(payload.exp) ? payload.exp : null;
  } catch {
    return null;
  }
}

interface Cached {
  token: string | null;
  fetchedAt: number;
  expiresAt: number;
}

export class TokenManager {
  private cached_: Cached | null = null;
  private inflight_: Promise<string | null> | null = null;
  private generation_ = 0;

  constructor(
    private tokenUrl_: string | null,
    private provider_: TokenProvider | null,
    private now_: () => number = () => Date.now(),
  ) {}

  setProvider(fn: TokenProvider | null): void {
    this.provider_ = fn;
    this.invalidate();
  }

  invalidate(): void {
    this.cached_ = null;
    this.inflight_ = null;
    this.generation_++;
  }

  // get returns a usable token, or null when the visitor is anonymous. Tokens are reused until
  // 60s before exp (or half their lifetime for very short-lived tokens).
  get(): Promise<string | null> {
    const c = this.cached_;
    if (c) {
      const margin = Math.min(REFRESH_MARGIN_MS, (c.expiresAt - c.fetchedAt) / 2);
      if (this.now_() < c.expiresAt - margin) return Promise.resolve(c.token);
    }
    if (this.inflight_) return this.inflight_;
    const gen = this.generation_;
    const p = this.fetchToken_()
      .catch(() => null)
      .then((token) => {
        if (gen === this.generation_) {
          const fetchedAt = this.now_();
          let expiresAt = fetchedAt + ANONYMOUS_TTL_MS;
          if (token) {
            const exp = decodeExp(token);
            expiresAt = exp !== null ? exp * 1000 : fetchedAt + UNKNOWN_EXP_TTL_MS;
          }
          this.cached_ = { token, fetchedAt, expiresAt };
          this.inflight_ = null;
        }
        return token;
      });
    this.inflight_ = p;
    return p;
  }

  private async fetchToken_(): Promise<string | null> {
    if (this.provider_) {
      const t = await this.provider_();
      return typeof t === 'string' && t ? t : null;
    }
    if (!this.tokenUrl_) return null;
    // 401/403/404/network errors all mean "anonymous, read-only".
    const res = await fetch(this.tokenUrl_, {
      method: 'GET',
      credentials: 'include',
      cache: 'no-store',
      headers: { Accept: 'application/json' },
    });
    if (!res.ok) return null;
    const body = await res.json();
    return body && typeof body.token === 'string' && body.token ? body.token : null;
  }
}
