import { useEffect, useState } from 'react';
import { api } from '../api.js';
import { t } from '../i18n.js';
import { useToastOk, useToastErr } from '../toast.jsx';
import { getProbeVoice } from '../voiceProbePref.js';

// 一键同步角色包到 Codie：未绑定机器/槽位时弹槽位设置（绑定会被记住，下次直接同步）。
export default function SyncRoleButton({ role, machines, className = 'mini', onSynced }) {
  const toastOk = useToastOk();
  const toastErr = useToastErr();
  const [needBinding, setNeedBinding] = useState(false);
  const [busy, setBusy] = useState(false);
  const [machineID, setMachineID] = useState(role.machine_id || '');
  const [slot, setSlot] = useState(role.slot > 0 ? role.slot : 1);

  const doSync = async (binding) => {
    setBusy(true);
    try {
      const res = await api(`/api/roles/${encodeURIComponent(role.id)}/sync`, {
        method: 'POST',
        // probe_voice：先试听验证已记录的克隆能播才沿用（默认开，见 voiceProbePref）
        body: JSON.stringify({ ...(binding || {}), probe_voice: getProbeVoice() }),
      });
      if (res.code === 'NEED_BINDING') { setNeedBinding(true); return; }
      if (res.ok) {
        // 原 slot 模式不上传 VRM：提示文案跟着模式走，避免误导「VRM 后台补传」。
        toastOk(role.voice_vrm_mode === 'custom' ? t('syncedVrmBackfill') : t('synced') + ' ✓（' + t('assetSourceSlotBadge') + '）');
        setNeedBinding(false);
        onSynced && onSynced();
      }
    } catch (e) { toastErr(e.message); }
    setBusy(false);
  };

  return (
    <>
      <button className={className} disabled={busy} title={t('syncTip')} onClick={(e) => { e.stopPropagation(); doSync(); }}>
        {busy ? '…' : `⚡ ${t('syncToCodie')}`}
      </button>
      {needBinding && (
        <dialog open style={{ position: 'fixed', inset: 0, margin: 'auto', zIndex: 50 }}>
          <h3>{t('slotBindingTitle')}</h3>
          <p className="hint" style={{ marginTop: 0 }}>{t('slotBindingHint')}</p>
          <label className="field">{t('bindMachine')}
            <select value={machineID} onChange={(e) => setMachineID(e.target.value)}>
              <option value="">--</option>
              {(machines || []).map((m) => <option key={m.id} value={m.id}>{m.name || m.addr}</option>)}
            </select>
          </label>
          <label className="field">{t('slot')}
            <input type="number" min="1" max="20" value={slot} onChange={(e) => setSlot(+e.target.value)} />
          </label>
          <div className="row">
            <button className="secondary" onClick={() => setNeedBinding(false)}>{t('cancel')}</button>
            <button
              disabled={!machineID || slot <= 0 || busy}
              onClick={() => doSync({ machine_id: machineID, slot })}
            >{t('save')} & {t('syncToCodie')}</button>
          </div>
        </dialog>
      )}
    </>
  );
}
