// CSS injected into the shadow root. Nothing here leaks to the host page, and `:host { all: initial }`
// (important, so it also beats hostile `* { ... !important }` host rules) stops inherited host styles
// from leaking in.

const DEFAULT_ACCENT = '#4f46e5';

function parseHex(hex: string): [number, number, number] {
  let h = hex.slice(1);
  if (h.length === 3) h = h[0] + h[0] + h[1] + h[1] + h[2] + h[2];
  return [0, 2, 4].map((i) => parseInt(h.slice(i, i + 2), 16)) as [number, number, number];
}

function luminance(hex: string): number {
  const [r, g, b] = parseHex(hex).map((v) => {
    const c = v / 255;
    return c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4);
  });
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

function contrast(a: number, b: number): number {
  return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
}

export function normalizeAccent(value: string | undefined): string {
  return value && /^#([0-9a-f]{3}|[0-9a-f]{6})$/i.test(value.trim()) ? value.trim() : DEFAULT_ACCENT;
}

// Picks white or near-black text for the accent, whichever contrasts more.
export function onAccent(accent: string): string {
  const l = luminance(accent);
  return contrast(l, 1) >= contrast(l, luminance('#111827')) ? '#ffffff' : '#111827';
}

// buildCss returns the shadow-root stylesheet with the accent variables.
// The accent is validated as #rgb/#rrggbb before it gets anywhere near the CSS text.
export function buildCss(accentInput: string | undefined): string {
  const accent = normalizeAccent(accentInput);
  // The focus ring must be visible on the white panel: fall back to dark grey for pale accents.
  const focus = contrast(luminance(accent), 1) >= 3 ? accent : '#1f2937';
  return CSS + '.fv-root{--fv-accent:' + accent + ';--fv-on-accent:' + onAccent(accent) + ';--fv-focus:' + focus + '}';
}

const CSS = `:host{all:initial !important;}
.fv-root{--fv-fg:#1f2937;--fv-muted:#4b5563;--fv-bg:#ffffff;--fv-soft:#f5f6f8;--fv-border:#e3e5ea;--fv-field:#6b7280;
font-family:system-ui,-apple-system,"Segoe UI",Roboto,"Noto Sans","PingFang SC","Microsoft YaHei",sans-serif;
font-size:14px;line-height:1.45;color:var(--fv-fg);-webkit-text-size-adjust:100%;}
*,::before,::after{box-sizing:border-box;}
[hidden]{display:none !important;}
button,input,select,textarea{font:inherit;color:inherit;margin:0;}
button{cursor:pointer;}
button:disabled{cursor:not-allowed;}
:focus-visible{outline:2px solid var(--fv-focus);outline-offset:2px;}
:focus:not(:focus-visible){outline:none;}
.fv-sr{position:absolute !important;width:1px;height:1px;padding:0;margin:-1px;overflow:hidden;clip:rect(0,0,0,0);white-space:nowrap;border:0;}

.fv-launcher{position:fixed;z-index:2147483000;display:inline-flex;align-items:center;gap:8px;min-height:48px;max-width:calc(100vw - 32px);padding:10px 18px 10px 14px;border:0;border-radius:999px;background:var(--fv-accent);color:var(--fv-on-accent);font-weight:600;font-size:15px;line-height:1.2;box-shadow:0 6px 20px rgba(17,24,39,.22);}
.fv-launcher svg{flex:none;}
.fv-launcher-text{overflow:hidden;text-overflow:ellipsis;white-space:nowrap;}
.fv-bottom-right .fv-launcher{right:20px;bottom:20px;}
.fv-bottom-left .fv-launcher{left:20px;bottom:20px;}
.fv-top-right .fv-launcher{right:20px;top:20px;}
.fv-top-left .fv-launcher{left:20px;top:20px;}

.fv-panel{position:fixed;z-index:2147483000;display:flex;flex-direction:column;width:380px;max-width:calc(100vw - 32px);max-height:calc(100vh - 96px);max-height:min(640px,calc(100vh - 96px));background:var(--fv-bg);border:1px solid var(--fv-border);border-radius:14px;box-shadow:0 18px 50px rgba(17,24,39,.25);overflow:hidden;animation:fv-in .18s ease-out;}
.fv-bottom-right .fv-panel{right:20px;bottom:80px;}
.fv-bottom-left .fv-panel{left:20px;bottom:80px;}
.fv-top-right .fv-panel{right:20px;top:80px;}
.fv-top-left .fv-panel{left:20px;top:80px;}
@keyframes fv-in{from{opacity:0;transform:translateY(8px);}to{opacity:1;transform:none;}}

.fv-header{flex:none;display:flex;align-items:flex-start;gap:8px;padding:12px 10px 8px 16px;}
.fv-title{flex:1 1 auto;min-width:0;margin:0;padding-top:8px;font-size:16px;line-height:1.3;font-weight:700;overflow-wrap:anywhere;hyphens:auto;}
.fv-icon-btn{flex:none;display:inline-flex;align-items:center;justify-content:center;width:40px;height:40px;padding:0;border:0;border-radius:8px;background:transparent;color:var(--fv-muted);}
.fv-icon-btn:hover{background:var(--fv-soft);color:var(--fv-fg);}

.fv-toolbar{flex:none;display:flex;flex-wrap:wrap;align-items:center;justify-content:space-between;gap:8px 12px;padding:0 16px 10px;border-bottom:1px solid var(--fv-border);}
.fv-seg{display:inline-flex;flex-wrap:wrap;gap:2px;padding:2px;border-radius:9px;background:var(--fv-soft);border:1px solid var(--fv-border);}
.fv-seg button{min-height:34px;padding:4px 12px;border:0;border-radius:7px;background:transparent;font-weight:600;font-size:13px;color:var(--fv-muted);}
.fv-seg button[aria-pressed="true"]{background:var(--fv-bg);color:var(--fv-fg);box-shadow:0 1px 2px rgba(17,24,39,.15);}
.fv-filter{display:flex;align-items:center;gap:6px;min-width:0;max-width:100%;}
.fv-filter label{font-size:13px;color:var(--fv-muted);font-weight:600;}
.fv-filter select{min-width:0;max-width:100%;min-height:34px;padding:4px 8px;border:1px solid var(--fv-field);border-radius:8px;background:var(--fv-bg);font-size:13px;}

.fv-body{flex:1 1 auto;min-height:0;overflow-y:auto;overflow-x:hidden;overscroll-behavior:contain;padding:12px 16px 16px;}
.fv-state{margin:0;padding:28px 8px;text-align:center;color:var(--fv-muted);overflow-wrap:anywhere;}
.fv-state-actions{display:flex;justify-content:center;}

.fv-list{list-style:none;margin:0;padding:0;display:flex;flex-direction:column;gap:10px;}
.fv-card{display:flex;gap:12px;align-items:flex-start;padding:12px;border:1px solid var(--fv-border);border-radius:10px;background:var(--fv-bg);}
.fv-vote{flex:none;display:flex;flex-direction:column;align-items:center;gap:2px;width:40px;}
.fv-vote-btn{display:inline-flex;align-items:center;justify-content:center;width:40px;height:36px;padding:0;border:1px solid var(--fv-border);border-radius:8px;background:var(--fv-bg);color:var(--fv-muted);}
.fv-vote-btn:hover:not(:disabled){border-color:var(--fv-accent);color:var(--fv-fg);}
.fv-vote-btn[aria-pressed="true"]{background:var(--fv-accent);border-color:var(--fv-accent);color:var(--fv-on-accent);}
.fv-vote-btn.fv-down[aria-pressed="true"]{background:#374151;border-color:#374151;color:#ffffff;}
.fv-vote-btn:disabled{opacity:.45;}
.fv-score{font-weight:700;font-variant-numeric:tabular-nums;font-size:15px;line-height:1.4;}
.fv-main{flex:1 1 auto;min-width:0;}
.fv-card-title{margin:0;font-size:15px;line-height:1.35;font-weight:600;overflow-wrap:anywhere;hyphens:auto;}
.fv-card-body{margin:4px 0 0;color:var(--fv-muted);white-space:pre-wrap;overflow-wrap:anywhere;}
.fv-clamp{display:-webkit-box;-webkit-line-clamp:3;-webkit-box-orient:vertical;overflow:hidden;}
.fv-link-btn{display:inline-flex;align-items:center;min-height:28px;padding:0;border:0;background:transparent;color:var(--fv-fg);font-size:13px;font-weight:600;text-decoration:underline;text-underline-offset:2px;}
.fv-meta{display:flex;flex-wrap:wrap;align-items:center;gap:6px 10px;margin-top:8px;}
.fv-chip{display:inline-flex;align-items:center;gap:5px;padding:2px 9px;border-radius:999px;font-size:12px;font-weight:600;line-height:1.5;background:#eef1f5;color:#334155;}
.fv-chip::before{content:"";width:6px;height:6px;border-radius:50%;background:currentColor;flex:none;}
.fv-st-planned{background:#ede9fe;color:#5b21b6;}
.fv-st-in_progress{background:#fef3c7;color:#92400e;}
.fv-st-shipped{background:#dcfce7;color:#166534;}
.fv-st-declined{background:#fee2e2;color:#991b1b;}
.fv-st-pending{background:#e0f2fe;color:#075985;}
.fv-note{font-size:12px;color:var(--fv-muted);}

.fv-pending{margin:0 0 14px;padding:10px;border:1px dashed #7dd3fc;border-radius:10px;background:#f7fbff;}
.fv-section-title{margin:0 0 8px;font-size:13px;font-weight:700;color:var(--fv-fg);}
.fv-pending .fv-card{padding:10px;}

.fv-form{display:flex;flex-direction:column;gap:6px;}
.fv-form-title{margin:0 0 4px;font-size:15px;font-weight:700;}
.fv-hint{margin:0 0 6px;font-size:13px;color:var(--fv-muted);}
.fv-form label{font-weight:600;font-size:13px;}
.fv-form input,.fv-form textarea{display:block;width:100%;padding:8px 10px;border:1px solid var(--fv-field);border-radius:8px;background:var(--fv-bg);font-size:14px;}
.fv-form textarea{min-height:110px;resize:vertical;}
.fv-counter{align-self:flex-end;font-size:12px;color:var(--fv-muted);font-variant-numeric:tabular-nums;margin-bottom:4px;}
.fv-form-error{margin:0;padding:8px 10px;border-radius:8px;background:#fee2e2;color:#991b1b;font-size:13px;}
.fv-actions{display:flex;flex-wrap:wrap;gap:8px;justify-content:flex-end;margin-top:4px;}
.fv-btn{display:inline-flex;align-items:center;justify-content:center;min-height:40px;padding:6px 16px;border-radius:8px;border:1px solid var(--fv-border);background:var(--fv-bg);font-weight:600;text-align:center;}
.fv-btn:hover{background:var(--fv-soft);}
.fv-primary{background:var(--fv-accent);border-color:var(--fv-accent);color:var(--fv-on-accent);}
.fv-primary:hover{background:var(--fv-accent);}
.fv-wide{width:100%;margin-top:12px;}
.fv-success{margin:0 0 12px;padding:10px 12px;border-radius:8px;background:#dcfce7;color:#166534;overflow-wrap:anywhere;}

.fv-toast{flex:none;margin:0 16px 10px;padding:10px 12px;border-radius:8px;background:#1f2937;color:#ffffff;font-size:13px;overflow-wrap:anywhere;}
.fv-footer{flex:none;display:flex;flex-wrap:wrap;align-items:center;justify-content:space-between;gap:8px 12px;padding:12px 16px;border-top:1px solid var(--fv-border);background:var(--fv-soft);}
.fv-cta-text{flex:1 1 180px;min-width:0;margin:0;font-size:13px;overflow-wrap:anywhere;}
.fv-cta-link{display:inline-flex;align-items:center;justify-content:center;min-height:40px;padding:6px 14px;border-radius:8px;background:var(--fv-accent);color:var(--fv-on-accent);font-weight:600;text-decoration:none;text-align:center;overflow-wrap:anywhere;}
.fv-footer .fv-primary{flex:1 1 auto;}

@media (max-width:480px){
  .fv-root .fv-panel{left:0;right:0;bottom:0;top:auto;width:auto;max-width:none;max-height:90vh;border-radius:16px 16px 0 0;border-left:0;border-right:0;border-bottom:0;}
  .fv-seg button,.fv-filter select,.fv-link-btn{min-height:40px;}
  .fv-vote-btn{height:40px;}
  .fv-launcher-text{max-width:calc(100vw - 110px);}
}
@media (prefers-reduced-motion:reduce){
  *{animation:none !important;}
}
`;
