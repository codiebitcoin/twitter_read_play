// 设置页「内置浏览器登录」卡片：登录态状态机展示 + 登录/检测/退出操作，
// 以及 Web 模式下内置浏览器不可用时的远程浏览器配置卡（WebBrowserHint）。
// 从原先的单文件 SettingsPage.jsx 拆出，代码保持原样。
import { useEffect, useState } from 'react';
import { api } from '../../api.js';
import { t } from '../../i18n.js';
import { useToastErr, useToastOk } from '../../toast.jsx';

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

export function BrowserLoginBar({ domain }) {
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
  // 检测到登录后等 5 秒再自动关掉登录窗（后端 close 接口；Web 模式下是空操作）。
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
          setTimeout(() => {
            api(`/api/browser/session/${encodeURIComponent(p)}/close`, { method: 'POST' }).catch(() => {});
          }, 5000);
          load();
          return;
        }
        if (s.state === 'disconnected' || s.state === 'expired') {
          clearInterval(timer);
          if (!sawConnected) toastErr(`${t('browserNotConnected')} · ${p}`);
          load();
        }
      } catch { /* 登录页导航中，忽略 */ }
      if (tries > 60) clearInterval(timer);
    }, 3000);
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
