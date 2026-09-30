// DOM for the widget. Elements are built with createElement + textContent only: user text never goes
// through markup parsing. Every user-facing string comes from t() (see test/ui-guard.test.mjs).

import { AUTH_CODES, Api, Idea, Me, Result, STATUSES, Status } from './api';
import { MessageKey, Translator } from './i18n';
import { IconName, icon } from './icons';
import { buildCss } from './styles';

export interface UiOptions {
  locale: string;
  position?: string;
  accent?: string;
  loginUrl?: string;
  loginText?: string;
  loginLinkText?: string;
  upgradeUrl?: string;
  upgradeText?: string;
  upgradeLinkText?: string;
}

type Mode = 'anon' | 'nonvoter' | 'voter';
type Attrs = Record<string, string | number | boolean | undefined | null>;
type Child = Node | string | null | undefined | false;

interface Card {
  up: HTMLButtonElement;
  down: HTMLButtonElement;
  score: HTMLElement;
  detail: HTMLElement;
  note: HTMLElement;
  chip: HTMLElement;
  title: HTMLElement;
  body: HTMLElement | null;
  more: HTMLButtonElement | null;
}

const POSITIONS = ['bottom-right', 'bottom-left', 'top-right', 'top-left'];
const TITLE_MAX = 120;
const BODY_MAX = 2000;
const FOCUSABLE = 'a[href],button,input,select,textarea,[tabindex]';
const TOAST_MS = 6000;

function h<K extends keyof HTMLElementTagNameMap>(tag: K, attrs?: Attrs | null, ...kids: Child[]): HTMLElementTagNameMap[K] {
  const el = document.createElement(tag);
  if (attrs) {
    for (const k of Object.keys(attrs)) {
      const v = attrs[k];
      if (v === undefined || v === null || v === false) continue;
      if (k === 'class') el.className = String(v);
      else el.setAttribute(k, v === true ? '' : String(v));
    }
  }
  for (const c of kids) if (c) el.appendChild(typeof c === 'string' ? document.createTextNode(c) : c);
  return el;
}

// Only http(s) links (absolute or relative to the host page) are rendered.
function safeUrl(u: string | undefined): string | null {
  if (!u) return null;
  try {
    const url = new URL(u, location.href);
    return /^https?:$/.test(url.protocol) ? url.href : null;
  } catch {
    return null;
  }
}

function statusOf(idea: Idea): Status {
  return STATUSES.indexOf(idea.status) >= 0 ? idea.status : 'under_review';
}

export class Widget {
  private root_: HTMLElement;
  private launcher_: HTMLButtonElement;
  private panel_: HTMLElement;
  private closeBtn_: HTMLButtonElement;
  private sortBtns_: Record<string, HTMLButtonElement> = {};
  private statusSelect_: HTMLSelectElement;
  private body_: HTMLElement;
  private success_: HTMLElement;
  private formWrap_: HTMLElement;
  private content_: HTMLElement;
  private pendingEl_: HTMLElement;
  private stateEl_: HTMLElement;
  private retryBtn_: HTMLButtonElement;
  private list_: HTMLElement;
  private moreBtn_: HTMLButtonElement;
  private toastEl_: HTMLElement;
  private live_: HTMLElement;
  private footer_: HTMLElement;
  private suggestBtn_: HTMLButtonElement;
  private titleInput_!: HTMLInputElement;
  private bodyInput_!: HTMLTextAreaElement;
  private titleCount_!: HTMLElement;
  private bodyCount_!: HTMLElement;
  private formError_!: HTMLElement;
  private submitBtn_!: HTMLButtonElement;

  private mode_: Mode = 'anon';
  private ideas_: Idea[] = [];
  private total_ = 0;
  private sort_ = 'top';
  private status_ = '';
  private myVotes_ = new Map<number, number>();
  private ownIds_ = new Set<number>();
  private pending_: Idea[] = [];
  private cards_ = new Map<number, Card>();
  private voting_ = new Set<number>();
  private isOpen_ = false;
  private loaded_ = false;
  private listFailed_ = false;
  private submitting_ = false;
  private seq_ = 0;
  private toastTimer_ = 0;

  constructor(private shadow_: ShadowRoot, private api_: Api, private t_: Translator, private opts_: UiOptions) {
    const t = this.t_;
    const opts = this.opts_;
    const shadow = this.shadow_;
    const pos = POSITIONS.indexOf(opts.position || '') >= 0 ? opts.position : POSITIONS[0];
    const style = document.createElement('style');
    style.textContent = buildCss(opts.accent);

    this.launcher_ = h(
      'button',
      { type: 'button', class: 'fv-launcher', 'aria-expanded': 'false', 'aria-controls': 'fv-panel', 'aria-label': t('launcher') },
      icon('bulb', 20),
      h('span', { class: 'fv-launcher-text' }, t('launcher')),
    );
    this.launcher_.addEventListener('click', () => this.toggle());

    this.closeBtn_ = h('button', { type: 'button', class: 'fv-icon-btn', 'aria-label': t('close'), title: t('close') }, icon('close', 20));
    this.closeBtn_.addEventListener('click', () => this.close());

    const seg = h('div', { class: 'fv-seg', role: 'group', 'aria-label': t('sortLabel') });
    for (const [key, label] of [['top', t('sortTop')], ['new', t('sortNew')]]) {
      const b = h('button', { type: 'button', 'aria-pressed': String(key === this.sort_) }, label);
      b.addEventListener('click', () => this.setSort_(key));
      this.sortBtns_[key] = b;
      seg.appendChild(b);
    }

    this.statusSelect_ = h('select', { id: 'fv-status' }, h('option', { value: '' }, t('filterAll')));
    for (const s of STATUSES) this.statusSelect_.appendChild(h('option', { value: s }, t(('status_' + s) as MessageKey)));
    this.statusSelect_.addEventListener('change', () => this.setStatus_(this.statusSelect_.value));

    this.success_ = h('p', { class: 'fv-success', hidden: true });
    this.formWrap_ = h('div', { id: 'fv-form', hidden: true }, this.buildForm_());
    this.pendingEl_ = h('section', { class: 'fv-pending', 'aria-labelledby': 'fv-pending-title', hidden: true });
    this.stateEl_ = h('p', { class: 'fv-state' });
    this.retryBtn_ = h('button', { type: 'button', class: 'fv-btn' }, t('retry'));
    this.retryBtn_.addEventListener('click', () => this.load_());
    this.list_ = h('ul', { class: 'fv-list' });
    this.moreBtn_ = h('button', { type: 'button', class: 'fv-btn fv-wide', hidden: true }, t('loadMore'));
    this.moreBtn_.addEventListener('click', () => this.loadList_(true));
    this.content_ = h(
      'div',
      null,
      this.pendingEl_,
      this.stateEl_,
      h('div', { class: 'fv-state-actions' }, this.retryBtn_),
      this.list_,
      this.moreBtn_,
    );
    this.body_ = h('div', { class: 'fv-body', 'aria-busy': 'false' }, this.success_, this.formWrap_, this.content_);

    this.toastEl_ = h('div', { class: 'fv-toast', role: 'alert', hidden: true });
    this.live_ = h('div', { class: 'fv-sr', 'aria-live': 'polite', 'aria-atomic': 'true' });
    this.suggestBtn_ = h(
      'button',
      { type: 'button', class: 'fv-btn fv-primary', 'aria-expanded': 'false', 'aria-controls': 'fv-form' },
      t('suggest'),
    );
    this.suggestBtn_.addEventListener('click', () => this.openForm_());
    this.footer_ = h('div', { class: 'fv-footer' });

    this.panel_ = h(
      'div',
      { id: 'fv-panel', class: 'fv-panel', role: 'dialog', 'aria-modal': 'true', 'aria-labelledby': 'fv-title', hidden: true },
      h('div', { class: 'fv-header' }, h('h2', { id: 'fv-title', class: 'fv-title' }, t('title')), this.closeBtn_),
      h(
        'div',
        { class: 'fv-toolbar' },
        seg,
        h('div', { class: 'fv-filter' }, h('label', { for: 'fv-status' }, t('filterLabel')), this.statusSelect_),
      ),
      this.body_,
      this.toastEl_,
      this.footer_,
      this.live_,
    );
    this.panel_.addEventListener('keydown', (e) => this.onKey_(e));

    this.root_ = h('div', { class: 'fv-root fv-' + pos, lang: opts.locale }, this.launcher_, this.panel_);
    shadow.appendChild(style);
    shadow.appendChild(this.root_);
    this.renderFooter_();
  }

  // ---- public API ----

  open(): void {
    if (this.isOpen_) return;
    this.isOpen_ = true;
    this.panel_.hidden = false;
    this.launcher_.setAttribute('aria-expanded', 'true');
    this.closeBtn_.focus();
    if (!this.loaded_) this.load_();
    else this.measure_();
  }

  close(): void {
    if (!this.isOpen_) return;
    this.isOpen_ = false;
    this.panel_.hidden = true;
    this.launcher_.setAttribute('aria-expanded', 'false');
    this.launcher_.focus();
  }

  toggle(): void {
    if (this.isOpen_) this.close();
    else this.open();
  }

  // refresh reloads identity and ideas now if the panel is open, else on next open.
  refresh(): void {
    this.loaded_ = false;
    if (this.isOpen_) this.load_();
  }

  // ---- loading ----

  private async load_(): Promise<void> {
    const seq = ++this.seq_;
    this.setBusy_(true);
    if (!this.loaded_) this.showState_(this.t_('loading'));
    const [me, list] = await Promise.all([this.api_.me(), this.api_.listIdeas(this.sort_, this.status_, 0)]);
    if (seq !== this.seq_) return;
    this.applyMe_(me);
    this.listFailed_ = !(list.ok && list.data);
    if (list.ok && list.data) {
      this.ideas_ = list.data.ideas || [];
      this.total_ = list.data.total || 0;
      this.loaded_ = true;
    }
    this.renderPending_();
    this.renderList_(0);
    this.renderFooter_();
    this.setBusy_(false);
  }

  private async loadList_(append: boolean): Promise<void> {
    const seq = ++this.seq_;
    this.setBusy_(true);
    const res = await this.api_.listIdeas(this.sort_, this.status_, append ? this.ideas_.length : 0);
    if (seq !== this.seq_) return;
    this.setBusy_(false);
    if (!res.ok || !res.data) {
      if (append) this.toast_(this.t_('errGeneric'));
      else {
        this.listFailed_ = true;
        this.renderList_(0);
      }
      return;
    }
    this.listFailed_ = false;
    this.total_ = res.data.total || 0;
    const incoming = res.data.ideas || [];
    if (append) {
      const from = this.ideas_.length;
      const known = new Set(this.ideas_.map((i) => i.id));
      this.ideas_ = this.ideas_.concat(incoming.filter((i) => !known.has(i.id)));
      const hadFocus = this.shadow_.activeElement === this.moreBtn_;
      this.renderList_(from);
      const first = this.ideas_[from] && this.cards_.get(this.ideas_[from].id);
      if (hadFocus && this.moreBtn_.hidden && first) first.title.focus();
    } else {
      this.ideas_ = incoming;
      this.body_.scrollTop = 0;
      this.renderList_(0);
    }
  }

  private applyMe_(res: Result<Me>): void {
    if (res.ok && res.data) {
      const me = res.data;
      this.mode_ = me.voter ? 'voter' : 'nonvoter';
      this.myVotes_ = new Map((me.votes || []).map((v) => [v.idea_id, v.value] as [number, number]));
      this.ownIds_ = new Set((me.ideas || []).map((i) => i.id));
      this.pending_ = (me.ideas || []).filter((i) => i.moderation_state === 'pending');
      return;
    }
    if (res.code && AUTH_CODES.indexOf(res.code) < 0) this.toast_(this.t_('errGeneric'));
    this.mode_ = 'anon';
    this.myVotes_ = new Map();
    this.ownIds_ = new Set();
    this.pending_ = [];
  }

  // ---- rendering ----

  private setBusy_(busy: boolean): void {
    this.body_.setAttribute('aria-busy', String(busy));
  }

  private showState_(text: string, retry = false): void {
    this.stateEl_.textContent = text;
    this.stateEl_.hidden = false;
    this.retryBtn_.hidden = !retry;
    this.list_.hidden = true;
    this.moreBtn_.hidden = true;
  }

  private renderList_(from: number): void {
    if (from === 0) {
      this.list_.textContent = '';
      this.cards_.clear();
    }
    if (this.listFailed_) {
      this.showState_(this.t_('errGeneric'), true);
      return;
    }
    if (!this.ideas_.length) {
      const key: MessageKey = this.status_ ? 'emptyFiltered' : this.mode_ === 'voter' ? 'emptyVoter' : 'emptyReadOnly';
      this.showState_(this.t_(key));
      return;
    }
    this.stateEl_.hidden = true;
    this.retryBtn_.hidden = true;
    this.list_.hidden = false;
    for (const idea of this.ideas_.slice(from)) this.list_.appendChild(this.buildCard_(idea));
    this.moreBtn_.hidden = this.ideas_.length >= this.total_;
    this.measure_();
  }

  private voteButton_(dir: number, name: IconName, cls: string, id: number): HTMLButtonElement {
    const b = h('button', { type: 'button', class: cls }, icon(name));
    b.addEventListener('click', () => this.vote_(id, dir));
    return b;
  }

  private buildCard_(idea: Idea): HTMLElement {
    const t = this.t_;
    const bodyId = 'fv-b-' + idea.id;
    const up = this.voteButton_(1, 'up', 'fv-vote-btn fv-up', idea.id);
    const down = this.voteButton_(-1, 'down', 'fv-vote-btn fv-down', idea.id);
    const score = h('span', { class: 'fv-score', 'aria-hidden': 'true' });
    const detail = h('span', { class: 'fv-sr' });
    const note = h('span', { class: 'fv-note', id: 'fv-n-' + idea.id, hidden: true });
    const chip = h('span', { class: 'fv-chip' });
    const title = h('h3', { class: 'fv-card-title', tabindex: '-1' }, idea.title);
    let body: HTMLElement | null = null;
    let more: HTMLButtonElement | null = null;
    if (idea.body) {
      const b = h('p', { id: bodyId, class: 'fv-card-body fv-clamp' }, idea.body);
      const m = h('button', { type: 'button', class: 'fv-link-btn', 'aria-expanded': 'false', 'aria-controls': bodyId, hidden: true }, t('showMore'));
      m.addEventListener('click', () => {
        const expanded = m.getAttribute('aria-expanded') !== 'true';
        b.classList.toggle('fv-clamp', !expanded);
        m.setAttribute('aria-expanded', String(expanded));
        m.textContent = t(expanded ? 'showLess' : 'showMore');
      });
      body = b;
      more = m;
    }
    this.cards_.set(idea.id, { up, down, score, detail, note, chip, title, body, more });
    this.updateCard_(idea);
    return h(
      'li',
      { class: 'fv-card' },
      h('div', { class: 'fv-vote' }, up, score, down, detail),
      h('div', { class: 'fv-main' }, title, body, more, h('div', { class: 'fv-meta' }, chip, note)),
    );
  }

  private updateCard_(idea: Idea): void {
    const c = this.cards_.get(idea.id);
    if (!c) return;
    const t = this.t_;
    const status = statusOf(idea);
    const my = this.myVotes_.get(idea.id) || 0;
    const own = this.ownIds_.has(idea.id);
    const closed = status === 'shipped' || status === 'declined';
    const reason = own ? t('ownIdea') : closed ? t('votingClosed') : '';
    c.score.textContent = String(idea.score);
    c.detail.textContent = t('scoreDetail', { score: idea.score, up: idea.up, down: idea.down });
    c.chip.className = 'fv-chip fv-st-' + status;
    c.chip.textContent = t(('status_' + status) as MessageKey);
    c.note.textContent = reason;
    c.note.hidden = !reason;
    const pairs: Array<[HTMLButtonElement, number, MessageKey]> = [
      [c.up, 1, 'upvote'],
      [c.down, -1, 'downvote'],
    ];
    for (const [btn, dir, key] of pairs) {
      btn.disabled = this.mode_ !== 'voter' || own || closed;
      btn.setAttribute('aria-pressed', String(my === dir));
      btn.setAttribute('aria-label', t(key, { title: idea.title }));
      if (reason) {
        btn.title = reason;
        btn.setAttribute('aria-describedby', c.note.id);
      } else {
        btn.removeAttribute('title');
        btn.removeAttribute('aria-describedby');
      }
    }
  }

  private updateAllCards_(): void {
    for (const idea of this.ideas_) this.updateCard_(idea);
  }

  // Reveals "show more" only for bodies that actually overflow their 3-line clamp.
  private measure_(): void {
    if (!this.isOpen_) return;
    requestAnimationFrame(() => {
      this.cards_.forEach((c) => {
        if (!c.body || !c.more || c.more.getAttribute('aria-expanded') === 'true') return;
        c.more.hidden = c.body.scrollHeight <= c.body.clientHeight + 1;
      });
    });
  }

  private renderPending_(): void {
    const el = this.pendingEl_;
    el.textContent = '';
    if (!this.pending_.length || this.mode_ === 'anon') {
      el.hidden = true;
      return;
    }
    const ul = h('ul', { class: 'fv-list' });
    for (const idea of this.pending_) {
      ul.appendChild(
        h(
          'li',
          { class: 'fv-card' },
          h(
            'div',
            { class: 'fv-main' },
            h('h4', { class: 'fv-card-title' }, idea.title),
            idea.body ? h('p', { class: 'fv-card-body fv-clamp' }, idea.body) : null,
            h('div', { class: 'fv-meta' }, h('span', { class: 'fv-chip fv-st-pending' }, this.t_('awaitingReview'))),
          ),
        ),
      );
    }
    el.appendChild(h('h3', { id: 'fv-pending-title', class: 'fv-section-title' }, this.t_('pendingTitle')));
    el.appendChild(ul);
    el.hidden = false;
  }

  private renderFooter_(): void {
    const f = this.footer_;
    const o = this.opts_;
    f.textContent = '';
    f.hidden = !this.formWrap_.hidden;
    if (this.mode_ === 'voter') {
      f.appendChild(this.suggestBtn_);
      return;
    }
    const anon = this.mode_ === 'anon';
    const text = anon ? o.loginText || this.t_('loginCta') : o.upgradeText || this.t_('upgradeCta');
    const url = safeUrl(anon ? o.loginUrl : o.upgradeUrl);
    const label = anon ? o.loginLinkText || this.t_('loginLink') : o.upgradeLinkText || this.t_('upgradeLink');
    f.appendChild(h('p', { class: 'fv-cta-text' }, text));
    if (url) f.appendChild(h('a', { class: 'fv-cta-link', href: url }, label));
  }

  private setMode_(mode: Mode): void {
    if (this.mode_ === mode) return;
    this.mode_ = mode;
    if (mode !== 'voter') this.closeForm_(false);
    if (mode === 'anon') this.pending_ = [];
    this.renderPending_();
    this.updateAllCards_();
    this.renderFooter_();
    this.ensureFocus_();
  }

  // Keeps keyboard focus inside the open dialog when the focused control got disabled or removed.
  private ensureFocus_(): void {
    if (!this.isOpen_) return;
    const a = this.shadow_.activeElement as HTMLButtonElement | null;
    if (!a || !this.panel_.contains(a) || a.disabled || a.hidden) this.closeBtn_.focus();
  }

  // ---- sorting / filtering ----

  private setSort_(sort: string): void {
    if (sort === this.sort_) return;
    this.sort_ = sort;
    for (const k of Object.keys(this.sortBtns_)) this.sortBtns_[k].setAttribute('aria-pressed', String(k === sort));
    this.loadList_(false);
  }

  private setStatus_(status: string): void {
    this.status_ = STATUSES.indexOf(status as Status) >= 0 ? status : '';
    this.loadList_(false);
  }

  // ---- voting ----

  private async vote_(id: number, dir: number): Promise<void> {
    const idea = this.ideas_.filter((i) => i.id === id)[0];
    if (!idea || this.mode_ !== 'voter' || this.voting_.has(id)) return;
    const prev = this.myVotes_.get(id) || 0;
    const next = prev === dir ? 0 : dir;
    const snapshot = { up: idea.up, down: idea.down, score: idea.score, status: idea.status };

    // Optimistic update; rolled back below if the server refuses.
    if (prev === 1) idea.up--;
    if (prev === -1) idea.down--;
    if (next === 1) idea.up++;
    if (next === -1) idea.down++;
    idea.score = idea.up - idea.down;
    this.setMyVote_(id, next);
    this.updateCard_(idea);

    this.voting_.add(id);
    const res = next === 0 ? await this.api_.unvote(id) : await this.api_.vote(id, next);
    this.voting_.delete(id);

    if (res.ok && res.data && res.data.idea) {
      const s = res.data.idea;
      idea.up = s.up;
      idea.down = s.down;
      idea.score = s.score;
      idea.status = s.status;
      this.setMyVote_(id, res.data.my_vote || 0);
      this.updateCard_(idea);
      this.announce_(this.t_(next === 0 ? 'voteRemoved' : 'voteSaved'));
      return;
    }
    idea.up = snapshot.up;
    idea.down = snapshot.down;
    idea.score = snapshot.score;
    idea.status = snapshot.status;
    this.setMyVote_(id, prev);
    if (res.code === 'own_idea') this.ownIds_.add(id);
    this.updateCard_(idea);
    this.toast_(this.errorMessage_(res, false));
    if (res.code === 'voting_closed' || res.code === 'not_found') {
      await this.loadList_(false);
      this.ensureFocus_();
    }
  }

  private setMyVote_(id: number, v: number): void {
    if (v) this.myVotes_.set(id, v);
    else this.myVotes_.delete(id);
  }

  // errorMessage maps a failed call to a localized message, downgrading the widget's mode when the
  // service says the visitor is no longer logged in / eligible.
  private errorMessage_(res: Result<unknown>, submit: boolean): string {
    const code = res.code || 'internal';
    const t = this.t_;
    if (AUTH_CODES.indexOf(code) >= 0) {
      this.setMode_('anon');
      return t('errSession');
    }
    switch (code) {
      case 'not_eligible':
        this.setMode_('nonvoter');
        return this.opts_.upgradeText || t('upgradeCta');
      case 'own_idea':
        return t('errOwnIdea');
      case 'voting_closed':
        return t('errVotingClosed');
      case 'not_found':
        return t('errNotFound');
      case 'rate_limited':
        return t(submit ? 'errSubmitLimit' : 'errRateLimited');
      case 'invalid_input':
      case 'payload_too_large':
        return t('errInvalidInput');
      default:
        return t('errGeneric');
    }
  }

  // ---- suggest form ----

  private buildForm_(): HTMLElement {
    const t = this.t_;
    this.titleInput_ = h('input', {
      id: 'fv-f-title',
      type: 'text',
      maxlength: TITLE_MAX,
      required: true,
      autocomplete: 'off',
      'aria-describedby': 'fv-f-title-count',
    });
    this.bodyInput_ = h('textarea', { id: 'fv-f-body', maxlength: BODY_MAX, rows: 5, 'aria-describedby': 'fv-f-body-count' });
    this.titleCount_ = h('span', { id: 'fv-f-title-count', class: 'fv-counter' });
    this.bodyCount_ = h('span', { id: 'fv-f-body-count', class: 'fv-counter' });
    this.formError_ = h('p', { class: 'fv-form-error', role: 'alert', hidden: true });
    this.submitBtn_ = h('button', { type: 'submit', class: 'fv-btn fv-primary' }, t('submit'));
    const cancel = h('button', { type: 'button', class: 'fv-btn' }, t('cancel'));
    cancel.addEventListener('click', () => this.closeForm_(true));
    this.titleInput_.addEventListener('input', () => {
      this.titleInput_.removeAttribute('aria-invalid');
      this.formError_.hidden = true;
      this.updateCounters_();
    });
    this.bodyInput_.addEventListener('input', () => this.updateCounters_());
    const form = h(
      'form',
      { class: 'fv-form', novalidate: true, 'aria-labelledby': 'fv-form-title' },
      h('h3', { id: 'fv-form-title', class: 'fv-form-title' }, t('suggest')),
      h('p', { class: 'fv-hint' }, t('formHint')),
      h('label', { for: 'fv-f-title' }, t('formTitle')),
      this.titleInput_,
      this.titleCount_,
      h('label', { for: 'fv-f-body' }, t('formBody')),
      this.bodyInput_,
      this.bodyCount_,
      this.formError_,
      h('div', { class: 'fv-actions' }, cancel, this.submitBtn_),
    );
    form.addEventListener('submit', (e) => {
      e.preventDefault();
      this.submit_();
    });
    this.updateCounters_();
    return form;
  }

  private updateCounters_(): void {
    this.titleCount_.textContent = this.t_('counter', { count: this.titleInput_.value.length, max: TITLE_MAX });
    this.bodyCount_.textContent = this.t_('counter', { count: this.bodyInput_.value.length, max: BODY_MAX });
  }

  private openForm_(): void {
    this.success_.hidden = true;
    this.formError_.hidden = true;
    this.formWrap_.hidden = false;
    this.content_.hidden = true;
    this.suggestBtn_.setAttribute('aria-expanded', 'true');
    this.footer_.hidden = true;
    this.body_.scrollTop = 0;
    this.titleInput_.focus();
  }

  private closeForm_(focusButton: boolean): void {
    this.formWrap_.hidden = true;
    this.content_.hidden = false;
    this.formError_.hidden = true;
    this.suggestBtn_.setAttribute('aria-expanded', 'false');
    this.footer_.hidden = false;
    this.measure_();
    if (focusButton && this.mode_ === 'voter') this.suggestBtn_.focus();
  }

  private showFormError_(msg: string): void {
    this.formError_.textContent = msg;
    this.formError_.hidden = false;
  }

  private async submit_(): Promise<void> {
    if (this.submitting_) return;
    const title = this.titleInput_.value.trim();
    const body = this.bodyInput_.value.trim();
    if (!title) {
      this.titleInput_.setAttribute('aria-invalid', 'true');
      this.showFormError_(this.t_('titleRequired'));
      this.titleInput_.focus();
      return;
    }
    this.submitting_ = true;
    this.formError_.hidden = true;
    this.submitBtn_.textContent = this.t_('submitting');
    this.submitBtn_.setAttribute('aria-disabled', 'true');
    const res = await this.api_.createIdea(title, body);
    this.submitting_ = false;
    this.submitBtn_.textContent = this.t_('submit');
    this.submitBtn_.removeAttribute('aria-disabled');

    if (res.ok && res.data) {
      const idea: Idea = res.data;
      idea.moderation_state = 'pending';
      this.pending_.unshift(idea);
      this.ownIds_.add(idea.id);
      this.renderPending_();
      this.titleInput_.value = '';
      this.bodyInput_.value = '';
      this.updateCounters_();
      this.closeForm_(true);
      this.success_.textContent = this.t_('submitted');
      this.success_.hidden = false;
      this.announce_(this.t_('submitted'));
      return;
    }
    const msg = this.errorMessage_(res, true);
    if (this.mode_ === 'voter') this.showFormError_(msg);
    else this.toast_(msg);
  }

  // ---- feedback ----

  private toast_(msg: string): void {
    this.toastEl_.textContent = msg;
    this.toastEl_.hidden = false;
    clearTimeout(this.toastTimer_);
    this.toastTimer_ = window.setTimeout(() => {
      this.toastEl_.hidden = true;
    }, TOAST_MS);
  }

  private announce_(msg: string): void {
    this.live_.textContent = '';
    window.setTimeout(() => {
      this.live_.textContent = msg;
    }, 50);
  }

  // ---- keyboard ----

  private onKey_(e: KeyboardEvent): void {
    if (e.key === 'Escape' || e.key === 'Esc') {
      e.preventDefault();
      e.stopPropagation();
      this.close();
      return;
    }
    if (e.key !== 'Tab') return;
    const items = this.focusables_();
    if (!items.length) {
      e.preventDefault();
      return;
    }
    const first = items[0];
    const last = items[items.length - 1];
    const active = this.shadow_.activeElement;
    const inside = !!active && this.panel_.contains(active);
    if (e.shiftKey && (!inside || active === first)) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && (!inside || active === last)) {
      e.preventDefault();
      first.focus();
    }
  }

  private focusables_(): HTMLElement[] {
    const all = Array.prototype.slice.call(this.panel_.querySelectorAll(FOCUSABLE)) as HTMLElement[];
    return all.filter(
      (el) => !(el as HTMLButtonElement).disabled && el.getAttribute('tabindex') !== '-1' && el.getClientRects().length > 0,
    );
  }
}
