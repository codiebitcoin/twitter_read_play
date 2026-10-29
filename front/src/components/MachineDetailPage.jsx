import { useEffect, useState, useCallback } from 'react';
import { api, relTime } from '../api.js';
import { t } from '../i18n.js';
import { useToastErr } from '../toast.jsx';
import { useNav } from '../App.jsx';
import { putRole } from './roleDetail/shared.js';
import { probeStatus, statusTitle } from './MachinesPage.jsx';
import MacLanTip, { isLanFailure } from './MacLanTip.jsx';

function expiryParts(mc, t) {
  if (!mc.creds) return { text: t('notPairedBold') };
  if (mc.auth_scope !== 'session' || !mc.creds.local_session_expires_at_ms) return { text: '—' };
  const ms = mc.creds.local_session_expires_at_ms - Date.now();
  if (ms <= 0) return { badge: 'bad', text: t('expired') };
  const mins = Math.floor(ms / 60000);
  const left = mins >= 60 ? Math.floor(mins / 60) + ' h ' + (mins % 60) + ' min' : mins + ' min';
  return { text: `${t('expiresIn')} ${left}` };
}

function PairDialog({ machineId, onClose }) {
  const [status, setStatus] = useState(t('pairRequesting'));
  const [code, setCode] = useState('');
  const [lanErr, setLanErr] = useState('');
  const toastErr = useToastErr();

  useEffect(() => {
    let dead = false;
    (async () => {
      try {
        await api(`/api/machines/${machineId}/pair/start`, { method: 'POST' });
        if (!dead) setStatus(t('pairEnterCode'));
      } catch (e) {
        if (dead) return;
        setStatus(t('pairStartFailed') + e.message);
        if (isLanFailure(e)) setLanErr(e.message);
      }
    })();
    return () => { dead = true; api(`/api/machines/${machineId}/pair/cancel`, { method: 'POST' }).catch(() => {}); };
  }, [machineId]);

  const confirm = async () => {
    try {
      await api(`/api/machines/${machineId}/pair/confirm`, { method: 'POST', body: JSON.stringify({ code }) });
      onClose(true);
    } catch (e) {
      setStatus(t('pairFailed') + e.message);
      if (isLanFailure(e)) setLanErr(e.message);
    }
  };

  return (
    <dialog open style={{ position: 'fixed', inset: 0, margin: 'auto' }}>
      <h3>{t('pairDeviceTitle')}</h3>
      {/* 只在连不上对端时提示「本地网络」授权：配对码错/鉴权失败与系统权限无关 */}
      <MacLanTip show={!!lanErr} />
      <p id="pair-status">{status}</p>
      <input id="pair-code" maxLength={6} value={code} onChange={(e) => setCode(e.target.value)} placeholder={t('pairCodePlaceholder')} />
      <div className="row">
        <button onClick={confirm}>{t('pairConfirm')}</button>
        <button className="secondary" onClick={() => onClose(false)}>{t('cancel')}</button>
      </div>
    </dialog>
  );
}

export default function MachineDetailPage({ id }) {
  const [mc, setMc] = useState(null);
  const [roles, setRoles] = useState([]);
  const [st, setSt] = useState(null);
  const [errText, setErrText] = useState('');
  const [pairOpen, setPairOpen] = useState(false);
  const [checkedTick, setCheckedTick] = useState(0);
  const toastErr = useToastErr();
  const nav = useNav();

  useEffect(() => {
    let dead = false;
    (async () => {
      try {
        const [ms, rs] = await Promise.all([api('/api/machines'), api('/api/roles')]);
        if (dead) return;
        const found = ms.find((x) => x.id === id);
        setMc(found || false);
        setRoles(rs);
      } catch (e) { if (!dead) toastErr(e.message); }
    })();
    return () => { dead = true; };
  }, [id]);

  // reloadMachine 重新拉机器本体（配对成功后 creds/paired_at 变了，必须重拉，
  // 否则界面上一直显示「未配对」——之前只 probe() 了在线状态，没刷机器数据）。
  const reloadMachine = useCallback(async () => {
    try {
      const ms = await api('/api/machines');
      const found = ms.find((x) => x.id === id);
      if (found) setMc(found);
    } catch (e) { toastErr(e.message); }
  }, [id, toastErr]);

  const probe = useCallback(async () => {
    setSt(null); setErrText('');
    const s = await probeStatus(id);
    setSt(s);
    setErrText(s && s.error ? (t('statusErrPrefix') + s.error) : '');
    if (s && s.provision_error) setErrText((prev) => (prev ? prev + ' · ' : '') + '内置资源下发失败：' + s.provision_error);
    setCheckedTick(Date.now());
  }, [id]);

  // loadRoles 勾/去勾后重拉角色列表：绑定（bindings）变化必须反映到本页勾选态
  const loadRoles = useCallback(async () => {
    try { setRoles(await api('/api/roles')); } catch (e) { toastErr(e.message); }
  }, [toastErr]);

  useEffect(() => { probe(); }, [probe]);

  if (mc === null) return <p className="loading">{t('loading')}</p>;
  if (mc === false) return <p className="err-banner">machine {id} not found</p>;

  const exp = expiryParts(mc, t);
  const badge = !st ? { text: t('checking'), cls: 'badge mut' }
    : st.error && st.connected ? { text: t('authIssue'), cls: 'badge warn' }
    : st.connected ? { text: t('connected'), cls: 'badge ok' }
    : { text: st.expired ? t('expired') : t('offline'), cls: 'badge bad' };

  // toggleRole 以「真实绑定」为准（旧版勾的是 enabled_roles 白名单，白名单为空=全部启用，
  // 导致本页新角色也显示为已勾选，与真实绑定对不上）：
  //   勾选   = 本机已有停用绑定就原地启用（保留 voice 记录）；没有绑定就自动挑一个空闲槽位新建；
  //   取消勾 = 只停用本机绑定（不清空，voice 记录可恢复），不删绑定。
  // 旧白名单非空的机器上顺手把名单对齐，避免运行时 effectivelyEnabled 仍被白名单挡住/放行。
  const toggleRole = async (r, on) => {
    const mine = (r.bindings || []).filter((b) => b.machine_id === id);
    try {
      if (on) {
        if (mine.some((b) => b.enabled)) return;
        const disabled = mine.filter((b) => !b.enabled);
        let next;
        if (disabled.length) {
          next = (r.bindings || []).map((b) => (b.machine_id === id ? { ...b, enabled: true } : b));
        } else {
          const taken = new Set(roles.flatMap((x) => (x.bindings || [])
            .filter((b) => b.machine_id === id && b.slot > 0).map((b) => b.slot)));
          const free = [1, 2, 3, 4, 5].find((s) => !taken.has(s));
          if (!free) throw new Error(t('machineNoFreeSlot'));
          next = [...(r.bindings || []), { machine_id: id, slot: free, enabled: true, voice_id: '', group_key: '' }];
        }
        await putRole(r, { bindings: next });
      } else {
        if (!mine.some((b) => b.enabled)) return;
        const next = (r.bindings || []).map((b) => (b.machine_id === id ? { ...b, enabled: false } : b));
        await putRole(r, { bindings: next });
      }
      if (mc && (mc.enabled_roles || []).length) {
        const inList = mc.enabled_roles.includes(r.id);
        if ((on && !inList) || (!on && inList)) {
          await api(`/api/machines/${id}/roles`, { method: 'POST', body: JSON.stringify({ role_id: r.id, on }) });
          reloadMachine();
        }
      }
      loadRoles();
    } catch (e) { toastErr(e.message); }
  };

  const del = async () => {
    if (!confirm(t('deleteConfirm'))) return;
    try { await api('/api/machines/' + id, { method: 'DELETE' }); nav('/machines'); }
    catch (e) { toastErr(e.message); }
  };

  return (
    <>
      <div className="actionbar">
        <button className="mini secondary" onClick={() => nav('/machines')}>← {t('back')}</button>
        <h2>{mc.name || mc.id}</h2>
        <span className="spacer"></span>
        <button className="mini" onClick={() => setPairOpen(true)}>{mc.creds ? t('repair') : t('pair')}</button>
        <button className="mini danger" onClick={del}>{t('del')}</button>
      </div>
      <div className="card" style={{ maxWidth: 720, marginBottom: 16 }}>
        <p className="hint" style={{ margin: '0 0 12px' }}>{t('pairedVsOnline')}</p>
        <dl className="kv-grid">
          <div><dt>{t('address')}</dt><dd>{mc.addr}</dd></div>
          <div><dt>{t('authScope')}</dt><dd>{mc.auth_scope || '-'}</dd></div>
          <div><dt>{t('pairedAt')}</dt><dd>{mc.creds ? relTime(mc.creds.paired_at) : t('notPairedBold')}</dd></div>
          <div><dt>{t('sessionExpiry')}</dt><dd>{exp.badge ? <span className={'badge ' + exp.badge}>{exp.text}</span> : exp.text}</dd></div>
          <div><dt>{t('statusCol')}</dt><dd><span className={badge.cls} title={statusTitle(st, st && st.provision_error, t) + (checkedTick ? ' · ' + new Date(checkedTick).toLocaleTimeString() : '')}>{badge.text}</span></dd></div>
        </dl>
        {errText ? <p className="hint" style={{ margin: '10px 0 0', whiteSpace: 'pre-wrap' }}>{errText}</p> : null}
      </div>
      <h3 style={{ fontSize: 14, margin: '0 0 8px' }}>{t('rolesOnMachine')}</h3>
      <p className="hint" style={{ margin: '0 0 8px', maxWidth: 720 }}>{t('machineBindHint')}</p>
      <div className="card" style={{ maxWidth: 720 }}>
        {roles.length ? roles.map((r) => {
          const mine = (r.bindings || []).filter((b) => b.machine_id === id);
          const bound = mine.some((b) => b.enabled);
          return (
            <div key={r.id} className="md-role-row" style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '8px 0', borderBottom: '1px solid var(--line-soft)' }}>
              <input
                type="checkbox"
                checked={bound}
                onChange={(ev) => toggleRole(r, ev.target.checked)}
              />
              <span>{r.name}</span>
              <span className="hint">
                {mine.length
                  ? mine.map((b) => 'slot ' + b.slot + (b.enabled ? '' : ' · ' + t('bindingDisabled'))).join(' / ')
                  : t('notBoundToMachine')}
              </span>
              <span style={{ flex: 1 }}></span>
              <button className="mini ghost" onClick={() => nav('/roles/' + encodeURIComponent(r.id))}>{t('openRole')} →</button>
            </div>
          );
        }) : <p className="hint" style={{ padding: '8px 0' }}>{t('empty')}</p>}
      </div>
      {pairOpen && (
        <PairDialog
          machineId={id}
          onClose={(paired) => {
            setPairOpen(false);
            if (paired) { reloadMachine(); probe(); } // 配对成功后机器本体与在线状态都要刷新
          }}
        />
      )}
    </>
  );
}
