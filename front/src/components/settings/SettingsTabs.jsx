// 设置页的五个子页内容：账号、Feed、Live、高级、上游数据源。
// 从原先的单文件 SettingsPage.jsx 拆出，代码保持原样。
import { useEffect, useState } from 'react';
import { api } from '../../api.js';
import { t } from '../../i18n.js';
import { useToastErr, useToastOk } from '../../toast.jsx';
import { useNav } from '../../App.jsx';
import { SHOW_LIVE } from '../../features.js';
import { BrowserLoginBar } from './BrowserLogin.jsx';
import { ChannelsSection } from './Channels.jsx';

export function AccountTab({ st, onSaved }) {
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

export function FeedTab({ st, onSaved }) {
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

export function LiveTab() {
  return (
    <>
      <BrowserLoginBar domain="live" />
      <ChannelsSection domain="live" />
    </>
  );
}

export function AdvancedTab({ st, onSaved }) {
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

export function UpstreamsTab() {
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
