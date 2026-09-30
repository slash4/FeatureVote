import { TokenManager } from './token';

export type Status = 'under_review' | 'planned' | 'in_progress' | 'shipped' | 'declined';
export const STATUSES: Status[] = ['under_review', 'planned', 'in_progress', 'shipped', 'declined'];

export interface Idea {
  id: number;
  title: string;
  body: string;
  status: Status;
  up: number;
  down: number;
  score: number;
  created_at: string;
  moderation_state?: 'pending' | 'approved';
}

export interface Me {
  voter: boolean;
  votes: Array<{ idea_id: number; value: number }>;
  ideas: Idea[];
}

export interface VoteResult {
  idea: Idea;
  my_vote: number;
}

export interface Result<T> {
  ok: boolean;
  status: number;
  data?: T;
  // Service error code (see SPEC error format), or 'network' when the request never completed.
  code?: string;
  retryAfter?: number;
}

export const AUTH_CODES = ['unauthorized', 'invalid_token', 'token_expired'];

export class Api {
  private base_: string;

  constructor(base: string, private tokens_: TokenManager) {
    this.base_ = base.replace(/\/+$/, '');
  }

  listIdeas(sort: string, status: string, offset: number): Promise<Result<{ ideas: Idea[]; total: number }>> {
    let q = '?sort=' + encodeURIComponent(sort) + '&limit=50&offset=' + offset;
    if (status) q += '&status=' + encodeURIComponent(status);
    return this.request_('GET', '/v1/ideas' + q);
  }

  me(): Promise<Result<Me>> {
    return this.request_('GET', '/v1/me', undefined, true);
  }

  createIdea(title: string, body: string): Promise<Result<Idea>> {
    return this.request_('POST', '/v1/ideas', { title, body }, true);
  }

  vote(id: number, value: number): Promise<Result<VoteResult>> {
    return this.request_('PUT', '/v1/ideas/' + id + '/vote', { value }, true);
  }

  unvote(id: number): Promise<Result<VoteResult>> {
    return this.request_('DELETE', '/v1/ideas/' + id + '/vote', undefined, true);
  }

  // request sends the call; authenticated calls carry the host token and, on a 401, re-fetch the
  // token once and retry once.
  private async request_<T>(method: string, path: string, body?: unknown, auth = false): Promise<Result<T>> {
    if (!auth) return this.send_<T>(method, path, body, null);
    let token = await this.tokens_.get();
    if (!token) return { ok: false, status: 401, code: 'unauthorized' };
    let res = await this.send_<T>(method, path, body, token);
    if (res.status === 401) {
      this.tokens_.invalidate();
      token = await this.tokens_.get();
      if (token) res = await this.send_<T>(method, path, body, token);
    }
    return res;
  }

  private async send_<T>(method: string, path: string, body: unknown, token: string | null): Promise<Result<T>> {
    const headers: Record<string, string> = { Accept: 'application/json' };
    if (token) headers.Authorization = 'Bearer ' + token;
    if (body !== undefined) headers['Content-Type'] = 'application/json';
    let res: Response;
    try {
      res = await fetch(this.base_ + path, {
        method,
        headers,
        body: body !== undefined ? JSON.stringify(body) : undefined,
        credentials: 'omit',
        cache: 'no-store',
      });
    } catch {
      return { ok: false, status: 0, code: 'network' };
    }
    let data: any;
    try {
      data = await res.json();
    } catch {
      data = undefined;
    }
    if (res.ok) return { ok: true, status: res.status, data };
    const ra = parseInt(res.headers.get('Retry-After') || '', 10);
    return {
      ok: false,
      status: res.status,
      code: (data && data.error && data.error.code) || (res.status === 401 ? 'unauthorized' : 'internal'),
      retryAfter: isNaN(ra) ? undefined : ra,
    };
  }
}
