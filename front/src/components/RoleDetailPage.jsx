import { useEffect, useRef, useState } from 'react';
import { api } from '../api.js';
import { t } from '../i18n.js';
import { useToastErr, useToastOk } from '../toast.jsx';
import { useNav } from '../App.jsx';
import StatusTab from './roleDetail/StatusTab.jsx';
import BroadcastListTab from './roleDetail/BroadcastListTab.jsx';
import FeedTabs from './roleDetail/FeedTabs.jsx';
import StreamPanel from './roleDetail/StreamPanel.jsx';
import BookPanel from './roleDetail/BookPanel.jsx';
import PersonaTab from './roleDetail/PersonaTab.jsx';
import SettingsTab from './roleDetail/SettingsTab.jsx';
import LogsTab from './roleDetail/LogsTab.jsx';
import { needsSocialdataKey } from './roleDetail/shared.js';
import { SHOW_BOOK, SHOW_LIVE } from '../features.js';

// 左侧分组导航：工作台 / 内容 / 角色。Feed 才有子模式；Stream 是单路 Chat。
// 图书与直播入口由 features.js 的开关控制（当前构建暂时隐藏）。
const TAB_GROUPS = [
  { sec: 'railWorkspace', tabs: ['status', 'broadcastList'] },
  { sec: 'railContent', tabs: ['feed', ...(SHOW_LIVE ? ['stream'] : []), ...(SHOW_BOOK ? ['book'] : [])] },
  { sec: 'railRole', tabs: ['persona', 'settings', 'logs'] },
];
const TAB_LABEL = {
  status: 'tabStatus', broadcastList: 'tabBroadcastList',
  feed: 'tabFeed', stream: 'tabStream', book: 'tabBook',
  persona: 'tabPersona', settings: 'tabSettings', logs: 'tabLogs',
};

export default function RoleDetailPage({ id }) {
  const [role, setRole] = useState(null);
  const [machines, setMachines] = useState([]);
  const [upstreams, setUpstreams] = useState([]);
  const [tab, setTab] = useState(() => {
    const q = new URLSearchParams(location.search).get('tab');
    return q && TAB_LABEL[q] ? q : 'status';
  });
  const [anchor, setAnchor] = useState(''); // 跳到某个 tab 后要滚到的元素 id
  const [llmReady, setLlmReady] = useState(false); // Settings 里 LLM 是否配好
  const [hasKey, setHasKey] = useState(true);     // socialdata API Key 是否配好
  const toastOk = useToastOk();
  const toastErr = useToastErr();
  const nav = useNav();
  const pendingAnchor = useRef('');

  const load = async () => {
    try {
      const r = await api('/api/roles/' + encodeURIComponent(id));
      setRole(r);
      setMachines(await api('/api/machines'));
      try { setUpstreams(await api('/api/upstreams')); } catch { setUpstreams([]); }
      try {
        const s = await api('/api/settings');
        setHasKey(!!(s && s.socialdata_api_key));
        setLlmReady(!!(s && s.llm_base_url && s.llm_api_key && s.llm_model));
      } catch { /* 设置拿不到就不拦 */ }
    } catch (e) { toastErr(e.message); }
  };
  useEffect(() => { load(); }, [id]);

  // goTab 跨 tab 跳转 + 滚到指定元素（例如「去 Persona 传 VRM」）。
  // 切 tab 是异步渲染，所以先把锚点记下来，等目标 tab 渲染完再滚。
  const goTab = (tb, anchorId = '') => {
    pendingAnchor.current = anchorId;
    setTab(tb);
    setAnchor('');
  };
  useEffect(() => {
    const a = pendingAnchor.current;
    if (!a) return;
    pendingAnchor.current = '';
    const el = document.getElementById(a);
    if (el) {
      el.scrollIntoView({ behavior: 'smooth', block: 'center' });
      setAnchor(a);
    }
  }, [tab]);

  // 跳到全局 Settings 页的 socialdata key（跨页面，用 hash 让目标页自己滚）
  const goGlobalKey = () => nav('/settings#socialdata-key');

  if (!role) return <p className="loading">{t('loading')}</p>;

  const del = async () => {
    if (!confirm(t('delRoleConfirm'))) return;
    try { await api('/api/roles/' + id, { method: 'DELETE' }); nav('/roles'); }
    catch (e) { toastErr(e.message); }
  };

  // 拉不了帖子的前置：twitter 没配 socialdata key / 没设 username（RSS 用 feed URL）。
  const missing = [];
  if (!hasKey && needsSocialdataKey(role.platform)) missing.push('key');
  if (!role.username) missing.push('user');

  return (
    <>
      {(!role.username && role.enabled) ? <div className="warn-banner">⚠️ {t('warnNoUser')}</div> : ''}
      <div className="role-shell">
        <div className="actionbar">
          <button className="mini secondary" onClick={() => nav('/roles')}>← {t('back')}</button>
          <RoleAvatar role={role} onSaved={load} />
          <h2>{role.name}</h2>
          <span className="spacer"></span>
          <button className="mini danger" onClick={del}>{t('delRole')}</button>
        </div>
        {missing.length > 0 && (
          <div className="warn-banner role-shell-banner" title={t('cantPullTip')}>
            ⚠️ {missing.includes('key') && missing.includes('user') ? t('cantPullBoth')
              : missing.includes('key') ? t('cantPullNoKey') : t('cantPullNoUser')}
            {missing.includes('key') && (
              <button className="mini ghost" type="button" onClick={goGlobalKey}>{t('goSetKey')}</button>
            )}
            {missing.includes('user') && (
              <button className="mini ghost" type="button" onClick={() => goTab('settings', 'role-username')}>{t('goSetUser')}</button>
            )}
          </div>
        )}
        <div className="role-layout">
          <nav className="role-rail" aria-label={role.name}>
            {TAB_GROUPS.map((group) => (
              <div key={group.sec} className="rail-group">
                <p className="role-rail-sec">{t(group.sec)}</p>
                {group.tabs.map((tb) => (
                  <button
                    key={tb}
                    type="button"
                    className={'rail-item' + (tb === tab ? ' active' : '')}
                    aria-current={tb === tab ? 'page' : undefined}
                    onClick={() => setTab(tb)}
                  >{t(TAB_LABEL[tb])}</button>
                ))}
              </div>
            ))}
          </nav>
          <div className="role-main">
            {tab === 'status' && <StatusTab role={role} machines={machines} onSaved={load} goTab={goTab} />}
            {tab === 'broadcastList' && <BroadcastListTab role={role} />}
            {tab === 'feed' && <FeedTabs role={role} onSaved={load} goTab={goTab} />}
            {tab === 'stream' && SHOW_LIVE && <StreamPanel role={role} onSaved={load} />}
            {tab === 'book' && SHOW_BOOK && <BookPanel key={role.id} role={role} />}
            {tab === 'persona' && <PersonaTab role={role} machines={machines} onSaved={load} />}
            {tab === 'settings' && <SettingsTab role={role} machines={machines} upstreams={upstreams} onSaved={load} llmReady={llmReady} />}
            {tab === 'logs' && <LogsTab />}
          </div>
        </div>
      </div>
    </>
  );
}

// 顶栏头像：点击即选图上传（png/jpg/webp/gif ≤5MB），hover 有 title 提示。
// 从 Persona 卡里挪出来——它不属于 VRM，且不该占一张卡的位置。
function RoleAvatar({ role, onSaved }) {
  const toastOk = useToastOk();
  const toastErr = useToastErr();
  const ref = useRef(null);
  const upload = async (f) => {
    if (!f) return;
    const fd = new FormData(); fd.append('file', f);
    try {
      await api(`/api/roles/${role.id}/icon`, { method: 'POST', body: fd });
      toastOk(t('saved')); onSaved();
    } catch (e) { toastErr(e.message); }
  };
  return (
    <label title={t('iconHint')} style={{ cursor: 'pointer', flexShrink: 0 }}>
      {role.icon
        ? <img src={`/api/roles/${encodeURIComponent(role.id)}/icon?v=${encodeURIComponent(role.updated_at || '')}`} alt="" width="36" height="36" style={{ borderRadius: 10, objectFit: 'cover', display: 'block', background: 'var(--bg-soft)' }} />
        : <span style={{ width: 36, height: 36, borderRadius: 10, background: 'var(--bg-soft)', display: 'flex', alignItems: 'center', justifyContent: 'center', fontSize: 18, color: 'var(--mut)' }}>{(role.name || '?').trim().charAt(0).toUpperCase()}</span>}
      <input ref={ref} type="file" accept="image/png,image/jpeg,image/webp,image/gif" style={{ display: 'none' }}
        onChange={(e) => { const f = e.target.files[0]; e.target.value = ''; upload(f); }} />
    </label>
  );
}
