// 设置页「上游数据源 / 渠道」区块：渠道卡、编辑弹窗与按 domain 的列表容器。
// 从原先的单文件 SettingsPage.jsx 拆出，代码保持原样。
import { useEffect, useState } from 'react';
import { api } from '../../api.js';
import { t } from '../../i18n.js';
import { useToastErr, useToastOk } from '../../toast.jsx';
import { locAdapter, locChannelName } from '../common/adapters.js';

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
  // M5：密钥字段不再回传明文，只有 *_set —— 输入框留空 = 保留已存值（服务端合并），
  // 填新值覆盖。badge 提示该渠道哪些密钥已设置。
  const credSet = (k) => !!initial?.credentials?.[k + '_set'];
  const [f, setF] = useState(() => ({
    id: initial?.id || '',
    name: initial?.name || '',
    adapter_id: initial?.adapter_id || (adapters[0]?.id || ''),
    domain: initial?.domain || domain,
    enabled: initial?.enabled !== false,
    refresh_seconds: initial?.refresh_seconds || 600,
    credentials: {
      api_key: '',
      base_url: initial?.credentials?.base_url || '',
      cookie: '',
      oauth: '',
      client_id: initial?.credentials?.client_id || '',
      client_secret: '',
      proxy: initial?.credentials?.proxy || '',
      session_provider: initial?.credentials?.session_provider || '',
    },
  }));
  const info = adapters.find((a) => a.id === f.adapter_id);
  const keys = credentialKeys(info);
  const isRemote = f.adapter_id === 'chrome_driverless';
  const sessionProvider = f.credentials.session_provider || adapterSessionProvider(f.adapter_id);
  const setC = (k, v) => setF((x) => ({ ...x, credentials: { ...x.credentials, [k]: v } }));
  const secretField = (k, label) => (
    <label className="field">{label} {credSet(k) && <span className="badge ok">✓</span>}
      <input type="password" autoComplete="off" value={f.credentials[k]} onChange={(e) => setC(k, e.target.value)}
        placeholder={credSet(k) ? '••••••••（' + t('passSet') + '）' : ''} />
    </label>
  );

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
      {keys.has('api_key') ? secretField('api_key', 'API Key') : null}
      {keys.has('base_url') ? <label className="field">Base URL<input value={f.credentials.base_url} onChange={(e) => setC('base_url', e.target.value)} placeholder={isRemote ? 'http://localhost:9222' : 'https://…'} /></label> : null}
      {keys.has('cookie') ? secretField('cookie', 'Cookie') : null}
      {keys.has('oauth') ? secretField('oauth', 'OAuth Token') : null}
      {keys.has('client_id') ? <label className="field">Client ID<input value={f.credentials.client_id} onChange={(e) => setC('client_id', e.target.value)} /></label> : null}
      {keys.has('client_id') ? secretField('client_secret', 'Client Secret') : null}
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
      {f.id ? <div className="hint" style={{ margin: '4px 0 4px' }}>{t('secretKeepHint')}</div> : null}
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

export function ChannelsSection({ domain }) {
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
