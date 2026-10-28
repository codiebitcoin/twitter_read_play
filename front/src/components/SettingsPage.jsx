import { useEffect, useState } from 'react';
import { api } from '../api.js';
import { t } from '../i18n.js';
import { useToastErr, useToastOk } from '../toast.jsx';
import { useNav } from '../App.jsx';
import { locAdapter, locChannelName } from './common/adapters.js';
import { SHOW_LIVE } from '../features.js';
const TABS = ['account', 'feed', ...(SHOW_LIVE ? ['live'] : []), 'advanced', 'upstreams'];
const TAB_LABEL = { account: 'account', feed: 'feed', live: 'navLive', advanced: 'advanced', upstreams: 'upstreams' };

function AccountTab({ st, onSaved }) {
  const toastOk = useToastOk();
  const toastErr = useToastErr();
  const [user, setUser] = useState(st.auth_user || '');
  const [pass, setPass] = useState('');
  const [pass2, setPass2] = useState('');

  const save = async () => {
    if (pass && pass !== pass2) { toastErr(t('pwdMismatch')); return; }
    try {
      const body = { auth_user: user };
      if (pass) body.auth_pass = pass;
      await api('/api/settings', { method: 'PUT', body: JSON.stringify(body) });
      toastOk(t('saved'));
      onSaved && onSaved();
    } catch (e) { toastErr(e.message); }
  };

  return (
    <div className="card" style={{ maxWidth: 520 }}>
      <label className="field">
        {t('loginUser')} {st.auth_user
          ? <><span className="badge ok">✓ {st.auth_user}</span> <span className={'badge ' + (st.auth_user_from_db ? 'mut' : 'warn')}>{st.auth_user_from_db ? t('fromDB') : t('fromEnv')}</span></>
          : <span className="badge warn">{t('passUnset')}</span>}
        <input value={user} onChange={(e) => setUser(e.target.value)} placeholder={st.auth_user || 'admin'} />
      </label>
      <label className="field">
        {t('newPass')} {st.auth_pass_set
          ? <><span className="badge mut">••••••••</span> <span className={'badge ' + (st.auth_pass_from_db ? 'mut' : 'warn')}>{st.auth_pass_from_db ? t('fromDB') : t('fromEnv')}</span></>
          : <span className="badge warn">{t('passUnset')}</span>}
        <input type="password" autoComplete="new-password" value={pass} onChange={(e) => setPass(e.target.value)}
          placeholder={st.auth_pass_set ? '••••••••（' + t('passSet') + '）' : t('passUnset')} />
      </label>
      <label className="field">
        {t('newPass')} ×2
        <input type="password" autoComplete="new-password" value={pass2} onChange={(e) => setPass2(e.target.value)} placeholder={t('passUnset')} />
      </label>
      <div className="card-actions"><button onClick={save}>{t('save')}</button></div>
      <p className="hint">{t('dbPriorityHint')}；{t('saveHint')}</p>
    </div>
  );
}

// ---- 渠道（Feed / Live 共用）----

function credentialKeys(info) {
  const keys = new Set();
  Object.values(info?.needs || {}).forEach((arr) => (arr || []).forEach((k) => keys.add(k)));
  Object.values(info?.any_of || {}).forEach((groups) => (groups || []).forEach((g) => (g || []).forEach((k) => keys.add(k))));
  return keys;
}

function adapterSessionProvider(adapterID) {
  const m = String(adapterID || '').match(/^(.+)_browser$/);
  return m ? m[1] : '';
}

function ChannelDialog({ adapters, domain, initial, onClose, onSaved }) {
  const toastErr = useToastErr();
  const [f, setF] = useState(() => ({
    id: initial?.id || '',
    name: initial?.name || '',
    adapter_id: initial?.adapter_id || (adapters[0]?.id || ''),
    domain: initial?.domain || domain,
    enabled: initial?.enabled !== false,
    refresh_seconds: initial?.refresh_seconds || 600,
    credentials: {
      api_key: initial?.credentials?.api_key || '',
      base_url: initial?.credentials?.base_url || '',
      cookie: initial?.credentials?.cookie || '',
      oauth: initial?.credentials?.oauth || '',
      client_id: initial?.credentials?.client_id || '',
      client_secret: initial?.credentials?.client_secret || '',
      proxy: initial?.credentials?.proxy || '',
      session_provider: initial?.credentials?.session_provider || '',
    },
  }));
  const info = adapters.find((a) => a.id === f.adapter_id);
  const keys = credentialKeys(info);
  const isRemote = f.adapter_id === 'chrome_driverless';
  const sessionProvider = f.credentials.session_provider || adapterSessionProvider(f.adapter_id);
  const setC = (k, v) => setF((x) => ({ ...x, credentials: { ...x.credentials, [k]: v } }));

  const save = async () => {
    try {
      const body = {
        id: f.id,
        name: f.name || info?.name || f.adapter_id,
        domain: f.domain,
        adapter_id: f.adapter_id,
        enabled: !!f.enabled,
        refresh_seconds: +f.refresh_seconds || 600,
        credentials: {
          ...f.credentials,
          session_provider: sessionProvider,
        },
      };
      await api(f.id ? '/api/channels/' + encodeURIComponent(f.id) : '/api/channels', {
        method: f.id ? 'PUT' : 'POST', body: JSON.stringify(body),
      });
      onClose();
      onSaved();
    } catch (e) { toastErr(e.message); }
  };

  return (
    <dialog open style={{ position: 'fixed', inset: 0, margin: 'auto', maxWidth: 560 }}>
      <h3>{f.id ? t('channelEdit') : t('channelAdd')}</h3>
      <label className="field">{t('name')}<input value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} placeholder={info?.name || 'channel'} /></label>
      <label className="field">Adapter
        <select value={f.adapter_id} onChange={(e) => setF({ ...f, adapter_id: e.target.value })}>
          {adapters.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}
        </select>
      </label>
      {keys.has('api_key') ? <label className="field">API Key<input value={f.credentials.api_key} onChange={(e) => setC('api_key', e.target.value)} /></label> : null}
      {keys.has('base_url') ? <label className="field">Base URL<input value={f.credentials.base_url} onChange={(e) => setC('base_url', e.target.value)} placeholder={isRemote ? 'http://localhost:9222' : 'https://…'} /></label> : null}
      {keys.has('cookie') ? <label className="field">Cookie<input value={f.credentials.cookie} onChange={(e) => setC('cookie', e.target.value)} /></label> : null}
      {keys.has('oauth') ? <label className="field">OAuth Token<input value={f.credentials.oauth} onChange={(e) => setC('oauth', e.target.value)} /></label> : null}
      {keys.has('client_id') ? <label className="field">Client ID<input value={f.credentials.client_id} onChange={(e) => setC('client_id', e.target.value)} /></label> : null}
      {keys.has('client_id') ? <label className="field">Client Secret<input type="password" value={f.credentials.client_secret} onChange={(e) => setC('client_secret', e.target.value)} /></label> : null}
      {keys.has('desktop_session') ? (
        <label className="field">Session Provider
          <select value={sessionProvider} onChange={(e) => setC('session_provider', e.target.value)}>
            <option value="">{t('autoOption')}</option>
            {['x', 'weibo', 'xiaohongshu', 'instagram', 'truth', 'bluesky', 'youtube', 'bilibili', 'twitch'].map((p) => <option key={p} value={p}>{p}</option>)}
          </select>
        </label>
      ) : null}
      {isRemote ? <label className="field">Proxy<input value={f.credentials.proxy} onChange={(e) => setC('proxy', e.target.value)} placeholder="http://127.0.0.1:7890" /></label> : null}
      <label className="field">{t('pollInterval')}<input type="number" value={f.refresh_seconds} onChange={(e) => setF({ ...f, refresh_seconds: e.target.value })} /></label>
      <label style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 13, marginBottom: 4 }}>
        <input type="checkbox" checked={!!f.enabled} onChange={(e) => setF({ ...f, enabled: e.target.checked })} />{t('enabled')}
      </label>
      {info?.hint ? <div className="hint" style={{ margin: '8px 0 4px' }}>{info.hint}</div> : null}
      <div className="row">
        <button className="secondary" onClick={onClose}>{t('cancel')}</button>
        <button onClick={save}>{t('save')}</button>
      </div>
    </dialog>
  );
}

function ChannelCard({ ch, adapters, onChanged }) {
  const toastOk = useToastOk();
  const toastErr = useToastErr();
  const [edit, setEdit] = useState(false);
  const [busy, setBusy] = useState(false);

  const test = async () => {
    setBusy(true);
    try {
      const res = await api(`/api/channels/${encodeURIComponent(ch.id)}/test`, {
        method: 'POST', body: JSON.stringify({ domain: ch.domain }),
      });
      if (res.ok) {
        toastOk(`✓ ${res.probe?.detail || (res.probe?.count ? String(res.probe.count) : 'ok')}`);
      } else {
        toastErr(res.error || res.probe?.detail || 'failed');
      }
    } catch (e) { toastErr(e.message); }
    setBusy(false);
  };

  const toggle = async () => {
    try {
      await api(`/api/channels/${encodeURIComponent(ch.id)}/disable`, {
        method: 'POST', body: JSON.stringify({ enabled: !ch.enabled }),
      });
      onChanged();
    } catch (e) { toastErr(e.message); }
  };

  return (
    <>
      <div className="card" style={{ maxWidth: 760, marginTop: 12 }}>
        <div className="bound-source" style={{ justifyContent: 'space-between' }}>
          <div>
            <b>{locChannelName(ch)}</b>{' '}
            <span className="badge mut">{locAdapter(ch.adapter)?.name || ch.adapter_id}</span>{' '}
            <span className={'badge ' + (ch.enabled ? 'ok' : 'warn')}>{ch.enabled ? t('enabled') : t('disabled')}</span>{' '}
            <span className={'badge ' + (ch.ready ? 'ok' : 'warn')}>{ch.ready ? t('channelReady') : t('channelNotReady')}</span>
          </div>
          <div style={{ display: 'flex', gap: 6 }}>
            <button className="mini ghost" disabled={busy} onClick={test}>{busy ? '…' : t('test')}</button>
            <button className="mini ghost" onClick={() => setEdit(true)}>{t('edit')}</button>
            <button className="mini ghost" onClick={toggle}>{ch.enabled ? t('disable') : t('enable')}</button>
          </div>
        </div>
        <div className="hint" style={{ marginTop: 6 }}>
          {ch.adapter?.platforms?.join(' / ')}
          {ch.credentials?.base_url ? ` · ${ch.credentials.base_url}` : ''}
          {ch.credentials?.session_provider ? ` · session: ${ch.credentials.session_provider}` : ''}
          {ch.missing?.length ? ` · ${t('channelMissing')}: ${ch.missing.join(', ')}` : ''}
        </div>
        {ch.usage?.length ? (
          <div className="hint" style={{ marginTop: 4 }}>
            {t('channelUsedBy')}: {ch.usage.map((u) => u.role_name || u.role_id).join(', ')}
          </div>
        ) : null}
      </div>
      {edit ? <ChannelDialog adapters={adapters} domain={ch.domain} initial={ch} onClose={() => setEdit(false)} onSaved={onChanged} /> : null}
    </>
  );
}

// 登录态状态机：后端返回 state（unknown/connected/disconnected/expired）。
//
//   unknown      → 不展示状态文字，给「登录 + 检测」（还没判断过，两种都可能）
//   connected    → 「检测 / 重新登录 / 退出」（检测用于发现过期）
//   expired      → 「重新登录」，提示上次会话失效
//   disconnected → 只有「登录」（已经明确未登录，再检测没有意义）
function browserState(p) {
  if (p.state) return p.state;
  if (!p.known) return 'unknown';
  if (p.connected) return 'connected';
  return p.expired ? 'expired' : 'disconnected';
}

// 页面都加载不出来时（国内常见：Twitch/Truth 直连失败），把原因显式说出来，
// 否则卡片只有按钮、看起来像「没有登录状态」。
function browserErrorText(err) {
  if (!err) return '';
  if (/load failed|timeout|timed out|network|ECONN|not connected/i.test(err)) return t('browserNetHint');
  return err;
}

function BrowserLoginBar({ domain }) {
  const toastOk = useToastOk();
  const toastErr = useToastErr();
  const [st, setSt] = useState(null);
  const [busy, setBusy] = useState({});
  const [checkingAll, setCheckingAll] = useState(false);

  const load = () => api('/api/browser/session')
    .then(setSt)
    .catch(() => setSt({ available: false, providers: [] }));

  useEffect(() => { load(); }, []);

  if (!st) return null;
  if (!st.available) return <WebBrowserHint />;

  const allowed = domain === 'live'
    ? ['youtube', 'bilibili', 'twitch']
    : ['x', 'weibo', 'xiaohongshu', 'instagram', 'truth', 'bluesky'];
  const providers = (st.providers || []).filter((p) => allowed.includes(p.provider));

  // 登录窗关闭后后端会主动 Probe 一次；这里持续轮询，拿到明确结论就提示并刷新整条状态。
  // 未登录也要有反馈：否则用户关窗后页面看起来「什么都没发生」。
  const poll = (p) => {
    let tries = 0;
    let sawConnected = false;
    const timer = setInterval(async () => {
      tries++;
      try {
        const s = await api(`/api/browser/session/${encodeURIComponent(p)}`);
        if (s.connected) {
          sawConnected = true;
          clearInterval(timer);
          toastOk(`${t('browserConnected')} · ${s.account || p}`);
          load();
          return;
        }
        if (s.state === 'disconnected' || s.state === 'expired') {
          clearInterval(timer);
          if (!sawConnected) toastErr(`${t('browserNotConnected')} · ${p}`);
          load();
        }
      } catch { /* 登录页导航中，忽略 */ }
      if (tries > 120) clearInterval(timer);
    }, 1500);
  };

  const open = async (p) => {
    try {
      const res = await api(`/api/browser/session/${encodeURIComponent(p)}/open`, { method: 'POST' });
      if (res && res.remote) {
        // Web 模式：登录发生在远程浏览器的操作页。打开它并提醒用户去登录。
        const op = res.operation_url;
        if (op) window.open(op, '_blank', 'noopener');
        toastOk(t('browserRemoteHint'));
      } else {
        toastOk(t('browserOpenHint'));
      }
      poll(p);
    } catch (e) { toastErr(e.message); }
  };

  // silent=true 用于「检测全部」：不逐个弹 toast，最后统一刷新面板。
  const probe = async (p, silent) => {
    setBusy((b) => ({ ...b, [p]: true }));
    try {
      const s = await api(`/api/browser/session/${encodeURIComponent(p)}`);
      if (!silent) {
        if (s.connected) toastOk(`${t('browserConnected')} · ${s.account || p}`);
        else if (s.state === 'expired') toastErr(t('browserSessionExpired'));
        else if (s.state === 'disconnected') toastErr(s.error || t('browserNotConnected'));
        else toastErr(s.error || t('browserNotChecked'));
      }
      return s;
    } catch (e) {
      if (!silent) toastErr(e.message);
      return null;
    } finally {
      setBusy((b) => ({ ...b, [p]: false }));
    }
  };

  const probeOne = async (p) => { await probe(p, false); load(); };

  const probeAll = async () => {
    setCheckingAll(true);
    try {
      for (const p of providers) await probe(p.provider, true);
      await load();
    } finally { setCheckingAll(false); }
  };

  const logout = async (p) => {
    try {
      await api(`/api/browser/session/${encodeURIComponent(p)}/logout`, { method: 'POST' });
      load();
    } catch (e) { toastErr(e.message); }
  };

  return (
    <div className="card" style={{ maxWidth: 900 }} id={domain === 'live' ? 'browser-login-live' : 'browser-login'}>
      <div className="bound-source">
        <b>{t('browserLoginTitle')}</b>
        <div style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
          <button className="mini ghost" disabled={checkingAll} onClick={probeAll}>
            {checkingAll ? '…' : t('browserCheckAll')}
          </button>
          <span className="badge mut">{st.mode}</span>
        </div>
      </div>
      {/* Web 模式：登录发生在远程浏览器的操作页，给出入口和提醒。 */}
      {st.remote ? (
        <div className="hint" style={{ marginTop: 6, display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center' }}>
          {st.operation_url
            ? <a href={st.operation_url} target="_blank" rel="noopener" style={{ color: 'var(--acc)' }}>
                {t('webBrowserOperation')} ↗
              </a>
            : null}
          <span>{t('webBrowserOperationHint')}</span>
        </div>
      ) : null}
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8, marginTop: 8 }}>
        {providers.map((p) => {
          const state = browserState(p);
          const checked = state !== 'unknown';
          return (
            <div key={p.provider} className="card" style={{ padding: 10, minWidth: 200 }}>
              <div className="bound-source">
                <b>{p.provider}</b>
                {/* 未检测不显示状态文字，只留按钮 */}
                {checked ? (
                  <span className={'badge ' + (state === 'connected' ? 'ok' : 'warn')}>
                    {state === 'connected' ? t('browserConnected')
                      : state === 'expired' ? t('browserSessionExpired')
                        : t('browserNotConnected')}
                  </span>
                ) : null}
              </div>
              {checked ? (
                <div className="hint" style={{ margin: '4px 0' }}>
                  {p.account || ''}
                  {p.error ? ` · ${browserErrorText(p.error)}` : ''}
                  {p.last_check ? ` · ${t('browserCheckedAt')} ${new Date(p.last_check).toLocaleString()}` : ''}
                </div>
              ) : null}
              {/* 未检测但探测报了错（页面加载失败）：也要显示原因，否则卡片看起来没状态。 */}
              {!checked && p.error ? (
                <div className="hint" style={{ margin: '4px 0' }}>⚠ {browserErrorText(p.error)}</div>
              ) : null}
              {state === 'expired'
                ? <div className="hint" style={{ margin: '0 0 4px' }}>{t('browserExpiredHint')}</div>
                : null}
              {state === 'disconnected'
                ? <div className="hint" style={{ margin: '0 0 4px' }}>
                    {p.login_optional ? t('browserPublicHint') : t('browserNeedLogin')}
                  </div>
                : null}
              {p.provider === 'youtube' && state !== 'connected'
                ? <div className="hint" style={{ margin: '0 0 4px' }}>{t('browserPasswordHint')}</div>
                : null}
              <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
                {state === 'connected' ? (
                  <>
                    <button className="mini" disabled={!!busy[p.provider]} onClick={() => probeOne(p.provider)}>
                      {busy[p.provider] ? '…' : t('browserProbe')}
                    </button>
                    <button className="mini ghost" onClick={() => open(p.provider)}>{t('browserRelogin')}</button>
                    <button className="mini danger" onClick={() => logout(p.provider)}>{t('browserLogout')}</button>
                  </>
                ) : (
                  <>
                    <button className="mini" onClick={() => open(p.provider)}>
                      {state === 'expired' ? t('browserRelogin') : t('browserLogin')}
                    </button>
                    {/* 从未检测过才提供「检测」；明确未登录后收成一个「登录」按钮 */}
                    {state === 'unknown' ? (
                      <button className="mini ghost" disabled={!!busy[p.provider]} onClick={() => probeOne(p.provider)}>
                        {busy[p.provider] ? '…' : t('browserProbe')}
                      </button>
                    ) : null}
                  </>
                )}
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
}

function WebBrowserHint() {
  const toastOk = useToastOk();
  const toastErr = useToastErr();
  const [url, setUrl] = useState('');
  const [proxy, setProxy] = useState('');
  const [state, setState] = useState(null);

  const load = () => api('/api/browser/session').then(setState).catch(() => setState(null));

  useEffect(() => {
    api('/api/settings')
      .then((s) => {
        setUrl(s.chrome_driverless_url || '');
        setProxy(s.chrome_driverless_proxy || '');
      })
      .catch(() => {});
    load();
  }, []);

  const save = async () => {
    try {
      await api('/api/settings', {
        method: 'PUT',
        body: JSON.stringify({ chrome_driverless_url: url, chrome_driverless_proxy: proxy }),
      });
      toastOk(t('saved'));
      load();
    } catch (e) { toastErr(e.message); }
  };

  return (
    <div className="card" style={{ maxWidth: 760 }}>
      <div className="bound-source">
        <b>{t('webBrowserTitle')}</b>
        {state && state.operation_url
          ? <span className={'badge ' + (state.available ? 'ok' : 'warn')}>
              {state.available ? t('webBrowserReachable') : t('webBrowserUnreachable')}
            </span>
          : null}
      </div>
      <p className="hint">{t('webBrowserHint')}</p>
      <p className="hint">
        <a href="https://github.com/friddle/chrome-driverless" target="_blank" rel="noopener" style={{ color: 'var(--acc)' }}>
          {t('webBrowserInstall')} ↗
        </a>
        {state && state.operation_url ? (
          <> · <a href={state.operation_url} target="_blank" rel="noopener" style={{ color: 'var(--acc)' }}>
            {t('webBrowserOperation')} ↗
          </a></>
        ) : null}
      </p>
      <label className="field">{t('webBrowserUrl')}
        <input value={url} onChange={(e) => setUrl(e.target.value)} placeholder="http://localhost:9222" />
      </label>
      <label className="field">Proxy
        <input value={proxy} onChange={(e) => setProxy(e.target.value)} placeholder="http://127.0.0.1:7890" />
      </label>
      <div className="card-actions"><button onClick={save}>{t('save')}</button></div>
    </div>
  );
}

function ChannelsSection({ domain }) {
  const toastErr = useToastErr();
  const [data, setData] = useState(null);
  const [add, setAdd] = useState(false);

  const load = async () => {
    try {
      setData(await api('/api/channels?domain=' + encodeURIComponent(domain)));
    } catch (e) { toastErr(e.message); }
  };
  useEffect(() => { load(); }, [domain]);

  if (!data) return <p className="loading">{t('loading')}</p>;
  const adapters = (data.adapters || []).map(locAdapter);
  const channels = data.channels || [];

  return (
    <div className="card" style={{ maxWidth: 900 }}>
      <div className="bound-source">
        <b>{domain === 'live' ? t('navLive') : t('feed')}</b>
        <span className="badge mut">{data.mode}</span>
      </div>
      {channels.length === 0 ? <p className="hint">{t('channelEmpty')}</p> : null}
      {channels.map((ch) => <ChannelCard key={ch.id} ch={ch} adapters={adapters} onChanged={load} />)}
      <div className="card-actions" style={{ justifyContent: 'flex-start' }}>
        <button className="mini" onClick={() => setAdd(true)} disabled={adapters.length === 0}>＋ {t('channelAdd')}</button>
      </div>
      {add ? <ChannelDialog adapters={adapters} domain={domain} onClose={() => setAdd(false)} onSaved={load} /> : null}
    </div>
  );
}

function FeedTab({ st, onSaved }) {
  const toastOk = useToastOk();
  const toastErr = useToastErr();
  const [key, setKey] = useState(st.socialdata_api_key || '');
  const [base, setBase] = useState(st.socialdata_base_url || '');

  useEffect(() => {
    setKey(st.socialdata_api_key || '');
    setBase(st.socialdata_base_url || '');
  }, [st.socialdata_api_key, st.socialdata_base_url]);

  const saveSocialdata = async () => {
    try {
      await api('/api/settings', {
        method: 'PUT', body: JSON.stringify({ socialdata_api_key: key, socialdata_base_url: base }),
      });
      toastOk(t('saved'));
      onSaved && onSaved();
    } catch (e) { toastErr(e.message); }
  };

  return (
    <>
      <BrowserLoginBar domain="feed" />
      <div className="card" style={{ maxWidth: 760 }}>
        <div className="bound-source"><b>socialdata.tools</b><span className="badge mut">Feed</span></div>
        <label className="field" id="socialdata-key">{t('socialdataKey')}<input value={key} onChange={(e) => setKey(e.target.value)} /></label>
        <p className="hint" style={{ margin: '0 0 12px' }}>
          {t('apiKeyTip')} <a href="https://socialdata.tools/app/dashboard" target="_blank" rel="noopener" style={{ color: 'var(--acc)' }}>socialdata.tools/app/dashboard ↗</a>
        </p>
        <label className="field" id="socialdata-base">{t('socialdataBase')}
          <input value={base} onChange={(e) => setBase(e.target.value)} placeholder="https://api.socialdata.tools" />
        </label>
        <div className="card-actions"><button onClick={saveSocialdata}>{t('save')}</button></div>
      </div>
      <div id="feed-channels"><ChannelsSection domain="feed" /></div>
    </>
  );
}

function LiveTab() {
  return (
    <>
      <BrowserLoginBar domain="live" />
      <ChannelsSection domain="live" />
    </>
  );
}

function AdvancedTab({ st, onSaved }) {
  const toastOk = useToastOk();
  const toastErr = useToastErr();
  const [whBase, setWhBase] = useState(st.webhook_base || '');
  const [whSec, setWhSec] = useState('');
  const [freq, setFreq] = useState(300);
  const [mon, setMon] = useState(null);
  const [llm, setLlm] = useState({
    base_url: st.llm_base_url || '',
    api_key: st.llm_api_key || '',
    model: st.llm_model || '',
  });
  const [httpProxy, setHttpProxy] = useState(st.http_proxy || '');
  const [noHTTP, setNoHTTP] = useState(st.no_http || '');
  const [mode, setMode] = useState('c');

  useEffect(() => { api('/api/monitor/search').then(setMon).catch(() => {}); }, []);
  const reloadMon = () => api('/api/monitor/search').then(setMon).catch(() => {});

  const saveNet = async () => {
    try {
      await api('/api/settings', { method: 'PUT', body: JSON.stringify({ http_proxy: httpProxy, no_http: noHTTP }) });
      toastOk(t('saved'));
      onSaved && onSaved();
    } catch (e) { toastErr(e.message); }
  };

  const saveLlm = async () => {
    try {
      await api('/api/settings', { method: 'PUT', body: JSON.stringify({
        llm_base_url: llm.base_url, llm_api_key: llm.api_key, llm_model: llm.model,
      }) });
      toastOk(t('saved'));
      onSaved && onSaved();
    } catch (e) { toastErr(e.message); }
  };

  const saveWebhook = async () => {
    try {
      const body = { webhook_base: whBase };
      if (whSec) body.webhook_secret = whSec;
      await api('/api/settings', { method: 'PUT', body: JSON.stringify(body) });
      toastOk(t('saved'));
      onSaved && onSaved();
    } catch (e) { toastErr(e.message); }
  };

  const syncMon = async () => {
    try {
      await api('/api/monitor/search', { method: 'POST', body: JSON.stringify({ refresh_frequency: +freq }) });
      toastOk(t('monSync') + ' ✓'); reloadMon();
    } catch (e) { toastErr(e.message); }
  };
  const delMon = async () => {
    try { await api('/api/monitor/search', { method: 'DELETE' }); toastOk(t('monDel') + ' ✓'); reloadMon(); } catch (e) { toastErr(e.message); }
  };

  return (
    <>
      <div className="card" style={{ maxWidth: 520 }}>
        <b>{t('netTitle')}</b>
        <label className="field" style={{ marginTop: 8 }}>{t('httpProxy')}
          <input value={httpProxy} onChange={(e) => setHttpProxy(e.target.value)} placeholder={t('httpProxyPh')} />
        </label>
        <label className="field">{t('noHTTP')}
          <input value={noHTTP} onChange={(e) => setNoHTTP(e.target.value)} placeholder="1" />
        </label>
        <p className="hint">{t('noHTTPHint')}</p>
        <div className="card-actions"><button onClick={saveNet}>{t('save')}</button></div>
      </div>
      <div className="card" style={{ maxWidth: 520, marginTop: 16 }} id="llm-api">
        <b>{t('llmTitle')}</b>
        <label className="field" style={{ marginTop: 8 }}>{t('llmBaseUrl')}
          <input value={llm.base_url} onChange={(e) => setLlm({ ...llm, base_url: e.target.value })} placeholder="https://api.openai.com/v1" />
        </label>
        <label className="field">{t('llmApiKey')}
          <input type="password" autoComplete="off" value={llm.api_key} onChange={(e) => setLlm({ ...llm, api_key: e.target.value })} placeholder="sk-…" />
        </label>
        <label className="field">{t('llmModel')}
          <input value={llm.model} onChange={(e) => setLlm({ ...llm, model: e.target.value })} placeholder="gpt-4o-mini" />
        </label>
        <p className="hint">{t('llmHint')}</p>
        <div className="card-actions"><button onClick={saveLlm}>{t('save')}</button></div>
      </div>
      {!st.pc_mode && (
        <div className="card" style={{ maxWidth: 520, marginTop: 16 }} id="webhook">
          <b>{t('webhookTitle')}</b>
          <label className="field" style={{ marginTop: 8 }}>
            {t('monMode')}
            <select value={mode} onChange={(e) => setMode(e.target.value)}>
              <option value="c">C · {t('monModeC')}</option>
              <option value="b">B · {t('monModeB')}</option>
            </select>
          </label>
          <p className="hint">{mode === 'c' ? t('monPriceC') : t('monPriceB')}</p>
          {mode === 'c' ? (
            <>
              <label className="field">{t('webhookBase')}<input value={whBase} onChange={(e) => setWhBase(e.target.value)} placeholder="https://…" /></label>
              <label className="field">
                {t('webhookSecret')} {st.webhook_secret_set ? <span className="badge ok">✓</span> : <span className="badge warn">{t('passUnset')}</span>}
                <input type="password" value={whSec} onChange={(e) => setWhSec(e.target.value)} placeholder={st.webhook_secret_set ? '••••••••' : t('webhookSecretHint')} />
              </label>
              <p className="hint">{t('webhookHintC')}</p>
              <div className="card-actions" style={{ justifyContent: 'flex-start', flexWrap: 'wrap', gap: 8, alignItems: 'center' }}>
                <select value={freq} onChange={(e) => setFreq(+e.target.value)}>
                  <option value={60}>60s</option><option value={300}>300s</option><option value={600}>600s</option><option value={1800}>1800s</option>
                </select>
                <button className="mini" onClick={syncMon}>{t('monSync')}</button>
                <button className="mini danger" onClick={delMon}>{t('monDel')}</button>
                {mon ? (
                  <>
                    <span className={'badge ' + (mon.in_sync ? 'ok' : 'warn')}>{mon.in_sync ? '✓ ' + t('monInSync') : t('monStale')}</span>
                    <span className="badge mut">{mon.query || ''}</span>
                  </>
                ) : null}
              </div>
              <div className="card-actions"><button onClick={saveWebhook}>{t('save')}</button></div>
            </>
          ) : (
            <p className="hint">{t('webhookHintB')}</p>
          )}
        </div>
      )}
    </>
  );
}

function UpstreamsTab() {
  const nav = useNav();
  const toastErr = useToastErr();
  const [rows, setRows] = useState(null);

  useEffect(() => {
    api('/api/source/bindings').then(setRows).catch((e) => toastErr(e.message));
  }, []);

  if (!rows) return <p className="loading">{t('loading')}</p>;

  return (
    <div className="card" style={{ maxWidth: 1100 }}>
      <p className="hint">{t('upstreamsReadonlyHint')}</p>
      <table>
        <thead>
          <tr>
            <th>{t('role')}</th><th>{t('domain')}</th><th>{t('target')}</th>
            <th>Channel</th><th>Adapter</th><th>{t('statusCol')}</th><th></th>
          </tr>
        </thead>
        <tbody>
          {rows.length === 0 ? <tr><td colSpan="7" className="hint">{t('empty')}</td></tr> : rows.map((r, i) => (
            <tr key={(r.role_id || '') + i + (r.domain || '')}>
              <td><b>{r.role_name || r.role_id}</b></td>
              <td>{r.domain}</td>
              <td>{r.target || r.channel_id || '-'}</td>
              <td>{r.channel_id || '-'}</td>
              <td>{r.adapter_id || '-'}</td>
              <td>
                {r.ready ? <span className="badge ok">{t('channelReady')}</span> : <span className="badge warn">{r.legacy ? 'legacy' : t('channelNotReady')}</span>}
                {r.warnings?.length ? <div className="hint">{r.warnings[0]}</div> : null}
              </td>
              <td>
                <button className="mini ghost" onClick={() => nav(`/roles/${encodeURIComponent(r.role_id)}?tab=${SHOW_LIVE && r.domain === 'live' ? 'stream' : 'settings'}`)}>
                  {t('edit')}
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

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
