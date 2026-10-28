// ---- i18n（en/ja/zh，兜底 en）----
// 词典按语言+域拆在同目录 locales/ 下：{en,ja,zh}.{core,flows}.js
// （core = 主控台界面，flows = 向导/直播/ChatRoom/图书等外围功能流；加新 key 前先 grep 这几个文件）
import enCore from './locales/en.core.js';
import enFlows from './locales/en.flows.js';
import jaCore from './locales/ja.core.js';
import jaFlows from './locales/ja.flows.js';
import zhCore from './locales/zh.core.js';
import zhFlows from './locales/zh.flows.js';

const en = { ...enCore, ...enFlows };
const ja = { ...jaCore, ...jaFlows };
const zh = { ...zhCore, ...zhFlows };

export const I18N = Object.fromEntries(
  Object.keys(en).map((k) => [k, { en: en[k], ja: ja[k], zh: zh[k] }]),
);

// 默认语言：浏览器区域若是中文/日文则跟随，否则英文（en 为全局兜底）。
const browser = (navigator.language || '').toLowerCase();
const browserLang = browser.startsWith('zh') ? 'zh' : (browser.startsWith('ja') ? 'ja' : 'en');
let LANG = localStorage.getItem('lang') || browserLang;

export const getLang = () => LANG;
export const setLang = (l) => { LANG = l; localStorage.setItem('lang', l); };
export const t = (k) => (I18N[k] && (I18N[k][LANG] || I18N[k].en)) || k;
