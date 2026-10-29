import { useEffect, useState, useCallback } from 'react';
import { api, apiRaw, relTime } from '../api.js';
import { t } from '../i18n.js';
import { useToastErr } from '../toast.jsx';
import { useNav } from '../App.jsx';
import SetupWizard from './wizard/SetupWizard.jsx';

// 状态徽章 3 态探测（apiRaw，body.error 不视为异常）
function statusBadge(st, t) {
  if (!st) return { text: t('checking'), cls: 'badge mut' };
  if (st.error && st.connected) return { text: t('authIssue'), cls: 'badge warn' };
  if (st.connected) return { text: t('connected'), cls: 'badge ok' };
  return { text: st.expired ? t('expired') : t('offline'), cls: 'badge bad' };
}

export function statusTitle(st, provision_error, t) {
  if (!st) return '';
  return (st.error || '') + (st.error ? ' · ' : '') + (provision_error ? '内置资源下发失败：' + provision_error + ' · ' : '') + t('checkedAt') + ' ' + new Date().toLocaleTimeString();
}

export async function probeStatus(id) {
  try { return await apiRaw(`/api/machines/${id}/status`); }
  catch { return null; }
}

export default function MachinesPage() {
  const [machines, setMachines] = useState(null);
  const [roles, setRoles] = useState([]);
  const [statuses, setStatuses] = useState({});
  const [checkedAt, setCheckedAt] = useState({});
  const [addOpen, setAddOpen] = useState(false);
  const [wizOpen, setWizOpen] = useState(false);
  const [form, setForm] = useState({ name: '', addr: '', auth_scope: 'permanent' });
  const [scan, setScan] = useState(null);       // 局域网扫描结果
  const [scanning, setScanning] = useState(false);
  const toastErr = useToastErr();
  const nav = useNav();

  const load = useCallback(async () => {
    try {
      const [ms, rs] = await Promise.all([
        api('/api/machines'),
        api('/api/roles').catch(() => []),
      ]);
      setMachines(ms);
      setRoles(rs || []);
    }
    catch (e) { toastErr(e.message); }
  }, [toastErr]);

  useEffect(() => { load(); }, [load]);

  // 每台机器并发探测状态（只依赖 id 列表，machines 引用变化不再触发重复探测）
  const idKey = (machines || []).map((m) => m.id).join(',');
  useEffect(() => {
    if (!machines) return;
    for (const m of machines) {
      probeStatus(m.id).then((st) => {
        setStatuses((s) => ({ ...s, [m.id]: st }));
        setCheckedAt((s) => ({ ...s, [m.id]: Date.now() }));
      });
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [idKey]);

  const reprobe = async (m, ev) => {
    ev && ev.stopPropagation();
    setStatuses((s) => ({ ...s, [m.id]: null }));
    const st = await probeStatus(m.id);
    setStatuses((s) => ({ ...s, [m.id]: st }));
    setCheckedAt((s) => ({ ...s, [m.id]: Date.now() }));
  };

  // 扫描本机网段里监听设备端口的地址；用户点结果即可填入地址，不用再去路由器找 IP。
  const scanLan = async () => {
    setScanning(true);
    setScan(null);
    try {
      setScan(await api('/api/machines/discover'));
    } catch (e) {
      toastErr(e.message);
    } finally {
      setScanning(false);
    }
  };

  const addMachine = async () => {
    setAddOpen(false);
    try {
      await api('/api/machines', { method: 'POST', body: JSON.stringify({ name: form.name.trim(), addr: form.addr.trim(), auth_scope: form.auth_scope }) });
      load();
    } catch (e) { toastErr(e.message); }
  };

  if (!machines) return <p className="loading">{t('loading')}</p>;

  // Welcome 栏目统计：机器连接状态 + 角色启用/绑定状态
  const online = machines.filter((m) => statuses[m.id]?.connected).length;
  const probed = machines.filter((m) => statuses[m.id]).length;
  const enabledRoles = roles.filter((r) => r.enabled).length;
  const boundRoles = roles.filter((r) => (r.bindings || []).some((b) => b.enabled === true && b.machine_id && b.slot > 0));

  return (
    <>
      {/* Welcome：机器 / 角色 / 运行状态一览 + 弹窗向导入口 */}
      <div className="card welcome-card">
        <div className="welcome-head">
          <div>
            <h3>👋 {t('welcomeTitle')}</h3>
            <p className="hint">{t('welcomeSub')}</p>
          </div>
          <button onClick={() => setWizOpen(true)}>＋ {t('homeAdd')}</button>
        </div>
        <div className="welcome-stats">
          <div className="welcome-stat">
            <b className="num">{machines.length}</b>
            <span>{t('homeDevices')}</span>
            <span className="hint">{probed ? t('welcomeOnlineOf').replace('%o', online).replace('%n', probed) : t('checking')}</span>
          </div>
          <div className="welcome-stat">
            <b className="num">{roles.length}</b>
            <span>{t('homeRoles')}</span>
            <span className="hint">{t('welcomeRolesEnabled').replace('%n', enabledRoles)}</span>
          </div>
          <div className="welcome-stat">
            <b className="num">{boundRoles.length}</b>
            <span>{t('homeBindings')}</span>
            <span className="hint">{t('homeBindingsHint')}</span>
          </div>
          <span className="spacer" style={{ flex: 1 }}></span>
          <div className="welcome-status">
            <div className="welcome-status-title">{t('welcomeRunStatus')}</div>
            <div className="welcome-chips">
              {machines.length ? machines.map((m) => {
                const st = statuses[m.id];
                const b = statusBadge(st, t);
                return (
                  <span key={m.id} className={'badge ' + b.cls.split(' ')[1]}
                    title={statusTitle(st, st && st.provision_error, t)}>
                    🖥️ {m.name || m.addr} · {b.text}
                  </span>
                );
              }) : <span className="badge mut">{t('welcomeNoMachine')}</span>}
            </div>
          </div>
        </div>
      </div>

      <div className="actionbar">
        <button onClick={() => { setForm({ name: '', addr: '', auth_scope: 'permanent' }); setScan(null); setAddOpen(true); }}>＋ {t('addMachine')}</button>
      </div>
      {!machines.length ? (
        <p className="empty"><span className="icon">🖥️</span>{t('empty')}</p>
      ) : (
        <table>
          <thead><tr><th></th><th>{t('name')}</th><th>{t('address')}</th><th>{t('authScope')}</th><th>{t('statusCol')}</th><th></th></tr></thead>
          <tbody>
            {machines.map((m) => {
              const st = statuses[m.id];
              const b = statusBadge(st, t);
              return (
                <tr key={m.id} style={{ cursor: 'pointer' }} onClick={() => nav('/machines/' + encodeURIComponent(m.id))}>
                  <td style={{ fontSize: 18 }}>🖥️</td>
                  <td><b>{m.name || m.id}</b></td>
                  <td>{m.addr}</td>
                  <td><span className="role-tags">
                    <span className="badge mut">{m.auth_scope || '-'}</span>
                    {m.creds
                      ? <span className="badge mut">{t('pairedAt')} · {relTime(m.creds.paired_at)}</span>
                      : <span className="badge warn">{t('notPairedBold')}</span>}
                  </span></td>
                  <td><span className={b.cls} title={statusTitle(st, st && st.provision_error, t) + (checkedAt[m.id] ? ' · ' + new Date(checkedAt[m.id]).toLocaleTimeString() : '')}>{b.text}</span></td>
                  <td className="mc-arrow" style={{ color: 'var(--mut-2)', cursor: 'pointer' }} title={t('recheck')}
                      onClick={(ev) => reprobe(m, ev)}>→</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}

      {addOpen && (
        <dialog open style={{ position: 'fixed', inset: 0, margin: 'auto', maxWidth: 620 }}>
          <h3>{t('addMachine')}</h3>
          <label>{t('name')}<input value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder={t('machineNamePh')} /></label>
          <label>{t('address')}<input value={form.addr} onChange={(e) => setForm({ ...form, addr: e.target.value })} placeholder="192.168.1.10" /></label>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, margin: '-4px 0 10px' }}>
            <button className="mini secondary" onClick={scanLan} disabled={scanning}>
              {scanning ? t('wizScanning') : `📡 ${t('wizScanLan')}`}
            </button>
            {scan ? (
              <span className="hint">
                {t('wizLocalIP')}: {(scan.local_ips || []).join(', ') || '-'}
                {' · '}{t('wizScanned')}: {scan.scanned || 0}
              </span>
            ) : <span className="hint">{t('wizScanHint')}</span>}
          </div>
          {scan && (
            <div style={{ margin: '-2px 0 10px' }}>
              {(scan.devices || []).length ? (
                <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
                  {scan.devices.map((d) => (
                    <button key={d.ip} className="mini secondary"
                      title={d.rtt_ms ? `${d.rtt_ms} ms` : ''}
                      onClick={() => setForm((f) => ({ ...f, addr: d.addr, name: f.name || d.ip }))}>
                      {d.addr}
                    </button>
                  ))}
                </div>
              ) : <p className="hint" style={{ margin: 0 }}>🔍 {t('wizScanNone')}（{(scan.subnets || []).join(', ') || '-'}）</p>}
            </div>
          )}
          <label>授权类型
            <select value={form.auth_scope} onChange={(e) => setForm({ ...form, auth_scope: e.target.value })}>
              <option value="permanent">permanent（长期授权）</option>
              <option value="session">session（会话授权）</option>
            </select>
          </label>
          <div className="row">
            <button className="secondary" onClick={() => setAddOpen(false)}>{t('cancel')}</button>
            <button onClick={addMachine}>添加</button>
          </div>
        </dialog>
      )}

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
