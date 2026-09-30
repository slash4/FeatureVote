// FeatureVote embeddable widget: bootstrap. Reads the <script> data-* attributes, mounts a Shadow DOM
// host and exposes window.FeatureVote (the only global the widget defines).

import { Api } from './api';
import { createTranslator, resolveLocale } from './i18n';
import { TokenManager, TokenProvider } from './token';
import { Widget } from './ui';

interface FeatureVoteApi {
  open(): void;
  close(): void;
  toggle(): void;
  refresh(): void;
  setTokenProvider(fn: TokenProvider | null): void;
}

declare global {
  interface Window {
    FeatureVote?: FeatureVoteApi;
    FeatureVoteConfig?: { getToken?: TokenProvider };
  }
}

function findScript(): HTMLScriptElement | null {
  const cur = document.currentScript;
  if (cur && cur.tagName === 'SCRIPT') return cur as HTMLScriptElement;
  return (
    document.querySelector<HTMLScriptElement>('script[src*="widget.js"][data-api]') ||
    document.querySelector<HTMLScriptElement>('script[src*="widget.js"]')
  );
}

function boot(): void {
  if (window.FeatureVote) return; // already initialised: never mount twice

  const script = findScript();
  const ds: DOMStringMap = script ? script.dataset : {};
  let apiBase = ds.api || '';
  if (!apiBase && script && script.src) {
    try {
      apiBase = new URL(script.src, location.href).origin;
    } catch {
      apiBase = '';
    }
  }

  const cfg = window.FeatureVoteConfig;
  const provider = cfg && typeof cfg.getToken === 'function' ? cfg.getToken : null;
  const tokens = new TokenManager(ds.tokenUrl || null, provider);
  const api = new Api(apiBase, tokens);
  const locale = resolveLocale([ds.locale, document.documentElement.getAttribute('lang'), navigator.language]);

  let widget: Widget | null = null;
  const queued: Array<(w: Widget) => void> = [];
  const withWidget = (fn: (w: Widget) => void) => {
    if (widget) fn(widget);
    else queued.push(fn);
  };

  window.FeatureVote = {
    open: () => withWidget((w) => w.open()),
    close: () => withWidget((w) => w.close()),
    toggle: () => withWidget((w) => w.toggle()),
    refresh: () => {
      tokens.invalidate();
      withWidget((w) => w.refresh());
    },
    setTokenProvider: (fn) => {
      tokens.setProvider(typeof fn === 'function' ? fn : null);
      withWidget((w) => w.refresh());
    },
  };

  const mount = () => {
    const host = document.createElement('div');
    host.id = 'featurevote-root';
    const shadow = host.attachShadow({ mode: 'open' });
    document.body.appendChild(host);
    const w = new Widget(shadow, api, createTranslator(locale), {
      locale,
      position: ds.position,
      accent: ds.accent,
      loginUrl: ds.loginUrl,
      loginText: ds.loginText,
      loginLinkText: ds.loginLinkText,
      upgradeUrl: ds.upgradeUrl,
      upgradeText: ds.upgradeText,
      upgradeLinkText: ds.upgradeLinkText,
    });
    widget = w;
    queued.splice(0).forEach((fn) => fn(w));
  };

  if (document.body) mount();
  else document.addEventListener('DOMContentLoaded', mount);
}

boot();
