import en, { MessageKey, Messages } from './locales/en';
import fr from './locales/fr';
import de from './locales/de';
import es from './locales/es';
import it from './locales/it';
import zh from './locales/zh';

export type { MessageKey };

export const LOCALES: Record<string, Messages> = { en, fr, de, es, it, zh };

export type Translator = (key: MessageKey, vars?: Record<string, string | number>) => string;

// resolveLocale takes candidates in priority order (data-locale, <html lang>, navigator.language),
// uses the first non-empty one, keeps its primary subtag (fr-CA -> fr, zh-Hans-CN -> zh) and falls
// back to English when that language is not supported.
export function resolveLocale(candidates: Array<string | null | undefined>): string {
  for (const c of candidates) {
    if (!c || !c.trim()) continue;
    const primary = c.trim().toLowerCase().split(/[-_]/)[0];
    return Object.prototype.hasOwnProperty.call(LOCALES, primary) ? primary : 'en';
  }
  return 'en';
}

// createTranslator returns t(key, vars): a missing or empty key in the locale falls back to English.
export function createTranslator(locale: string): Translator {
  const messages = Object.prototype.hasOwnProperty.call(LOCALES, locale) ? LOCALES[locale] : en;
  return (key, vars) => {
    const msg = messages[key] || en[key] || key;
    if (!vars) return msg;
    return msg.replace(/\{(\w+)\}/g, (m, name: string) =>
      Object.prototype.hasOwnProperty.call(vars, name) ? String(vars[name]) : m,
    );
  };
}
