// 设置页外壳：子页签切换、#tab/anchor 深链（锚点滚动 + 闪烁高亮）。
// 各子页内容在 settings/ 目录：AccountTab/FeedTab/LiveTab/AdvancedTab/UpstreamsTab
// 在 SettingsTabs.jsx，浏览器登录卡在 settings/BrowserLogin.jsx，渠道区块在 settings/Channels.jsx。
import { useEffect, useState } from 'react';
import { api } from '../api.js';
import { t } from '../i18n.js';
import { SHOW_LIVE } from '../features.js';
import { AccountTab, AdvancedTab, FeedTab, LiveTab, UpstreamsTab } from './settings/SettingsTabs.jsx';

const TABS = ['account', 'feed', ...(SHOW_LIVE ? ['live'] : []), 'advanced', 'upstreams'];
const TAB_LABEL = { account: 'account', feed: 'feed', live: 'navLive', advanced: 'advanced', upstreams: 'upstreams' };

export default function SettingsPage() {
  const [st, setSt] = useState(null);
  // hash 支持两段式「tab/anchor」：#feed/browser-login、#feed/socialdata-key、
  // #feed/feed-channels、#advanced/llm-api… —— 让别的页面能精准跳到设置里的某张卡。
  // 兼容旧的一段式 #socialdata-key（归入 feed）。
  const [hashTab, hashAnchor] = ((location.hash || '').slice(1)).split('/');
  const initialTab = TABS.includes(hashTab) ? hashTab
    : (hashTab === 'socialdata-key' || hashAnchor === 'socialdata-key' ? 'feed' : 'account');
  const [tab, setTab] = useState(initialTab);
  const [anchor] = useState(hashTab === 'socialdata-key' ? 'socialdata-key' : (hashAnchor || ''));

  const load = async () => {
    try {
      const [settings, setup] = await Promise.all([
        api('/api/settings'),
        api('/api/setup/status').catch(() => ({})),
      ]);
      setSt({ ...settings, pc_mode: !!setup.pc_mode, desktop: !!setup.desktop });
    } catch (e) { console.error(e); }
  };
  useEffect(() => { load(); }, []);
  useEffect(() => { history.replaceState(null, '', '/settings#' + tab); }, [tab]);

  // 带 anchor 进入时：等目标卡渲染完，滚动到它并闪烁高亮一次。
  useEffect(() => {
    if (!anchor) return;
    const t = setTimeout(() => {
      const el = document.getElementById(anchor);
      if (!el) return;
      el.scrollIntoView({ behavior: 'smooth', block: 'center' });
      el.classList.add('hash-flash');
      setTimeout(() => el.classList.remove('hash-flash'), 3600);
    }, 150);
    return () => clearTimeout(t);
  }, [anchor, tab]);

  if (!st) return <p className="loading">{t('loading')}</p>;

  return (
    <>
      <div className="page-head"><h2>{t('settingsTitle')}</h2></div>
      <nav className="subtabs">
        {TABS.map((tb) => (
          <button key={tb} className={'tab mini' + (tb === tab ? ' active' : '')} onClick={() => setTab(tb)}>{t(TAB_LABEL[tb])}</button>
        ))}
      </nav>
      {tab === 'account' && <AccountTab st={st} onSaved={load} />}
      {tab === 'feed' && <FeedTab st={st} onSaved={load} />}
      {tab === 'live' && <LiveTab />}
      {tab === 'advanced' && <AdvancedTab st={st} onSaved={load} />}
      {tab === 'upstreams' && <UpstreamsTab />}
    </>
  );
}
