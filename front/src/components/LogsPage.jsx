import { useEffect, useMemo, useState } from 'react';
import { api, relTime } from '../api.js';
import { t } from '../i18n.js';
import { useToastErr } from '../toast.jsx';

export default function LogsPage() {
  const [logs, setLogs] = useState(null);
  const [machines, setMachines] = useState([]); // 机器 id→名称映射（失败行要能看出是哪台机器）
  const [roleFilter, setRoleFilter] = useState('');
  const [machineFilter, setMachineFilter] = useState('');
  const [statusFilter, setStatusFilter] = useState('');
  const [merge, setMerge] = useState(true);
  const toastErr = useToastErr();

  const load = async () => {
    try {
      setLogs(await api('/api/logs'));
      try { setMachines(await api('/api/machines')); } catch { setMachines([]); }
    }
    catch (e) { toastErr(e.message); }
  };
  useEffect(() => { load(); }, []);

  const machineName = useMemo(() => {
    const map = {};
    for (const m of machines) map[m.id] = m.name || m.id;
    return (id) => map[id] || id;
  }, [machines]);

  const roles = useMemo(
    () => (logs ? [...new Set(logs.map((l) => l.twitter).filter(Boolean))] : []),
    [logs]
  );
  const machineIDs = useMemo(
    () => (logs ? [...new Set(logs.map((l) => l.machine_id).filter(Boolean))] : []),
    [logs]
  );

  const rows = useMemo(() => {
    if (!logs) return [];
    const rf = roleFilter.replace('@', '');
    let rs = logs.filter((l) =>
      (!rf || l.twitter === rf)
      && (!machineFilter || l.machine_id === machineFilter)
      && (!statusFilter || (statusFilter === 'ok') === !!l.ok));
    if (merge) {
      // 按「错误信息 + 机器」聚合失败行（保留每组最新一条的位置）：同一条错误在不同机器上
      // 是不同问题（如某台设备锁屏、另一台掉线），混成一条 ×N 会看不出该去修哪台。成功行原样。
      const out = []; const idxByErr = {};
      for (const l of rs) {
        if (!l.ok && l.error) {
          const key = l.error + '|' + (l.machine_id || '');
          if (idxByErr[key] === undefined) { idxByErr[key] = out.length; out.push({ ...l, _count: 1 }); }
          else out[idxByErr[key]]._count++;
        } else out.push(l);
      }
      rs = out;
    }
    return rs;
  }, [logs, roleFilter, machineFilter, statusFilter, merge]);

  if (!logs) return <p className="loading">{t('loading')}</p>;

  const okN = logs.filter((l) => l.ok).length;
  const failN = logs.length - okN;
  const firstFail = logs.find((l) => !l.ok);

  return (
    <>
      <div className="page-head">
        <h2>{t('logsTitle')}</h2>
        <span className="hint">{t('broadcastLog')}</span>
        <span className="spacer"></span>
        <button className="mini secondary" onClick={load}>{t('refresh')}</button>
      </div>
      <div className="stats-bar">
        <span>{t('recent200')}</span>
        <span>✅ <b className="ok">{okN}</b> {t('ok_播报')}</span>
        <span>❌ <b className="bad">{failN}</b> {t('failed')}</span>
        {failN && firstFail ? <span className="hint">{t('firstFailAt')} {relTime(firstFail.time)}</span> : ''}
      </div>
      <div className="bar">
        <select value={roleFilter} onChange={(e) => setRoleFilter(e.target.value)}>
          <option value="">{t('allRoles')}</option>
          {roles.map((r) => <option key={r} value={'@' + r}>@{r}</option>)}
        </select>
        <select value={machineFilter} onChange={(e) => setMachineFilter(e.target.value)}>
          <option value="">{t('colMachine')}: -</option>
          {machineIDs.map((id) => <option key={id} value={id}>{machineName(id)}</option>)}
        </select>
        <select value={statusFilter} onChange={(e) => setStatusFilter(e.target.value)}>
          <option value="">{t('colResult')}: -</option>
          <option value="ok">{t('ok_播报')}</option>
          <option value="bad">{t('failed')}</option>
        </select>
        <label style={{ display: 'flex', alignItems: 'center', gap: 6, margin: 0, fontSize: 13 }}>
          <input type="checkbox" checked={merge} onChange={(e) => setMerge(e.target.checked)} />{t('mergeErrors')}
        </label>
      </div>
      <table>
        <thead><tr><th>{t('colTime')}</th><th>{t('colRole')}</th><th>{t('colMachine')}</th><th>{t('colContent')}</th><th>{t('colResult')}</th></tr></thead>
        <tbody>
          {rows.length ? rows.map((l, i) => (
            <tr key={i} className={l.ok ? '' : 'fail-row'}>
              <td title={String(l.time)} style={{ whiteSpace: 'nowrap' }}>{relTime(l.time)}</td>
              <td style={{ whiteSpace: 'nowrap' }}>@{l.twitter}</td>
              <td style={{ whiteSpace: 'nowrap' }} title={l.machine_id || ''}>
                {l.machine_id ? <span className="badge mut">🖥 {machineName(l.machine_id)}</span> : <span style={{ color: 'var(--mut)' }}>—</span>}
              </td>
              <td>
                <div className="td-text">
                  {l.ok ? l.text : <span style={{ color: 'var(--mut)' }}>{l.error || l.text}</span>}
                  {l._count > 1 ? <span className="badge bad"> ×{l._count}</span> : ''}
                </div>
              </td>
              <td>{l.ok ? <span className="badge ok">{t('ok_播报')}</span> : <span className="badge bad">{t('failed')}</span>}</td>
            </tr>
          )) : <tr><td colSpan="5"><p className="empty">{t('empty')}</p></td></tr>}
        </tbody>
      </table>
    </>
  );
}
