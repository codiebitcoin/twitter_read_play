// RoleAssetsSetupPage：角色资源补全专页（/roles/{id}/setup）。
//
// 向导的「去补全」不再跳到信息量很大的角色详情页，而是到这个只做两件事的页面：
//   1. 🔊 音色：上传语音样本（upload_only，不立刻 clone）→ 激活某个样本
//   2. 🧍 VRM：上传模型（异步 job，进度在全局 MessageQueue 跟进）→ 激活某个模型
// 上传完点「完成」回到来源页（向导新标签页还在原位，刷新即可看到 ✓）。
import { useEffect, useRef, useState } from 'react';
import { api } from '../api.js';
import { t } from '../i18n.js';
import { useToast, useToastErr, useToastOk } from '../toast.jsx';
import { useNav } from '../App.jsx';
import { notifyMessageQueued } from '../messageQueue.js';

export default function RoleAssetsSetupPage({ id }) {
  const nav = useNav();
  const toast = useToast();
  const toastOk = useToastOk();
  const toastErr = useToastErr();
  const [role, setRole] = useState(null);

  const load = async () => {
    try { setRole(await api('/api/roles/' + encodeURIComponent(id))); }
    catch (e) { toastErr(e.message); }
  };
  useEffect(() => { load(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [id]);

  if (!role) return <p className="loading">{t('loading')}</p>;

  const slotMode = role.voice_vrm_mode !== 'custom';
  const voiceOk = !!(role.local_voice || (role.bindings || []).some((b) => b.voice_id));
  const vrmOk = !!role.local_vrm;
  // 原 slot 模式沿用设备原有形象/声音，这个「补全资源」页对它无事可做。
  const allOk = slotMode || (voiceOk && vrmOk);

  return (
    <div style={{ maxWidth: 760, margin: '0 auto', display: 'flex', flexDirection: 'column', gap: 16 }}>
      <div className="page-head">
        <h2>🧩 {t('assetsTitle')}</h2>
        <span className="badge mut">{role.name || role.id}</span>
        <span className={'badge ' + (allOk ? 'ok' : 'warn')}>{allOk ? t('assetsAllDone') : t('assetsIncomplete')}</span>
        <span className="spacer"></span>
        <button onClick={() => nav('/roles/' + encodeURIComponent(role.id))}>{t('assetsOpenRole')}</button>
      </div>
      <p className="hint" style={{ marginTop: -6 }}>{t('assetsSub')}</p>

      {slotMode
        ? <div className="card"><p className="hint" style={{ margin: 0 }}>{t('assetSourceSlotAssetsHint')}</p></div>
        : <>
            <VoiceCard role={role} onSaved={load} />
            <VrmCard role={role} onSaved={load} toast={toast} />
          </>}

      <div className="card" style={{ display: 'flex', gap: 12, alignItems: 'center', flexWrap: 'wrap' }}>
        <button onClick={() => history.length > 1 ? history.back() : nav('/')}>{t('assetsFinish')}</button>
        <span className="hint">{t('assetsFinishHint')}</span>
      </div>
    </div>
  );
}

// ---- 音色 ----
function VoiceCard({ role, onSaved }) {
  const toastOk = useToastOk();
  const toastErr = useToastErr();
  const fileRef = useRef(null);
  const [busy, setBusy] = useState(false);
  const ok = !!(role.local_voice || (role.bindings || []).some((b) => b.voice_id));

  // 仅上传样本并激活（upload_only=1，不触发 clone）；clone 在向导「同步」或绑定面板按机器触发
  const upload = async () => {
    const f = fileRef.current && fileRef.current.files[0];
    if (!f) { toastErr(t('pickAudioFirst')); return; }
    setBusy(true);
    const fd = new FormData(); fd.append('file', f); fd.append('upload_only', '1');
    try {
      await api(`/api/roles/${role.id}/clone`, { method: 'POST', body: fd });
      toastOk(t('sampleUploaded'));
      onSaved();
    } catch (e) { toastErr(e.message); } finally { setBusy(false); }
  };
  const selectVoice = async (file) => {
    try {
      await api(`/api/roles/${role.id}/voice/select`, { method: 'POST', body: JSON.stringify({ file }) });
      toastOk(t('saved')); onSaved();
    } catch (e) { toastErr(e.message); }
  };

  return (
    <div className="card">
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
        <b>🔊 {t('voiceSection')}</b>
        <span className={'badge ' + (ok ? 'ok' : 'warn')}>{ok ? '✓ ' + t('voiceConfigured') : t('voiceMissing')}</span>
      </div>
      <p className="hint" style={{ margin: '8px 0 0' }}>{t('assetsVoiceHint')}</p>

      <label className="field" style={{ marginTop: 10 }}>
        {t('uploadSampleHint')}
        <input type="file" accept="audio/*" ref={fileRef} style={{ marginTop: 6 }} />
      </label>
      <div className="card-actions" style={{ justifyContent: 'flex-start' }}>
        <button className="mini" onClick={upload} disabled={busy}>{busy ? '…' : t('uploadSample')}</button>
      </div>

      {(role.local_voices || []).length > 0 && (
        <table style={{ marginTop: 8 }}>
          <thead><tr><th style={{ textAlign: 'left' }}>{t('assetsSampleList')}</th><th></th></tr></thead>
          <tbody>
            {(role.local_voices || []).map((v) => (
              <tr key={v.file}>
                <td>{String(v.file).split('/').pop()} {v.file === role.local_voice ? <span className="badge ok">✓ {t('active')}</span> : ''}</td>
                <td style={{ textAlign: 'right' }}>
                  {v.file !== role.local_voice
                    ? <button className="mini" onClick={() => selectVoice(v.file)}>{t('selectActive')}</button>
                    : null}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <p className="hint" style={{ marginTop: 6 }}>{t('voiceSelectHint')}</p>
    </div>
  );
}

// ---- VRM ----
function VrmCard({ role, onSaved, toast }) {
  const toastErr = useToastErr();
  const fileRef = useRef(null);
  const [busy, setBusy] = useState(false);
  const ok = !!role.local_vrm;

  // 上传是异步 job：状态进全局 MessageQueue（成功/409/失败都会在队列里看到）
  const upload = async () => {
    const f = fileRef.current && fileRef.current.files[0];
    if (!f) { toastErr(t('pickVrmFirst')); return; }
    setBusy(true);
    const fd = new FormData(); fd.append('file', f);
    try {
      const res = await api(`/api/roles/${role.id}/vrm`, { method: 'POST', body: fd });
      // 上传是异步 job：状态进全局 MessageQueue（不再弹 toast），notify 让面板立刻拉一次
      if (res.ok && res.async) notifyMessageQueued();
      else toastErr(JSON.stringify(res));
      onSaved();
    } catch (e) {
      // 409 = 已有一个全局 VRM 上传在跑（MessageQueue 里能看到进度），不要重复上传
      toastErr(t('vrmJobBusy').includes('%s') ? t('vrmJobBusy').replace('%s', e.message) : t('vrmImportFail') + e.message);
    } finally { setBusy(false); }
  };
  const selectVrm = async (file) => {
    try {
      await api(`/api/roles/${role.id}/vrm/select`, { method: 'POST', body: JSON.stringify({ file }) });
      toast(t('vrmSelectHint')); onSaved();
    } catch (e) { toastErr(e.message); }
  };

  return (
    <div className="card">
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
        <b>🧍 VRM</b>
        <span className={'badge ' + (ok ? 'ok' : 'warn')}>{ok ? '✓ ' + t('vrmConfigured') : t('vrmMissing')}</span>
        {role.local_vrm ? <span className="badge mut">{String(role.local_vrm).split('/').pop()}</span> : null}
      </div>
      <p className="hint" style={{ margin: '8px 0 0' }}>{t('assetsVrmHint')}</p>

      <label className="field" style={{ marginTop: 10 }}>
        {t('vrmPickFile')} · cloth {role.cloth_id || 2}
        <input type="file" accept=".vrm" ref={fileRef} style={{ marginTop: 6 }} />
      </label>
      <div className="card-actions" style={{ justifyContent: 'flex-start' }}>
        <button className="mini" onClick={upload} disabled={busy}>{busy ? '…' : t('uploadVrm')}</button>
      </div>

      {(role.local_vrms || []).length > 0 && (
        <table style={{ marginTop: 8 }}>
          <thead><tr><th style={{ textAlign: 'left' }}>{t('assetsVrmList')}</th><th></th><th></th></tr></thead>
          <tbody>
            {(role.local_vrms || []).map((v) => (
              <tr key={v.file}>
                <td>{String(v.file).split('/').pop()} {v.file === role.local_vrm ? <span className="badge ok">✓ {t('active')}</span> : ''}</td>
                <td>cloth {v.cloth_id || 2}</td>
                <td style={{ textAlign: 'right' }}>
                  {v.file !== role.local_vrm
                    ? <button className="mini" onClick={() => selectVrm(v.file)}>{t('selectActive')}</button>
                    : null}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}
