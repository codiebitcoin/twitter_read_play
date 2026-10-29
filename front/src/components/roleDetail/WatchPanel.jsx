import { useEffect, useState } from 'react';
import { t } from '../../i18n.js';
import { useToastErr, useToastOk } from '../../toast.jsx';
import { api } from '../../api.js';
import { notifyMessageQueued } from '../../messageQueue.js';
import { putRole, normPlatform, platformLabel } from './shared.js';

// watch 模式：该角色唯一 Feed 源上的监听开关 / 间隔 / 关键字。
// 平台和上游在角色设置里配，这里不另选。
export default function WatchPanel({ role, onSaved, goTab }) {
  const toastOk = useToastOk();
  const toastErr = useToastErr();
  const [f, setF] = useState({
    enabled: role.enabled ? '1' : '0',
    interval: role.interval || 600,
    repeatCount: role.repeat_count || 1,
    repeatGap: role.repeat_gap || 30,
    filter_words: role.filter_words || '',
    require_words: role.require_words || '',
  });
  const [played, setPlayed] = useState(null);
  const set = (k) => (e) => setF({ ...f, [k]: e.target.value });
  const isRss = normPlatform(role.platform) === 'rss';
  const handle = (role.username || '').trim();
  const sourceLabel = handle
    ? (isRss ? `${platformLabel(role.platform)} · ${handle}` : `${platformLabel(role.platform)} · @${handle.replace(/^@/, '')}`)
    : platformLabel(role.platform);

  const loadPlayed = async () => {
    try {
      const all = await api(`/api/logs?role_id=${encodeURIComponent(role.id)}&kind=broadcast`);
      setPlayed((all || []).filter((l) => l.source === 'watch' || l.source === 'poll'));
    } catch { setPlayed([]); }
  };
  useEffect(() => { loadPlayed(); const iv = setInterval(loadPlayed, 10000); return () => clearInterval(iv); }, [role.id]);

  const save = async () => {
    try {
      await putRole(role, {
        enabled: f.enabled === '1',
        interval: +f.interval || 600,
        repeat_count: +f.repeatCount || 1,
        repeat_gap: +f.repeatGap || 0,
        filter_words: f.filter_words,
        require_words: f.require_words,
      });
      toastOk(t('saved'));
      onSaved && onSaved();
      if (f.enabled === '1') {
        try {
          await api(`/api/roles/${encodeURIComponent(role.id)}/poll`, { method: 'POST' });
          notifyMessageQueued();
        } catch (e) { toastErr(e.message); }
      }
    } catch (e) { toastErr(e.message); }
  };

  return (
    <>
      <div className="card" style={{ maxWidth: 760 }}>
        <div className="bound-source">
          <span>{sourceLabel}</span>
          <button className="mini ghost" type="button" onClick={() => goTab && goTab('settings', 'role-username')}>{t('feedSourceChange')}</button>
        </div>
        <div className="form-grid">
          <label className="field">{t('modeEnabled')}
            <select value={f.enabled} onChange={set('enabled')}>
              <option value="1">{t('enabled')}</option>
              <option value="0">{t('disabled')}</option>
            </select>
            <span className="hint">{t('watchEnableHint')}</span>
          </label>
          <label className="field">{t('pollInterval')}
            <input type="number" min="0" value={f.interval} onChange={set('interval')} />
          </label>
          <label className="field">{t('repeatCount')}
            <input type="number" min="1" max="10" value={f.repeatCount} onChange={set('repeatCount')} />
          </label>
          <label className="field">{t('repeatGap')}
            <input type="number" min="0" value={f.repeatGap} onChange={set('repeatGap')} />
          </label>
          <label className="field span-2">{t('filterWords')}
            <textarea rows="2" placeholder={t('filterWordsPh')} value={f.filter_words} onChange={set('filter_words')} />
          </label>
          <label className="field span-2">{t('requireWords')}
            <textarea rows="2" placeholder={t('requireWordsPh')} value={f.require_words} onChange={set('require_words')} />
          </label>
        </div>
        <p className="hint">ℹ️ {t('watchHint')}</p>
        <div className="card-actions">
          <button onClick={save}>{t('save')}</button>
        </div>
      </div>

      <div className="card" style={{ maxWidth: 760, marginTop: 12 }}>
        <b>{t('watchPlayedTitle')}</b>
        <span className="hint" style={{ marginLeft: 8 }}>{played ? played.length : '…'}</span>
        {!played ? <p className="loading">{t('loading')}</p>
          : played.length === 0 ? <p className="empty" style={{ marginTop: 8 }}>∅ {t('watchPlayedEmpty')}</p>
          : played.map((l, i) => (
            <div key={(l.tweet_id || '') + i} className="card tweet" style={{ marginTop: 8, border: '1px solid var(--line-soft)', borderRadius: 14 }}>
              <p style={{ margin: 0, whiteSpace: 'pre-wrap', wordBreak: 'break-word', fontSize: 14.5, lineHeight: 1.45 }}>{l.text}</p>
              <div className="hint" style={{ marginTop: 8, fontSize: 12, display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
                <span>{l.source === 'watch' ? t('watchPlayedWebhook') : t('watchPlayedPoll')}</span>
                <span>{new Date(l.time).toLocaleString()}</span>
                {l.machine_id && <span className="badge mut">@ {l.machine_id.slice(0, 12)}</span>}
                {l.ok ? <span className="badge ok">✓ {t('watchPlayedOk')}</span> : <span className="badge bad">✗ {l.error || t('watchPlayedFail')}</span>}
              </div>
            </div>
          ))}
      </div>
    </>
  );
}
