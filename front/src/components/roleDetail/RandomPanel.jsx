import { useState } from 'react';
import { api } from '../../api.js';
import { t } from '../../i18n.js';
import { useToastErr, useToastOk } from '../../toast.jsx';
import { putRole } from './shared.js';
import { notifyMessageQueued } from '../../messageQueue.js';
import PoolPicker from './PoolPicker.jsx';

// random 模式：按规则从缓存里随机播。
// 规则：必须含词 / 排除词 / 推文发布日期区间 / 点赞数≥N；「只看勾选」打开则只从勾选里挑。
// 与 broadcast 模式数据同源（真实推文），但配置与调度完全独立，可同时开。
export default function RandomPanel({ role, onSaved }) {
  const toastOk = useToastOk();
  const toastErr = useToastErr();
  const [f, setF] = useState({
    enabled: role.random_enabled ? '1' : '0',
    interval: role.random_interval || 0,
    require: role.random_require_words || '',
    exclude: role.random_exclude_words || '',
    from: role.random_from || '',
    to: role.random_to || '',
    minLikes: role.random_min_likes || 0,
    onlyChecked: role.random_only_checked ? '1' : '0',
    autoInterval: role.auto_random_interval || 0,
  });
  const [busy, setBusy] = useState(false);
  const set = (k) => (e) => setF({ ...f, [k]: e.target.value });

  const save = async () => {
    try {
      await putRole(role, {
        random_enabled: f.enabled === '1',
        random_interval: +f.interval || 0,
        random_require_words: f.require,
        random_exclude_words: f.exclude,
        random_from: f.from,
        random_to: f.to,
        random_min_likes: +f.minLikes || 0,
        random_only_checked: f.onlyChecked === '1',
        auto_random_interval: +f.autoInterval || 0,
      });
      toastOk(t('saved'));
      onSaved && onSaved();
    } catch (e) { toastErr(e.message); }
  };

  // 立即按规则挑一条入队（不等自动间隔）
  const playNow = async () => {
    setBusy(true);
    try {
      const res = await api(`/api/roles/${encodeURIComponent(role.id)}/random`, { method: 'POST' });
      toastOk(t('bqQueued').replace('%s', (res && res.queued) || 0));
      notifyMessageQueued();
    } catch (e) { toastErr(e.message); }
    setBusy(false);
  };

  return (
    <div className="card" style={{ maxWidth: 760 }}>
      <div className="form-grid">
        <label className="field">{t('modeEnabled')}
          <select value={f.enabled} onChange={set('enabled')}>
            <option value="1">{t('enabled')}</option>
            <option value="0">{t('disabled')}</option>
          </select>
          <span className="hint">{t('randomEnableHint')}</span>
        </label>
        <label className="field">{t('modeInterval')}
          <input type="number" min="0" value={f.interval} onChange={set('interval')} />
          <span className="hint">{t('modeIntervalHint')}</span>
        </label>
        <label className="field span-2">{t('randomRequire')}
          <textarea rows="2" value={f.require} onChange={set('require')} placeholder={t('randomRequirePh')} />
        </label>
        <label className="field span-2">{t('randomExclude')}
          <textarea rows="2" value={f.exclude} onChange={set('exclude')} placeholder={t('randomExcludePh')} />
        </label>
        <label className="field">{t('randomFrom')}
          <input type="date" value={f.from} onChange={set('from')} />
        </label>
        <label className="field">{t('randomTo')}
          <input type="date" value={f.to} onChange={set('to')} />
        </label>
        <label className="field">{t('randomMinLikes')}
          <input type="number" min="0" value={f.minLikes} onChange={set('minLikes')} />
          <span className="hint">{t('randomMinLikesHint')}</span>
        </label>
        <label className="field">{t('autoRandomInterval')}
          <input type="number" min="0" value={f.autoInterval} onChange={set('autoInterval')} />
          <span className="hint">{t('autoRandomIntervalHint')}</span>
        </label>
        <label className="field">{t('randomOnlyChecked')}
          <select value={f.onlyChecked} onChange={set('onlyChecked')}>
            <option value="0">{t('randomAllCached')}</option>
            <option value="1">{t('randomCheckedOnly')}</option>
          </select>
          <span className="hint">{t('randomOnlyCheckedHint')}</span>
        </label>
      </div>
      {f.onlyChecked === '1' && <PoolPicker role={role} pool="random" />}
      <p className="hint">ℹ️ {t('randomPoolHint')}</p>
      <div className="card-actions">
        <button onClick={save}>{t('save')}</button>
        <button className="secondary" disabled={busy} onClick={playNow}>{t('randomPlayNow')}</button>
      </div>
    </div>
  );
}
