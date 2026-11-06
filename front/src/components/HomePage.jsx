// HomePage：`/` 导航首页。
//
// 一屏看清「设备 / 角色 / 绑定 / 数据源」是否配齐，并给出两个入口：
//   - 「＋ 添加」打开分步向导（设备 → 角色 → 绑定 → 同步），与参考项目
//     codie_life_control 的 SetupWizard 同序，只是把 Account 一步换成了「设置角色」；
//   - 下面的设备/角色行直接跳到详情页，保留原来的完整 UI。
import { useEffect, useRef, useState } from 'react';
import { api, relTime } from '../api.js';
import { t } from '../i18n.js';
import { useNav } from '../App.jsx';
import { useToastErr } from '../toast.jsx';
import SyncRoleButton from './SyncRoleButton.jsx';
import SetupWizard from './wizard/SetupWizard.jsx';

export default function HomePage() {
  const nav = useNav();
  const toastErr = useToastErr();
  const [data, setData] = useState(null);
  const [wizOpen, setWizOpen] = useState(() => new URLSearchParams(location.search).get('wizard') === '1');

  const load = async () => {
    try {
      const [st, machines, roles, upstreams, logs] = await Promise.all([
        api('/api/setup/status'),
        api('/api/machines'),
        api('/api/roles'),
        api('/api/upstreams').catch(() => []),
        api('/api/logs').catch(() => []),
      ]);
      setData({ st, machines, roles, upstreams: upstreams || [], logs: logs || [] });
    } catch (e) { toastErr(e.message); }
  };
  useEffect(() => { load(); }, []);
  // ?wizard=1 是一次性入口：读完后从地址栏移除，刷新不会又弹一次。
  useEffect(() => {
    const u = new URL(location.href);
    if (u.searchParams.get('wizard') === '1') {
      u.searchParams.delete('wizard');
      history.replaceState(null, '', u.pathname + (u.search || ''));
    }
  }, []);

  // PC 端：首次进入且什么都没有（无设备；角色只有内置 musk 演示角色或为空）时，
  // 自动打开添加向导。ref 保证每次会话只自动弹一次——用户取消后手动刷新也不会再骚扰。
  const autoWiz = useRef(false);
  useEffect(() => {
    if (!data || autoWiz.current) return;
    const st = data.st;
    const noRoles = data.roles.length === 0 || (data.roles.length === 1 && data.roles[0].id === 'musk');
    if (st.pc_mode && st.machines === 0 && noRoles) {
      autoWiz.current = true;
      setWizOpen(true);
    }
  }, [data]);

  if (!data) return <p className="loading">{t('loading')}</p>;
  const { st, machines, roles, logs } = data;
  const boundRoles = roles.filter((r) => (r.bindings || []).some((b) => b.enabled === true && b.machine_id && b.slot > 0));
  const recent = logs.slice(0, 5);
  const upstreamReady = (data.upstreams || []).some((u) => u.enabled && (u.api_key_set || u.cookie_set || u.api === 'rss')); // M5：密钥改 *_set
  const unpaired = machines.filter((m) => !m.creds);

  return (
    <>
      <div className="page-head">
        <h2>{t('home')}</h2>
        <span className={'badge ' + (st.complete ? 'ok' : 'warn')}>
          {st.complete ? t('homeSetupDone') : t('homeSetupIncomplete')}
        </span>
        <span className="spacer"></span>
        <button onClick={() => setWizOpen(true)}>＋ {t('homeAdd')}</button>
      </div>

      {unpaired.length > 0 && (
        <div className="warn-banner">
          <span style={{ flex: 1 }}>⚠ {t('homeUnpairedWarn').replace('%s', unpaired.length)}</span>
          <button className="mini secondary" onClick={() => nav('/machines/' + encodeURIComponent(unpaired[0].id))}>
            {t('homeGoPair')}
          </button>
        </div>
      )}

      {!st.complete && (
        <div className="card hero-empty" onClick={() => setWizOpen(true)}>
          <div className="hero-art">🎛️</div>
          <h3>{t('homeEmptyTitle')}</h3>
          <p className="hint">{t('homeEmptyHint')}</p>
          <button style={{ fontSize: 15, padding: '9px 26px' }}>＋ {t('homeAdd')}</button>
        </div>
      )}

      <div className="hero-stats">
        <div className="hero-num" onClick={() => nav('/machines')}>
          <b className="num">{st.machines}</b>
          <span>{t('homeDevices')}</span>
          <span className="hint">{st.paired_machines} {t('homePaired')}</span>
        </div>
        <div className="hero-num" onClick={() => nav('/roles')}>
          <b className="num">{st.roles}</b>
          <span>{t('homeRoles')}</span>
          <span className="hint">{boundRoles.length} {t('homeBound')}</span>
        </div>
        <div className="hero-num">
          <b className="num">{st.bound_roles}</b>
          <span>{t('homeBindings')}</span>
          <span className="hint">{t('homeBindingsHint')}</span>
        </div>
        <span className="spacer" style={{ flex: 1 }}></span>
        <div className="home-readiness">
          <span className={'badge ' + (upstreamReady || st.data_source_ready || st.socialdata_ready ? 'ok' : 'warn')}
            style={{ cursor: 'pointer' }} title={t('homeGoSettings')} onClick={() => nav('/settings#feed/browser-login')}>
            🔑 {t('homeDataSource')}
          </span>
          <span className={'badge ' + (st.llm_ready ? 'ok' : 'mut')}
            style={{ cursor: 'pointer' }} title={t('homeGoSettings')} onClick={() => nav('/settings#advanced/llm-api')}>
            🧠 {t('llmTitle')}
          </span>
        </div>
      </div>

      <h3 className="home-h3">{t('homeQuickNav')}</h3>
      <div className="home-nav-grid">
        <NavTile icon="🖥️" title={t('machines')} hint={t('homeGoMachines')} n={st.machines} onClick={() => nav('/machines')} />
        <NavTile icon="🎭" title={t('roles')} hint={t('homeGoRoles')} n={st.roles} onClick={() => nav('/roles')} />
        <NavTile icon="⚙️" title={t('settings')} hint={t('homeGoSettings')} n={null} onClick={() => nav('/settings')} />
        <NavTile icon="📜" title={t('logs')} hint={t('homeGoLogs')} n={null} onClick={() => nav('/logs')} />
      </div>

      <div className="home-cols">
        <div>
          <h3 className="home-h3">{t('homeDevices')}</h3>
          <div className="card">
            {machines.length ? machines.map((m) => (
              <div key={m.id} className="account-row" onClick={() => nav('/machines/' + encodeURIComponent(m.id))}>
                <span className="row-logo">🖥️</span>
                <div className="account-main">
                  <div className="account-name">{m.name || m.addr}</div>
                  <div className="account-tags">
                    {m.creds ? <span className="badge ok">{t('pairedAt')}</span> : <span className="badge warn">{t('notPairedBold')}</span>}
                    <span className="badge mut mono">{m.addr}</span>
                  </div>
                </div>
                <span className="account-arrow">→</span>
              </div>
            )) : <p className="hint" style={{ padding: '8px 4px' }}>{t('empty')}</p>}
          </div>
        </div>

        <div>
          <h3 className="home-h3">{t('homeRoles')}</h3>
          <div className="card">
            {roles.length ? roles.map((r) => {
              // 原 slot 模式不上传 VRM / 不克隆声音：不显示「缺音色/缺形象」，直接标原 slot。
              const slotMode = r.voice_vrm_mode !== 'custom';
              const voice = !!(r.local_voice || (r.bindings || []).some((b) => b.voice_id));
              const vrm = !!r.local_vrm;
              return (
                <div key={r.id} className="account-row" onClick={() => nav('/roles/' + encodeURIComponent(r.id))}>
                  {r.icon
                    ? <img className="row-logo" src={`/api/roles/${encodeURIComponent(r.id)}/icon?v=${encodeURIComponent(r.updated_at || '')}`} alt="" style={{ objectFit: 'cover' }} />
                    : <span className="row-logo">🎭</span>}
                  <div className="account-main">
                    <div className="account-name">{r.name || r.id}</div>
                    <div className="account-tags">
                      <span className={'badge ' + (r.enabled ? 'ok' : 'warn')}>{r.enabled ? t('enabled') : t('disabled')}</span>
                      <span className="badge mut">{platformLabel(r.platform)}{r.username ? '/@' + r.username : ''}</span>
                      {slotMode
                        ? <span className="badge mut">🎛 {t('assetSourceSlotBadge')}</span>
                        : <>
                            <span className={'badge ' + (voice ? 'ok' : 'mut')}>🔊 {voice ? t('homeHasVoice') : t('homeNoVoice')}</span>
                            <span className={'badge ' + (vrm ? 'ok' : 'mut')}>🧍 {vrm ? t('homeHasVrm') : t('homeNoVrm')}</span>
                          </>}
                      {(r.bindings || []).slice(0, 2).map((b) => {
                        const bm = machines.find((m) => m.id === b.machine_id);
                        return <span key={b.machine_id + ':' + b.slot} className="badge mut">🖥 {bm ? (bm.name || bm.addr) : b.machine_id} · s{b.slot}</span>;
                      })}
                    </div>
                  </div>
                  <div className="home-role-actions" onClick={(e) => e.stopPropagation()}>
                    <SyncRoleButton role={r} machines={machines} onSynced={load} />
                  </div>
                  <span className="account-arrow">→</span>
                </div>
              );
            }) : <p className="hint" style={{ padding: '8px 4px' }}>{t('empty')}</p>}
          </div>
        </div>
      </div>

      <h3 className="home-h3">{t('homeRecent')}</h3>
      <div className="card">
        {recent.length ? recent.map((l, i) => (
          <div key={(l.time || '') + ':' + i} className="account-row" onClick={() => nav('/logs')}>
            <span className={'dot ' + (l.ok ? 'ok' : 'bad')}></span>
            <div className="account-main">
              <div className="account-name" style={{ fontSize: 13.5 }}>
                {l.text ? String(l.text).slice(0, 90) : (l.detail || l.error || l.kind || t('broadcastLog'))}
              </div>
              <div className="account-tags">
                <span className="badge mut">{l.kind === 'sync' ? t('kindSync') : l.kind === 'check' ? t('kindCheck') : t('kindBroadcast')}</span>
                {l.role_id ? <span className="badge mut">{roleName(roles, l.role_id)}</span> : null}
                {l.time ? <span className="hint num">{relTime(l.time)}</span> : null}
                {!l.ok && l.error ? <span className="badge bad">{l.error}</span> : null}
              </div>
            </div>
          </div>
        )) : <p className="hint" style={{ padding: '8px 4px' }}>{t('empty')}</p>}
      </div>

      <p className="hint" style={{ marginTop: 16 }}>{t('homeFooterHint')}</p>

      {wizOpen && (
        <SetupWizard
          machines={machines}
          roles={roles}
          onClose={() => { setWizOpen(false); load(); }}
          onDone={() => { setWizOpen(false); load(); }}
        />
      )}
    </>
  );
}

function NavTile({ icon, title, hint, n, onClick }) {
  return (
    <div className="home-nav-tile" onClick={onClick}>
      <span className="home-nav-icon">{icon}</span>
      <div style={{ flex: 1, minWidth: 0 }}>
        <div className="home-nav-title">{title}</div>
        <div className="hint">{hint}</div>
      </div>
      {n !== null && n !== '' ? <b className="home-nav-n">{n}</b> : null}
      <span className="account-arrow">→</span>
    </div>
  );
}

function platformLabel(p) {
  const v = (p || 'x').toLowerCase();
  if (v === 'weibo') return t('platformWeibo');
  if (v === 'xiaohongshu') return t('platformXhs');
  if (v === 'instagram') return t('platformInstagram');
  if (v === 'truth') return t('platformTruth');
  if (v === 'bluesky') return t('platformBluesky');
  if (v === 'bilibili') return t('platformBili');
  if (v === 'rss') return t('platformRss');
  if (v === 'twitter' || v === 'x') return t('platformX');
  return v;
}

function roleName(roles, id) {
  const r = (roles || []).find((x) => x.id === id);
  return r ? (r.name || r.id) : id;
}
