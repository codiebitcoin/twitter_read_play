import { useEffect, useRef, useState } from 'react';
import { api } from '../../api.js';
import { t } from '../../i18n.js';
import { useToast, useToastErr, useToastOk } from '../../toast.jsx';
import SyncRoleButton from '../SyncRoleButton.jsx';
import { putRole } from './shared.js';
import { notifyMessageQueued } from '../../messageQueue.js';
import VrmPreview from './VrmPreview.jsx';

// ---- Persona：头像 Icon / Prompt（多选一）/ Voice（多选一）/ VRM（多选一）+ 同步到 Codie ----
export default function PersonaTab({ role, machines, onSaved }) {
  const toast = useToast();
  const toastOk = useToastOk();
  const toastErr = useToastErr();
  const [prompt, setPrompt] = useState(role.prompt || '');
  const [name, setName] = useState(role.name || '');
  const [desc, setDesc] = useState(role.description || '');
  // 形象性别 / 动作强度：设备 character.sex（1男/2女/3其它）与 animation_intensity（1弱/2中/3强），
  // 动作强度默认 STRONG（3，与设备默认一致）；性别 0=沿用设备当前值。
  const [sex, setSex] = useState(String(role.sex || 0));
  const [anim, setAnim] = useState(String(role.animation_intensity || 3));
  const voiceFileRef = useRef(null);
  const vrmFileRef = useRef(null);
  const [busyCheck, setBusyCheck] = useState(false);
  // Voice / VRM 来源：原 slot（默认，不接管设备形象/声音）或角色自定义。
  const [assetMode, setAssetMode] = useState(role.voice_vrm_mode === 'custom' ? 'custom' : 'slot');
  useEffect(() => {
    setAssetMode(role.voice_vrm_mode === 'custom' ? 'custom' : 'slot');
  }, [role.id, role.voice_vrm_mode]);
  const slotMode = assetMode === 'slot';
  // 卡片折叠的默认状态：已就绪的资源（本地上传且已激活）默认收起，只留标题+徽标一行
  const voiceReady = !!(role.local_voice || (role.bindings || []).some((b) => b.voice_id));
  const vrmReady = !!role.local_vrm;
  const voiceOk = slotMode || voiceReady;
  const vrmOk = slotMode || vrmReady;

  const save = async (extra) => {
    try {
      await putRole(role, { ...extra, prompt, name, description: desc });
      toastOk(t('saved'));
      onSaved && onSaved();
    } catch (e) { toastErr(e.message); }
  };

  // 头像上传已移到页面顶部 actionbar（RoleDetailPage 的 RoleAvatar）

  const prompts = role.prompts || [];
  const saveAsPreset = () => {
    const name = prompt(t('presetName') + '：');
    if (!name || !prompt) return;
    save({ prompts: [...prompts.filter((p) => p.name !== name), { name, text: prompt }] });
  };
  const applyPreset = (p) => { setPrompt(p.text); save({ prompts }); };
  const delPreset = (name) => {
    if (!confirm(t('deleteConfirm'))) return;
    save({ prompts: prompts.filter((p) => p.name !== name) });
  };

  // 仅上传样本并激活（不触发 clone）；clone 在绑定面板按机器触发，或 sync 时自动进行
  const clone = async () => {
    const f = voiceFileRef.current && voiceFileRef.current.files[0];
    if (!f) { toastErr(t('pickAudioFirst')); return; }
    const fd = new FormData(); fd.append('file', f); fd.append('upload_only', '1');
    try {
      await api(`/api/roles/${role.id}/clone`, { method: 'POST', body: fd });
      toastOk(t('sampleUploaded'));
      onSaved && onSaved();
    } catch (e) { toastErr(e.message); }
  };

  const vrmUpload = async () => {
    const f = vrmFileRef.current && vrmFileRef.current.files[0];
    if (!f) { toastErr('先选择 .vrm 文件'); return; }
    const fd = new FormData(); fd.append('file', f);
    try {
      const res = await api(`/api/roles/${role.id}/vrm`, { method: 'POST', body: fd });
      // 上传是异步 job：状态进全局 MessageQueue（不再弹 toast），notify 让面板立刻拉一次
      if (res.ok && res.async) notifyMessageQueued();
      else toastErr(JSON.stringify(res));
      onSaved && onSaved();
    } catch (e) {
      // 409 = 已有一个全局 VRM 上传在跑（MessageQueue 里能看到进度），不要重复上传
      toastErr(t('vrmJobBusy').includes('%s') ? t('vrmJobBusy').replace('%s', e.message) : '导入失败：' + e.message);
    }
  };
  const selectVrm = async (file) => {
    try {
      await api(`/api/roles/${role.id}/vrm/select`, { method: 'POST', body: JSON.stringify({ file }) });
      toast(t('vrmSelectHint')); onSaved && onSaved(); // 刷新列表 ✓ 与预览（VrmPreview 按 file 变化重建场景）
    } catch (e) { toastErr(e.message); }
  };
  const selectVoice = async (file) => {
    try {
      await api(`/api/roles/${role.id}/voice/select`, { method: 'POST', body: JSON.stringify({ file }) });
      toastOk(t('saved')); onSaved && onSaved();
    } catch (e) { toastErr(e.message); }
  };
  // 删除本地资源（文件 + 登记）；删的是激活项时服务端自动切到剩余第一个
  const delVrm = async (file) => {
    if (!confirm(t('delAssetConfirm'))) return;
    try {
      await api(`/api/roles/${role.id}/vrm`, { method: 'DELETE', body: JSON.stringify({ file }) });
      toastOk(t('saved')); onSaved && onSaved();
    } catch (e) { toastErr(e.message); }
  };
  const delVoice = async (file) => {
    if (!confirm(t('delAssetConfirm'))) return;
    try {
      await api(`/api/roles/${role.id}/voice`, { method: 'DELETE', body: JSON.stringify({ file }) });
      toastOk(t('saved')); onSaved && onSaved();
    } catch (e) { toastErr(e.message); }
  };
  // VRM 展示名（默认=上传时原始文件名）：只改登记的 Name，落盘文件不动
  const renameVrm = async (file, cur) => {
    const name = prompt(t('vrmRename') + '：', cur);
    if (!name || name === cur) return;
    try {
      await api(`/api/roles/${role.id}/vrm/rename`, { method: 'POST', body: JSON.stringify({ file, name }) });
      toastOk(t('saved')); onSaved && onSaved();
    } catch (e) { toastErr(e.message); }
  };

  return (
    <div className="persona-grid">
      {/* Name / Description（同步到设备 character）：可折叠，默认收起省空间 */}
      <CollapseCard title={<b>🪪 {t('nameDescTitle')}</b>} defaultOpen={false}>
        <div className="form-grid" style={{ marginTop: 8 }}>
          <label className="field">{t('name')}<input value={name} onChange={(e) => setName(e.target.value)} /></label>
          <label className="field">{t('roleDesc')}<input value={desc} onChange={(e) => setDesc(e.target.value)} placeholder={t('roleDescPh')} /></label>
        </div>
        <div className="card-actions"><button onClick={() => save({})}>{t('save')}</button></div>
        <p className="hint" style={{ margin: 0 }}>{t('nameDescHint')}</p>
      </CollapseCard>

      {/* 形象与动作：性别 + 动作强度（设备 character.sex / animation_intensity，强度默认 STRONG）：可折叠，默认收起 */}
      <CollapseCard title={<b>🧍 {t('charLook')}</b>} defaultOpen={false}>
        <div className="form-grid" style={{ marginTop: 8 }}>
          <label className="field">{t('sex')}
            <select value={sex} onChange={(e) => setSex(e.target.value)}>
              <option value="0">{t('sexInherit')}</option>
              <option value="1">{t('sexMale')}</option>
              <option value="2">{t('sexFemale')}</option>
              <option value="3">{t('sexOther')}</option>
            </select>
          </label>
          <label className="field">{t('animIntensity')}
            <select value={anim} onChange={(e) => setAnim(e.target.value)}>
              <option value="1">{t('animLow')}</option>
              <option value="2">{t('animMedium')}</option>
              <option value="3">{t('animHigh')}</option>
            </select>
          </label>
        </div>
        <p className="hint" style={{ margin: '8px 0 0', wordBreak: 'normal' }}>{t('charLookHint')}</p>
        <div className="card-actions"><button onClick={() => save({ sex: +sex || 0, animation_intensity: +anim || 0 })}>{t('save')}</button></div>
      </CollapseCard>

      {/* Prompt：编辑主区，跨双列；可折叠 */}
      <CollapseCard className="span-2" title={<b>📝 {t('systemPrompt')}</b>}>
        <label className="field" style={{ marginTop: 8 }}><textarea rows="8" value={prompt} onChange={(e) => setPrompt(e.target.value)} /></label>
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6, alignItems: 'center' }}>
          {(role.prompts || []).map((p) => (
            <span key={p.name} style={{ display: 'inline-flex', gap: 4, alignItems: 'center' }}>
              <button type="button" className="mini ghost" title={t('presetApply')} onClick={() => applyPreset(p)}>{p.name}{p.text === prompt ? ' ✓' : ''}</button>
              <button type="button" className="mini ghost danger" title={t('del')} onClick={() => delPreset(p.name)}>×</button>
            </span>
          ))}
          <button type="button" className="mini ghost" onClick={saveAsPreset}>＋ {t('presetSave')}</button>
        </div>
        <p className="hint" style={{ marginTop: 6 }}>{t('presetApplyHint')}</p>
      </CollapseCard>

      {/* Voice / VRM 来源：默认「原 slot」——沿用设备已有形象与声音，不上传/不克隆 */}
      <CollapseCard className="span-2" title={<b>{t('assetSourceTitle')}</b>} defaultOpen>
        <div className="form-grid" style={{ marginTop: 8 }}>
          <label className="field">{t('assetSourceLabel')}
            <select value={assetMode} onChange={(e) => setAssetMode(e.target.value)}>
              <option value="slot">{t('assetSourceSlot')}</option>
              <option value="custom">{t('assetSourceCustom')}</option>
            </select>
          </label>
        </div>
        <p className="hint" style={{ margin: '8px 0' }}>{t('assetSourceHint')}</p>
        <div className="card-actions" style={{ justifyContent: 'flex-start' }}>
          <button className="mini" onClick={() => save({ voice_vrm_mode: assetMode })}>{t('save')}</button>
        </div>
      </CollapseCard>

      {/* Voice：已配置时默认收起，点标题展开 */}
      <CollapseCard
        title={<b>🔊 Voice</b>}
        badge={
          <>
            {slotMode
              ? <span className="badge mut">{t('assetSourceSlotBadge')}</span>
              : <span className={'badge ' + (voiceReady ? 'ok' : 'warn')}>{voiceReady ? '✓ ' + t('voiceConfigured') : t('voiceMissing')}</span>}
            {!slotMode && (role.local_voices || []).length > 0 && <span className="badge mut">{role.local_voices.length}/5</span>}
          </>
        }
        defaultOpen={!voiceOk}
        id="persona-voice"
      >
        <p className="hint" style={{ margin: '0 0 8px' }}>{slotMode ? t('assetSourceSlotAssetsHint') : t('bindingVoiceHint')}</p>
        <label className="field">{t('uploadSampleHint')}<input type="file" accept="audio/*" ref={voiceFileRef} style={{ marginTop: 6 }} /></label>
        <div className="row" style={{ display: 'flex', gap: 8 }}><button className="mini" onClick={clone}>{t('uploadSample')}</button></div>
        {(role.local_voices || []).length > 0 && (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 6, marginTop: 8 }}>
            {(role.local_voices || []).map((v) => (
              <div key={v.file} className="sample-row">
                <VoicePlay roleId={role.id} file={v.file} />
                <span style={{ flex: 1, wordBreak: 'break-all' }}>{String(v.file).split('/').pop()}</span>
                {/* 下载样本：走 /asset 附件下载（服务端校验 file 必须是本角色登记的资源） */}
                <a className="mini ghost" title={t('download')} href={`/api/roles/${encodeURIComponent(role.id)}/asset?file=` + encodeURIComponent(v.file)}>⬇</a>
                <button className="mini ghost danger" title={t('del')} onClick={() => delVoice(v.file)}>🗑</button>
                {v.file === role.local_voice
                  ? <span className="badge ok">✓ {t('active')}</span>
                  : <button className="mini" onClick={() => selectVoice(v.file)}>{t('selectActive')}</button>}
              </div>
            ))}
          </div>
        )}
        <p className="hint" style={{ marginTop: 6 }}>{t('voiceSelectHint')}</p>
      </CollapseCard>

      {/* VRM：已配置时默认收起，点标题展开（头像上传在页面顶部 actionbar） */}
      <CollapseCard
        title={<b>🧍 VRM</b>}
        badge={
          <>
            {slotMode
              ? <span className="badge mut">{t('assetSourceSlotBadge')}</span>
              : <span className={'badge ' + (vrmReady ? 'ok' : 'warn')}>{vrmReady ? '✓ ' + t('vrmConfigured') : t('vrmMissing')}</span>}
            {!slotMode && vrmReady ? <span className="badge mut">{vrmDisplayName(role)}</span> : null}
            {!slotMode && (role.local_vrms || []).length > 0 && <span className="badge mut">{role.local_vrms.length}/5</span>}
          </>
        }
        defaultOpen={!vrmOk}
        id="persona-vrm"
        className="span-2"
      >
        {/* 实时预览：拖拽旋转，可截图直接设为角色头像 */}
        <VrmPreview roleId={role.id} file={role.local_vrm} onUploaded={onSaved} />
        <label className="field">{t('vrmPickFile')} · cloth {role.cloth_id || 2}<input type="file" accept=".vrm" ref={vrmFileRef} style={{ marginTop: 6 }} /></label>
        <div className="card-actions" style={{ justifyContent: 'flex-start' }}><button className="mini" onClick={vrmUpload}>{t('uploadVrm')}</button></div>
        {(role.local_vrms || []).length > 0 && (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 6, marginTop: 8 }}>
            {(role.local_vrms || []).map((v) => (
              <div key={v.file} className="sample-row">
                <span style={{ flex: 1, wordBreak: 'break-all' }} title={String(v.file).split('/').pop()}>{v.name || String(v.file).split('/').pop()}</span>
                <span className="badge mut">cloth {v.cloth_id || 2}</span>
                <button className="mini ghost" title={t('vrmRename')} onClick={() => renameVrm(v.file, v.name || String(v.file).split('/').pop())}>✏</button>
                {/* 下载模型：走 /asset 附件下载（服务端校验 file 必须是本角色登记的资源） */}
                <a className="mini ghost" title={t('download')} href={`/api/roles/${encodeURIComponent(role.id)}/asset?file=` + encodeURIComponent(v.file)}>⬇</a>
                <button className="mini ghost danger" title={t('del')} onClick={() => delVrm(v.file)}>🗑</button>
                {v.file === role.local_vrm
                  ? <span className="badge ok">✓ {t('active')}</span>
                  : <button className="mini" onClick={() => selectVrm(v.file)}>{t('selectActive')}</button>}
              </div>
            ))}
          </div>
        )}
      </CollapseCard>

      {/* 同步 / 校验：Persona 的落点，使用各分类的激活项 */}
      <div className="card span-2" style={{ display: 'flex', gap: 12, alignItems: 'center', flexWrap: 'wrap' }}>
        <SyncRoleButton role={role} machines={machines} onSynced={onSaved} />
        <button className="mini" disabled={busyCheck} title={t('checkRun')} onClick={async () => {
          setBusyCheck(true);
          try {
            const res = await api(`/api/roles/${role.id}/check`, { method: 'POST', body: '{}' });
            const bad = (res.results || []).filter((x) => !x.ok).length;
            toastOk(bad ? `${bad} binding(s) mismatched — see Status tab` : t('checkOk'));
          } catch (e) { toastErr(e.message); }
          setBusyCheck(false);
        }}>{busyCheck ? '…' : '🔍 ' + t('checkBtn')}</button>
        <span className="hint">{t('syncUsesActiveHint')}</span>
      </div>
    </div>
  );
}

// vrmDisplayName 激活 VRM 的展示名：登记过的用 Name，否则回退文件名。
function vrmDisplayName(role) {
  const v = (role.local_vrms || []).find((x) => x.file === role.local_vrm);
  return (v && v.name) || String(role.local_vrm).split('/').pop();
}

// 可折叠卡片：标题行可点，收起时只留一行标题+徽标（用于已配置好的资源卡减少视觉噪音）
function CollapseCard({ title, badge, defaultOpen = true, id, className = '', children }) {
  const [open, setOpen] = useState(defaultOpen);
  return (
    <div className={'card ' + className} id={id}>
      <div className="cc-head" onClick={() => setOpen(!open)}>
        <span className="cc-chev">{open ? '▾' : '▸'}</span>
        {title}
        {badge}
      </div>
      {open && <div style={{ marginTop: 10 }}>{children}</div>}
    </div>
  );
}

// 语音样本点击播放：▶/⏸ 切换，离开组件时停掉
function VoicePlay({ roleId, file }) {
  const audioRef = useRef(null);
  const [playing, setPlaying] = useState(false);
  const url = `/api/roles/${encodeURIComponent(roleId)}/asset?file=` + encodeURIComponent(file);
  useEffect(() => () => { audioRef.current && audioRef.current.pause(); }, []);
  const toggle = () => {
    if (!audioRef.current) {
      audioRef.current = new Audio(url);
      audioRef.current.onended = () => setPlaying(false);
      audioRef.current.onerror = () => setPlaying(false);
    }
    if (playing) {
      audioRef.current.pause(); audioRef.current.currentTime = 0; setPlaying(false);
    } else {
      setPlaying(true);
      audioRef.current.play().catch(() => setPlaying(false));
    }
  };
  return <button type="button" className={'mini ghost' + (playing ? ' active' : '')} title={playing ? t('voiceStop') : t('voicePlay')} onClick={toggle}>{playing ? '⏸' : '▶'}</button>;
}
