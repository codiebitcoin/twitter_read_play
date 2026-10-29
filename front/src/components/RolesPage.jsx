import { useEffect, useRef, useState } from 'react';
import { api } from '../api.js';
import { t } from '../i18n.js';
import { useToast, useToastErr, useToastOk } from '../toast.jsx';
import { useNav } from '../App.jsx';
import { platformLabel as locPlatformLabel } from './common/adapters.js';
import { thumbPlaceholder } from '../vrmThumb.js';
import SyncRoleButton from './SyncRoleButton.jsx';
import { notifyMessageQueued } from '../messageQueue.js';

function RoleRow({ role, machines, onReload }) {
  const nav = useNav();
  const toast = useToast();
  const toastOk = useToastOk();
  const toastErr = useToastErr();
  const cvRef = useRef(null);

  useEffect(() => {
    const cv = cvRef.current;
    if (!cv || role.icon) return; // 有头像用 <img> 展示，canvas 只做无头像占位
    thumbPlaceholder(cv, role.name);
  }, [role.icon, role.name]);

  const act = async (e, kind) => {
    e.stopPropagation();
    try {
      if (kind === 'poll') { await api(`/api/roles/${encodeURIComponent(role.id)}/poll`, { method: 'POST' }); notifyMessageQueued(); }
      else if (kind === 'mon') {
        if (role.monitor_id) { await api(`/api/roles/${encodeURIComponent(role.id)}/monitor`, { method: 'DELETE' }); toastOk(t('monOff') + ' ✓'); }
        else { await api(`/api/roles/${encodeURIComponent(role.id)}/monitor`, { method: 'POST' }); toastOk(t('monOn') + ' ✓'); }
        onReload && onReload();
      }
      else {
        const res = await api(`/api/roles/${encodeURIComponent(role.id)}/broadcast`, { method: 'POST', body: '{}' });
        toastOk(t('bqQueued').replace('%s', (res && res.queued) || 0));
        notifyMessageQueued();
      }
    } catch (e2) { toastErr(e2.message); }
  };

  // 已知社交平台：webhook 铃铛对它们都显示（是否真支持由后端决定并返回可读错误）。
  // 之前只认 twitter，微博/小红书角色的铃铛被整个隐藏了。
  const isSocial = ['twitter', 'x', 'weibo', 'xiaohongshu', 'instagram', 'truth', 'bluesky', 'bilibili', 'rss'].includes((role.platform || 'twitter').toLowerCase());
  // platformLabel 平台显示名（common/adapters 里走 i18n）
  const platformLabel = (p) => locPlatformLabel(p);

  return (
    <div className="role-row" onClick={() => nav('/roles/' + encodeURIComponent(role.id))}>
      {role.icon
        ? <img className="model-thumb" width="96" height="96" style={{ objectFit: 'cover', borderRadius: 12 }} src={`/api/roles/${encodeURIComponent(role.id)}/icon?v=${encodeURIComponent(role.updated_at || '')}`} alt="" />
        : <canvas ref={cvRef} className="model-thumb" width="96" height="96"></canvas>}
      <div className="role-main">
        <div className="role-name">{role.name}</div>
        <div className="role-tags">
          <span className="badge mut">{(role.bindings || []).length} binding{(role.bindings || []).length === 1 ? '' : 's'}</span>
          <span className="badge mut">{platformLabel(role.platform)}{role.username ? '/@' + role.username : ''}</span>
          <span className={'badge ' + ((role.bindings || []).some((b) => b.voice_id) ? 'ok' : 'mut')}>🔊 {(role.bindings || []).filter((b) => b.voice_id).length || 'auto clone'}</span>
          {(role.bindings || []).slice(0, 2).map((b) => {
            const bm = machines.find((m) => m.id === b.machine_id);
            return <span key={b.machine_id + ':' + b.slot} className={'badge ' + (b.enabled ? 'mut' : 'warn')}>🖥 {bm ? (bm.name || bm.addr) : b.machine_id} · s{b.slot}</span>;
          })}
          <span className={'badge ' + (role.enabled ? 'ok' : 'warn')}>{role.enabled ? t('enabled') : t('disabled')}</span>
          {role.dead ? <span className="badge bad" title={role.dead_reason || ''}>☠ {t('dead')}</span> : ''}
          {role.monitor_id ? <span className="badge ok" title="socialdata webhook">🔔</span> : ''}
        </div>
      </div>
      <div className="row-actions">
        <SyncRoleButton role={role} machines={machines} onSynced={onReload} />
        {role.username && isSocial
          ? <button className="mini ghost" title={role.monitor_id ? t('monOff') : t('monOn')} onClick={(e) => act(e, 'mon')}>{role.monitor_id ? '🔔' : '🔕'}</button>
          : null}
        <button className="mini ghost" title={role.dead ? t('reviveHint') : t('pollNow')} onClick={(e) => act(e, 'poll')}>⟳</button>
        <button className="mini ghost" title={t('broadcastRandom')} onClick={(e) => act(e, 'bcast')}>▶</button>
      </div>
      <span className="role-arrow">→</span>
    </div>
  );
}

export default function RolesPage() {
  const [roles, setRoles] = useState(null);
  const [machines, setMachines] = useState([]);
  const toast = useToast();
  const toastErr = useToastErr();
  const nav = useNav();

  const load = () => {
    (async () => {
      try {
        const rs = await api('/api/roles');
        const ms = await api('/api/machines');
        setRoles(rs); setMachines(ms);
      } catch (e) { toastErr(e.message); }
    })();
  };
  useEffect(() => { load(); }, []);

  const addRole = async () => {
    const name = prompt(t('newRole') + '：');
    if (!name) return;
    try {
      const r = await api('/api/roles', { method: 'POST', body: JSON.stringify({ name, slot: 0, enabled: true }) });
      nav('/roles/' + encodeURIComponent(r.id));
    } catch (e) { toastErr(e.message); }
  };

  if (!roles) return <p className="loading">{t('loading')}</p>;

  return (
    <>
      <div className="actionbar">
        <button onClick={addRole}>{t('newRole')}</button>
        <span className="hint">{t('roleConcept')}</span>
        <span style={{ flex: 1 }}></span>
        <span className="hint">{roles.length} roles</span>
      </div>
      {!roles.length ? (
        <p className="empty"><span className="icon">🎭</span>{t('empty')}</p>
      ) : (
        <div className="role-rows">
          {roles.map((r) => (
            <RoleRow key={r.id} role={r} machines={machines} onReload={load} />
          ))}
        </div>
      )}
    </>
  );
}
