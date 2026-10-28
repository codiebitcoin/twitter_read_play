import { useState, useEffect, useCallback, useRef } from 'react';
import { t, getLang, setLang } from './i18n.js';
import { api } from './api.js';
import { SHOW_LIVE, SHOW_LAB } from './features.js';
import HomePage from './components/HomePage.jsx';
import MachinesPage from './components/MachinesPage.jsx';
import MachineDetailPage from './components/MachineDetailPage.jsx';
import RolesPage from './components/RolesPage.jsx';
import AdvancePage from './components/AdvancePage.jsx';
import RoleDetailPage from './components/RoleDetailPage.jsx';
import RoleAssetsSetupPage from './components/RoleAssetsSetupPage.jsx';
import SettingsPage from './components/SettingsPage.jsx';
import LogsPage from './components/LogsPage.jsx';
import MessageQueue from './components/MessageQueue.jsx';

// ---- 轻量路由（History API，路由表与原 SPA 相同）----
const routes = [
  [/^\/$/, () => <HomePage />],
  [/^\/machines\/?$/, () => <MachinesPage />],
  [/^\/machines\/([^/]+)\/?$/, (m) => <MachineDetailPage id={decodeURIComponent(m[1])} />],
  [/^\/roles\/?$/, () => <RolesPage />],
  [/^\/advance\/?$/, () => <AdvancePage />],
  // 资源补全专页要放在 /roles/{id} 之前（正则顺序匹配）
  [/^\/roles\/([^/]+)\/setup\/?$/, (m) => <RoleAssetsSetupPage id={decodeURIComponent(m[1])} />],
  [/^\/roles\/([^/]+)\/?$/, (m) => <RoleDetailPage id={decodeURIComponent(m[1])} />],
  [/^\/settings\/?$/, () => <SettingsPage />],
  [/^\/logs\/?$/, () => <LogsPage />],
];

// 旧的独立直播页并进设置。地址栏还停在 /live 时直接打开设置里的直播标签
// （直播入口由 features.js 的 SHOW_LIVE 控制，隐藏时统一回设置首页）。
function canonicalPath() {
  if (/^\/live\/?$/.test(location.pathname)) {
    history.replaceState(null, '', SHOW_LIVE ? '/settings#live' : '/settings');
    return '/settings';
  }
  return location.pathname;
}

export const RouterCtx = { nav: null };

// ---- 主题（Ethereal Glass 夜/昼）：localStorage 优先，未设置时跟随系统 ----
export function applyTheme(theme) {
  document.documentElement.dataset.theme = theme;
  try { localStorage.setItem('theme', theme); } catch {}
}
function initialTheme() {
  try {
    const v = localStorage.getItem('theme');
    if (v === 'light' || v === 'dark') return v;
  } catch {}
  return window.matchMedia?.('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
}
applyTheme(initialTheme()); // 首帧前应用，避免闪色

export default function App() {
  const [path, setPath] = useState(canonicalPath);
  const [langTick, setLangTick] = useState(0);

  useEffect(() => {
    const onPop = () => setPath(canonicalPath());
    window.addEventListener('popstate', onPop);
    return () => window.removeEventListener('popstate', onPop);
  }, []);

  // 路由 state 只存 pathname。nav('/settings#socialdata-key') 这类带 hash 的跳转
  // 若把整段写入 path，正则 /^\/settings\/?$/ 匹配失败，main 会渲成空白页。
  const nav = useCallback((p) => {
    history.pushState(null, '', p);
    setPath(canonicalPath());
    window.scrollTo(0, 0);
  }, []);
  RouterCtx.nav = nav;

  // 主题状态镜像：applyTheme 写 DOM + localStorage，这里同步触发重渲染更新按钮图标
  const [, setThemeTick] = useState(0);
  const setThemeMode = useCallback((mode) => {
    applyTheme(mode);
    setThemeTick((x) => x + 1);
  }, []);
  const theme = document.documentElement.dataset.theme === 'light' ? 'light' : 'dark';
  const logout = async () => {
    try { await api('/api/logout', { method: 'POST' }); } catch {}
    location.href = '/login';
  };

  useEffect(() => {
    // 未知路径回首页（导航页是新的入口；角色页仍可从顶部导航进入）
    if (!routes.some(([re]) => re.test(location.pathname))) location.replace('/');
  }, [path]);

  const pathname = path.split(/[?#]/)[0] || '/';
  let page = null;
  for (const [re, fn] of routes) {
    const m = pathname.match(re);
    if (m) { page = fn(m); break; }
  }

  const tabs = [
    ['/', 'home'],
    ['/machines', 'machines'],
    ['/roles', 'roles'],
    ...(SHOW_LAB ? [['/advance', 'advance']] : []),
    ['/settings', 'settings'],
    ['/logs', 'logs'],
  ];

  return (
    <>
      <header>
        <h1 style={{ cursor: 'pointer' }} onClick={() => nav('/')}>📣 Codie Media Broadcast</h1>
        <nav>
          {tabs.map(([route, key]) => {
            // 首页用精确匹配：否则 "/" 会让所有页面都点亮「首页」
            const active = route === '/' ? pathname === '/' : pathname.startsWith(route);
            return (
              <a
                key={route}
                href={route}
                className={'tab' + (active ? ' active' : '')}
                onClick={(e) => { e.preventDefault(); nav(route); }}
              >{t(key)}</a>
            );
          })}
        </nav>
        <div className="header-tools">
          <HeaderMenu label={t('language')} icon={<IconGlobe />}>
            {LANG_OPTIONS.map(([code, name]) => (
              <button
                key={code}
                type="button"
                role="menuitemradio"
                aria-checked={getLang() === code}
                className={'hmenu-item' + (getLang() === code ? ' active' : '')}
                onClick={() => { setLang(code); setLangTick((x) => x + 1); }}
              >{name}</button>
            ))}
          </HeaderMenu>
          <HeaderMenu label={t('themeToggle')} icon={theme === 'light' ? <IconMoon /> : <IconSun />}>
            <button
              type="button" role="menuitemradio" aria-checked={theme === 'light'}
              className={'hmenu-item' + (theme === 'light' ? ' active' : '')}
              onClick={() => setThemeMode('light')}
            ><IconSun />{t('themeLight')}</button>
            <button
              type="button" role="menuitemradio" aria-checked={theme === 'dark'}
              className={'hmenu-item' + (theme === 'dark' ? ' active' : '')}
              onClick={() => setThemeMode('dark')}
            ><IconMoon />{t('themeDark')}</button>
          </HeaderMenu>
          <HeaderMenu label={t('logout')} icon={<IconPower />}>
            <button type="button" role="menuitem" className="hmenu-item" onClick={logout}>
              <IconPower />{t('logout')}
            </button>
          </HeaderMenu>
        </div>
      </header>
      <main key={langTick}>{page}</main>
      <MessageQueue />
    </>
  );
}

export function useNav() {
  return (p) => RouterCtx.nav && RouterCtx.nav(p);
}

// ---- 顶栏图标菜单：语言 / 主题 / 退出。按钮只放图标，点开才展开选项 ----
const LANG_OPTIONS = [['en', 'English'], ['ja', '日本語'], ['zh', '中文']];

const svgProps = {
  width: 18, height: 18, viewBox: '0 0 24 24', fill: 'none',
  stroke: 'currentColor', strokeWidth: 1.8, strokeLinecap: 'round', strokeLinejoin: 'round',
};

function IconGlobe() {
  return (
    <svg {...svgProps} aria-hidden="true">
      <circle cx="12" cy="12" r="9" />
      <path d="M3 12h18" />
      <path d="M12 3c2.5 2.7 3.8 5.7 3.8 9s-1.3 6.3-3.8 9c-2.5-2.7-3.8-5.7-3.8-9S9.5 5.7 12 3z" />
    </svg>
  );
}

function IconSun() {
  return (
    <svg {...svgProps} aria-hidden="true">
      <circle cx="12" cy="12" r="4" />
      <path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" />
    </svg>
  );
}

function IconMoon() {
  return (
    <svg {...svgProps} aria-hidden="true">
      <path d="M20 14.5A8 8 0 0 1 9.5 4a8 8 0 1 0 10.5 10.5z" />
    </svg>
  );
}

function IconPower() {
  return (
    <svg {...svgProps} aria-hidden="true">
      <path d="M12 3v8" />
      <path d="M7.1 6.3a8 8 0 1 0 9.8 0" />
    </svg>
  );
}

// HeaderMenu 点外部（或按 Esc）关闭；点菜单里的任意一项后也关闭。
function HeaderMenu({ label, icon, children }) {
  const [open, setOpen] = useState(false);
  const ref = useRef(null);
  useEffect(() => {
    if (!open) return;
    const onDown = (e) => { if (!ref.current || !ref.current.contains(e.target)) setOpen(false); };
    const onKey = (e) => { if (e.key === 'Escape') setOpen(false); };
    document.addEventListener('mousedown', onDown);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('mousedown', onDown);
      document.removeEventListener('keydown', onKey);
    };
  }, [open]);
  return (
    <div className="hmenu" ref={ref}>
      <button
        type="button"
        className="hmenu-btn"
        title={label}
        aria-label={label}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
      >{icon}</button>
      {open && (
        <div className="hmenu-pop" role="menu" onClick={() => setOpen(false)}>{children}</div>
      )}
    </div>
  );
}
