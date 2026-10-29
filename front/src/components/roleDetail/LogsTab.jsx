import { useEffect, useState } from 'react';
import { api } from '../../api.js';
import { t } from '../../i18n.js';

// ---- 系统日志：播报/同步/校验 ----
export default function LogsTab() {
  const [logs, setLogs] = useState(null);
  const [kind, setKind] = useState('');
  const load = async () => {
    try { setLogs(await api('/api/logs' + (kind ? '?kind=' + kind : ''))); }
    catch { setLogs([]); }
  };
  useEffect(() => { load(); }, [kind]);
  const kindBadge = (k) => {
    if (k === 'sync') return <span className="badge mut">{t('kindSync')}</span>;
    if (k === 'check') return <span className="badge mut">{t('kindCheck')}</span>;
    return <span className="badge mut">{t('kindBroadcast')}</span>;
  };
  return (
    <div className="card" style={{ maxWidth: 900 }}>
      <div className="bar" style={{ display: 'flex', gap: 8, alignItems: 'center', marginBottom: 8 }}>
        <select className="mini" value={kind} onChange={(e) => setKind(e.target.value)}>
          <option value="">{t('allRoles')}</option>
          <option value="sync">{t('kindSync')}</option>
          <option value="check">{t('kindCheck')}</option>
          <option value="broadcast">{t('kindBroadcast')}</option>
        </select>
        <button className="mini ghost" onClick={load}>{t('refresh')}</button>
      </div>
      {!logs ? <p className="loading">{t('loading')}</p>
        : !logs.length ? <p className="empty">{t('empty')}</p>
        : (
          <table>
            <thead><tr><th>{t('colTime')}</th><th>{t('logKind')}</th><th>{t('colRole')}</th><th>{t('colContent')}</th><th>{t('colResult')}</th></tr></thead>
            <tbody>
              {logs.map((l, i) => (
                <tr key={i}>
                  <td>{new Date(l.time).toLocaleString()}</td>
                  <td>{kindBadge(l.kind)}</td>
                  <td>{l.role_id || l.twitter || ''}{l.machine_id ? <span className="hint"> @ {l.machine_id}</span> : null}</td>
                  <td>{l.text || l.detail || ''}</td>
                  <td>{l.ok ? <span className="badge ok">✓</span> : <span className="badge bad" title={l.error || ''}>✗ {l.error || 'failed'}</span>}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
    </div>
  );
}
