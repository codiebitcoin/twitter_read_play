import { useEffect, useState } from 'react';
import { api } from '../../api.js';
import { t } from '../../i18n.js';
import { useToastErr, useToastOk } from '../../toast.jsx';
import { putRole, jumpForKey } from './shared.js';
import { notifyMessageQueued } from '../../messageQueue.js';
import PoolPicker from './PoolPicker.jsx';

// parody 模式：用 Settings 里配置的 OpenAI 兼容接口按 prompt 生成仿写推文。
// 生成结果存在独立列表（source=parody），与真实推文完全分开；可勾选旧推文作背景。
export default function ParodyPanel({ role, onSaved }) {
  const toastOk = useToastOk();
  const toastErr = useToastErr();
  const [items, setItems] = useState(null);
  const [busy, setBusy] = useState('');
  const [f, setF] = useState({
    enabled: role.parody_enabled ? '1' : '0',
    interval: role.parody_interval || 0,
    prompt: role.parody_prompt || '',
    contextN: role.parody_context_n || 0, // 旧字段：不再暴露 UI，但原值要原样写回（后端仍兼容）
    background: role.parody_background ? '1' : '0',
  });
  const set = (k) => (e) => setF({ ...f, [k]: e.target.value });

  const load = async () => {
    try {
      const res = await api(`/api/roles/${encodeURIComponent(role.id)}/parody`);
      setItems((res && res.items) || []);
    } catch (e) { toastErr(e.message); setItems([]); }
  };
  useEffect(() => { load(); }, [role.id]);

  const save = async () => {
    try {
      await putRole(role, {
        parody_enabled: f.enabled === '1',
        parody_interval: +f.interval || 0,
        parody_prompt: f.prompt,
        parody_context_n: +f.contextN || 0,
        parody_background: f.background === '1',
      });
      toastOk(t('saved'));
      onSaved && onSaved();
    } catch (e) { toastErr(e.message); }
  };

  const generate = async (broadcast) => {
    setBusy(broadcast ? 'gen-play' : 'gen');
    try {
      const res = await api(`/api/roles/${encodeURIComponent(role.id)}/parody/generate${broadcast ? '?broadcast=1' : ''}`, { method: 'POST' });
      toastOk(broadcast ? t('bqQueued').replace('%s', (res && res.queued) || 0) : t('parodyGenerated'));
      if (broadcast) notifyMessageQueued();
      await load();
    } catch (e) { if (!jumpForKey(toastErr, e.message, role)) toastErr(e.message); }
    setBusy('');
  };

  const play = async (tw) => {
    try {
      const res = await api(`/api/roles/${encodeURIComponent(role.id)}/broadcast`, { method: 'POST', body: JSON.stringify({ tweet_id: tw.id }) });
      toastOk(t('bqQueued').replace('%s', (res && res.queued) || 0));
      notifyMessageQueued();
    } catch (e) { toastErr(e.message); }
  };

  const del = async (tw) => {
    if (!confirm(t('parodyDeleteConfirm'))) return;
    try {
      await api(`/api/roles/${encodeURIComponent(role.id)}/parody/${encodeURIComponent(tw.id)}`, { method: 'DELETE' });
      setItems((ts) => ts.filter((x) => x.id !== tw.id));
    } catch (e) { toastErr(e.message); }
  };

  return (
    <>
      <div className="card" style={{ maxWidth: 760 }}>
        <div className="form-grid">
          <label className="field">{t('modeEnabled')}
            <select value={f.enabled} onChange={set('enabled')}>
              <option value="1">{t('enabled')}</option>
              <option value="0">{t('disabled')}</option>
            </select>
            <span className="hint">{t('parodyEnableHint')}</span>
          </label>
          <label className="field">{t('modeInterval')}
            <input type="number" min="0" value={f.interval} onChange={set('interval')} />
            <span className="hint">{t('modeIntervalHint')}</span>
          </label>
          <label className="field">{t('parodyBackground')}
            <select value={f.background} onChange={set('background')}>
              <option value="0">{t('parodyBackgroundOff')}</option>
              <option value="1">{t('parodyBackgroundOn')}</option>
            </select>
            <span className="hint">{t('parodyBackgroundHint')}</span>
          </label>
          <label className="field span-2">{t('parodyPrompt')}
            <textarea rows="5" value={f.prompt} onChange={set('prompt')} placeholder={t('parodyPromptPh')} />
            <span className="hint">{t('parodyPromptHint')}</span>
          </label>
        </div>
        <p className="hint">ℹ️ {t('parodyApiHint')}</p>
        <div className="card-actions">
          <button onClick={save}>{t('save')}</button>
          <button className="secondary" disabled={!!busy} onClick={() => generate(false)}>{busy === 'gen' ? t('loading') : t('parodyGenerate')}</button>
          <button className="secondary" disabled={!!busy} onClick={() => generate(true)}>{busy === 'gen-play' ? t('loading') : t('parodyGenerateAndPlay')}</button>
        </div>
      </div>

      {/* 背景推文池：选择界面就在 parody 自己的 tab 里（不放在 broadcast 列表） */}
      {f.background === '1' && <PoolPicker role={role} pool="parody" />}

      <div className="card" style={{ maxWidth: 760, marginTop: 12 }}>
        <b>{t('parodyList')}</b>
        <span className="hint" style={{ marginLeft: 8 }}>{items ? items.length : '…'}</span>
        {!items ? <p className="loading">{t('loading')}</p>
          : items.length === 0 ? <p className="empty"><span className="icon">🎭</span>{t('parodyEmpty')}</p>
          : items.map((tw) => (
            <div key={tw.id} className="card tweet" style={{ marginTop: 8, border: '1px solid var(--line-soft)', borderRadius: 14 }}>
              <p style={{ margin: 0, whiteSpace: 'pre-wrap', wordBreak: 'break-word', fontSize: 14.5, lineHeight: 1.45 }}>{tw.text}</p>
              <div className="hint" style={{ marginTop: 8, fontSize: 12, display: 'flex', gap: 8, alignItems: 'center' }}>
                <span>🎭 {tw.created_at ? new Date(tw.created_at).toLocaleString() : ''}</span>
                <span style={{ flex: 1 }} />
                <button className="mini" onClick={() => play(tw)}>{t('broadcastThis')}</button>
                <button className="mini ghost danger" onClick={() => del(tw)}>{t('delete')}</button>
              </div>
            </div>
          ))}
      </div>
    </>
  );
}
