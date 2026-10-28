// SetupWizard：分步「添加设备 + 角色」向导（弹窗）。
//
// 与参考项目 codie_life_control 的 SetupWizard 同构（左侧步骤条、一步一件事、可回退），
// 但顺序按本项目的模型调整：
//
//   设备 → 设置角色 → 绑定槽位 → 同步角色信息 → 完成
//   (machine → role → binding → sync → done)
//
// 参考项目里的「账号（Account）」在本项目对应「角色（Role）」：角色才是人设/音色/VRM 的整体包。
// 所有步骤都复用现有接口，不新增"一键配齐"事务：
//   - 机器：POST /api/machines + pair/start|confirm，局域网扫描 GET /api/machines/discover
//   - 角色：POST /api/roles / PUT /api/roles/{id}（角色信息就是这一步"同步"的内容）
//   - 绑定：PUT /api/roles/{id} 的 bindings（同机同槽位互斥）
//   - 同步：POST /api/roles/{id}/apply（基础信息，快）或 /sync（含音色/形象，可能几分钟）
//
// 各步骤组件在 WizardSteps.jsx，共用常量/工具在 WizardShared.jsx。
import { useEffect, useMemo, useRef, useState } from 'react';
import { api } from '../../api.js';
import { t } from '../../i18n.js';
import { useToastErr, useToastOk } from '../../toast.jsx';
import { useNav } from '../../App.jsx';
import { platformLabel, uiPlatform } from '../roleDetail/shared.js';
import { BASE_STEPS, editFromRole, hasVoice, normUsername, slotVoiceVrm, sleep, SLOTS, SummaryRow } from './WizardShared.jsx';
import { KeyStep, MachineStep, RoleStep, TweetPickStep } from './WizardSteps.jsx';

export default function SetupWizard({ machines, roles, onClose, onDone }) {
  const toastOk = useToastOk();
  const toastErr = useToastErr();
  const nav = useNav();

  const [step, setStep] = useState(0);
  const dlgRef = useRef(null);

  // 用 showModal() 打开：背景变暗、点不到后面的页面，Esc 也走统一确认。
  useEffect(() => {
    const d = dlgRef.current;
    if (!d) return;
    if (!d.open) {
      try { d.showModal(); } catch { d.setAttribute('open', ''); }
    }
  }, []);

  // Step 1：设备。machineList 是本地副本：向导内新建/配对后后续步骤立刻能看到。
  const initialMachine = (machines || []).find((m) => m.creds) || (machines || [])[0];
  const [machineList, setMachineList] = useState(machines || []);
  const [machineId, setMachineId] = useState(initialMachine ? initialMachine.id : '');
  const [addingMachine, setAddingMachine] = useState(!(machines || []).length);

  // Step 2：角色（选择已有或新建）。roleDetail 是选中角色从后端重新拉取的完整信息
  // —— 这就是「同步角色信息」的第一步：列表里可能是旧快照，进入向导后以服务端为准。
  const [roleList, setRoleList] = useState(roles || []);
  // 有角色时默认选第一个（后端按创建时间排序），没有才落到「新建角色」。
  const [roleChoice, setRoleChoice] = useState((roles || [])[0] ? (roles || [])[0].id : 'new'); // 'new' | role id
  const [roleDetail, setRoleDetail] = useState(null);
  const [roleLoading, setRoleLoading] = useState(false);
  const [roleEdit, setRoleEdit] = useState({ name: '', description: '', prompt: '', platform: 'x', username: '', feed_target: '', live_url: '' });
  const [newRole, setNewRole] = useState({ name: '', description: '', prompt: '', platform: 'x', username: '', feed_target: '', live_url: '' });
  const [persistedRole, setPersistedRole] = useState(null);

  // Step 3：绑定槽位。
  const [slot, setSlot] = useState(1);

  // Step 4：同步。
  const [sync, setSync] = useState({ mode: '', busy: false, ok: false, skipped: false, error: '', detail: '' });

  const [saving, setSaving] = useState(false);
  const roleId = roleChoice !== 'new' ? roleChoice : '';

  // socialdata key：没配置时它就是向导第一步（在设备之前）——拉推文/试播都依赖它。
  // 配好之后这一步自动收起，step 回到 0 时内容就是「选择设备」。
  // 但桌面端还有第二条路：内置浏览器登录平台后直接用页面接口，不需要 key。
  const [sd, setSd] = useState('loading'); // 'loading' | 'missing' | 'ready'
  const [sdKey, setSdKey] = useState('');
  const [sdMode, setSdMode] = useState('browser'); // 'key' | 'browser'：默认内置浏览器登录（不可用时回落 key）
  const [browser, setBrowser] = useState(null);
  useEffect(() => {
    api('/api/setup/status')
      .then((st) => setSd(st.data_source_ready || st.socialdata_ready ? 'ready' : 'missing'))
      .catch(() => setSd('missing'));
    api('/api/browser/session').then(setBrowser).catch(() => setBrowser({ available: false, providers: [] }));
  }, []);
  // 默认「内置浏览器直接登录」（免 Key）；仅桌面端可用——Web 模式没有内置浏览器时回落填 key。
  useEffect(() => {
    if (!browser) return;
    if (!browser.available) setSdMode('key');
  }, [browser]);
  const browserLogged = !!browser && (browser.providers || []).some((p) => p.connected);
  const needSd = sd === 'missing' && !browserLogged;
  // 步骤条：没配 key 时在最前面多一步「数据源」，配好后自动收起。
  const STEPS = needSd ? ['wizStepKey', ...BASE_STEPS] : BASE_STEPS;
  // stepNum 是「基础步骤」编号：0 设备 / 1 角色 / 2 绑定 / 3 同步 / 4 试播。
  const stepNum = needSd ? step - 1 : step;
  const selectedRole = roleDetail && roleDetail.id === roleId
    ? roleDetail
    : (roleList.find((r) => r.id === roleId) || null);
  const machine = machineList.find((m) => m.id === machineId) || null;

  // 选中已有角色：重新拉详情（同步角色信息），并填充可编辑字段。
  useEffect(() => {
    if (!roleId) { setRoleDetail(null); setRoleLoading(false); return; }
    let dead = false;
    setRoleLoading(true);
    api('/api/roles/' + encodeURIComponent(roleId))
      .then((r) => {
        if (dead) return;
        setRoleDetail(r);
        // 以服务端最新快照为基准（含已回填的 binding voice_id），避免后续 PUT
        // 用旧快照把克隆结果冲掉。
        setPersistedRole(r);
        setRoleEdit(editFromRole(r));
      })
      .catch((e) => { if (!dead) toastErr(e.message); })
      .finally(() => { if (!dead) setRoleLoading(false); });
    return () => { dead = true; };
  }, [roleId, toastErr]);

  // 槽位占用：同机同 slot 只能有一个角色（服务端硬约束）。列出全部角色，包括停用的
  // —— 停用角色仍占着槽位，向导不能推荐一个保存时必然冲突的 slot。
  const occupied = useMemo(() => {
    const out = new Map();
    for (const r of roleList) {
      if (r.id === roleId) continue;
      for (const b of r.bindings || []) {
        if (b.machine_id === machineId && b.slot > 0) out.set(b.slot, r);
      }
    }
    return out;
  }, [roleList, machineId, roleId]);

  // 换机器/换角色后：优先沿用该角色在这台机器上的旧槽位，否则取第一个空闲槽位。
  useEffect(() => {
    if (!machineId) return;
    const own = (selectedRole?.bindings || []).find((b) => b.machine_id === machineId);
    if (own && own.slot > 0) { setSlot(own.slot); return; }
    const free = SLOTS.find((s) => !occupied.has(s));
    setSlot(free || 1);
  }, [machineId, roleId, selectedRole, occupied]);

  // 目标（机器/角色/槽位）变了 → 之前的同步结果作废，避免把旧 slot 的"成功"带过去。
  useEffect(() => {
    setSync({ mode: '', busy: false, ok: false, skipped: false, error: '', detail: '' });
  }, [machineId, slot, roleId]);

  const saveRoleInfo = async () => {
    const info = roleChoice === 'new' ? newRole : roleEdit;
    const name = String(info.name || '').trim();
    if (!name) throw new Error(t('wizRoleNameRequired'));
    const payload = {
      name,
      description: String(info.description || '').trim(),
      prompt: String(info.prompt || '').trim(),
      platform: uiPlatform(info.platform),
      username: normUsername(info.username),
      feed_target: String(info.feed_target || '').trim(),
      live_url: String(info.live_url || '').trim(),
    };
    let saved;
    if (roleChoice === 'new') {
      saved = await api('/api/roles', {
        method: 'POST',
        body: JSON.stringify({ ...payload, enabled: true }),
      });
      setRoleList((list) => (list.some((r) => r.id === saved.id) ? list : [...list, saved]));
      setRoleChoice(saved.id);
    } else {
      const base = roleDetail || roleList.find((r) => r.id === roleChoice);
      if (!base) throw new Error(t('wizRoleMissing'));
      saved = await api('/api/roles/' + encodeURIComponent(base.id), {
        method: 'PUT',
        body: JSON.stringify({ ...base, ...payload }),
      });
      setRoleList((list) => list.map((r) => (r.id === saved.id ? saved : r)));
    }
    setRoleDetail(saved);
    setRoleEdit(editFromRole(saved));
    setPersistedRole(saved);
    return saved;
  };

  // 写入绑定（同机只保留一个槽位；同槽位保留已有 voice_id，换槽位则清空让设备重新克隆）。
  const persistBinding = async (role) => {
    const base = role || persistedRole || roleDetail;
    if (!base) throw new Error(t('wizRoleMissing'));
    const previous = (base.bindings || []).find((b) => b.machine_id === machineId && b.slot === +slot);
    const bindings = [
      ...(base.bindings || []).filter((b) => b.machine_id !== machineId),
      {
        machine_id: machineId,
        slot: +slot,
        enabled: true,
        voice_id: previous ? (previous.voice_id || '') : '',
        group_key: previous ? (previous.group_key || '') : '',
      },
    ];
    const saved = await api('/api/roles/' + encodeURIComponent(base.id), {
      method: 'PUT',
      body: JSON.stringify({ ...base, bindings }),
    });
    setRoleDetail(saved);
    setPersistedRole(saved);
    setRoleList((list) => list.map((r) => (r.id === saved.id ? saved : r)));

    // 机器有一个「本机启用角色」白名单：空 = 全部启用；非空时新角色必须显式加进去，
    // 否则角色绑了也播不了。空列表时不要调用——那样会把白名单收窄成只有当前角色。
    const mc = machineList.find((m) => m.id === machineId);
    if (mc && (mc.enabled_roles || []).length && !mc.enabled_roles.includes(saved.id)) {
      await api(`/api/machines/${encodeURIComponent(machineId)}/roles`, {
        method: 'POST',
        body: JSON.stringify({ role_id: saved.id, on: true }),
      });
      try { setMachineList(await api('/api/machines')); } catch { /* 刷新失败不影响绑定 */ }
    }
    return saved;
  };

  const refreshMachines = async () => {
    try {
      const list = await api('/api/machines');
      setMachineList(list);
      return list;
    } catch (e) { toastErr(e.message); return null; }
  };

  // 轮询同步日志：apply 是异步的，接口立即返回 ok，真正的结论在 kind=sync 日志里。
  const waitSyncLog = async (id, mid, since, timeoutMs) => {
    const deadline = Date.now() + timeoutMs;
    while (Date.now() < deadline) {
      await sleep(1200);
      let logs = [];
      try { logs = await api(`/api/logs?role_id=${encodeURIComponent(id)}&kind=sync`); } catch { continue; }
      const fresh = (logs || []).filter(
        (l) => l.machine_id === mid && l.time && new Date(l.time).getTime() >= since - 2000,
      );
      // 同一角色可能还绑了同机的其它 slot：优先取明细里带当前 slot 的那条。
      const hit = fresh.find((l) => String(l.detail || '').includes('slot ' + slot)) || fresh[0];
      if (hit) return hit;
    }
    return null;
  };

  const runSync = async (full) => {
    setSync({ mode: full ? 'full' : 'info', busy: true, ok: false, skipped: false, error: '', detail: '' });
    try {
      let role = persistedRole;
      if (!role) role = await saveRoleInfo();
      const bound = await persistBinding(role);
      const startedAt = Date.now();
      if (full) {
        let res;
        try {
          res = await api(`/api/roles/${encodeURIComponent(bound.id)}/sync`, {
            method: 'POST',
            body: JSON.stringify({ machine_id: machineId, slot: +slot, probe_voice: false }),
          });
        } catch (e) {
          // api() 把 {error:...} 当异常抛；NEED_BINDING 换成可读提示。
          if (String(e.message).includes('NEED_BINDING')) throw new Error(t('wizSyncNeedBinding'));
          throw e;
        }
        if (res && res.code === 'NEED_BINDING') throw new Error(t('wizSyncNeedBinding'));
        setSync({ mode: 'full', busy: false, ok: true, skipped: false, error: '', detail: t('wizSyncFullOk') });
        toastOk(t('synced'));
      } else {
        await api(`/api/roles/${encodeURIComponent(bound.id)}/apply`, {
          method: 'POST',
          body: JSON.stringify({ machine_id: machineId, slot: +slot }),
        });
        const hit = await waitSyncLog(bound.id, machineId, startedAt, 20000);
        if (hit && !hit.ok) throw new Error(hit.error || hit.detail || t('wizSyncFailed'));
        setSync({
          mode: 'info',
          busy: false,
          ok: true,
          skipped: false,
          error: '',
          detail: (hit && (hit.detail || hit.error)) || t('wizSyncQueued'),
        });
        toastOk(t('wizSyncOk'));
      }
      try {
        const list = await api('/api/roles');
        setRoleList(list);
        const fresh = list.find((r) => r.id === bound.id);
        if (fresh) { setRoleDetail(fresh); setPersistedRole(fresh); } // 克隆回填的 voice_id 要反映到"角色资源"上
      } catch { /* 列表刷新失败不影响下一步 */ }
    } catch (e) {
      setSync((s) => ({ ...s, busy: false, ok: false, error: e.message }));
      toastErr(e.message);
    }
  };

  // 音色、形象都没配时，同步步骤没有东西可推 —— 自动视为「跳过」，不用再手点「稍后再同步」。
  // 原 slot 模式本来就不推形象/音色，同样直接跳过。
  useEffect(() => {
    if (stepNum !== 3 || sync.busy || sync.ok || sync.skipped) return;
    if (slotVoiceVrm(selectedRole)) { setSync({ mode: 'skip', busy: false, ok: false, skipped: true, error: '', detail: t('wizSyncAutoSkipSlot') }); return; }
    if (hasVoice(selectedRole) || (selectedRole && selectedRole.local_vrm)) return;
    setSync({ mode: 'skip', busy: false, ok: false, skipped: true, error: '', detail: t('wizSyncAutoSkip') });
  }, [stepNum, selectedRole, sync.busy, sync.ok, sync.skipped]);

  const canNext = () => {
    // 第一步是补填 socialdata key（仅在没配置且没有浏览器登录态时出现）
    if (needSd && step === 0) return sdMode === 'browser' || !!sdKey.trim();
    if (stepNum === 0) return !!machineId;
    // 角色步：名称 + 人设（character_prompt）+ 账号名都必填，缺一个不能下一步
    if (stepNum === 1) {
      const info = roleChoice === 'new' ? newRole : roleEdit;
      const base = roleChoice === 'new' ? true : (!!roleDetail && !roleLoading);
      const hasTarget = !!(normUsername(info.username)
        || String(info.feed_target || '').trim()
        || String(info.live_url || '').trim());
      return !!(info.name.trim() && String(info.prompt || '').trim() && hasTarget) && base;
    }
    if (stepNum === 2) return !!roleId && slot > 0 && !occupied.has(slot);
    if (stepNum === 3) return sync.ok || sync.skipped;
    return true;
  };

  const next = async () => {
    // 数据源步：保存 key 成功后 needSd 变 false、步骤自动收起，step 0 变成「选择设备」。
    // 选「内置浏览器登录」时不需要保存任何东西——登录态就是数据源，直接进下一步。
    if (needSd && step === 0) {
      if (sdMode === 'browser') {
        setStep(step + 1);
        return;
      }
      setSaving(true);
      try {
        await api('/api/settings', { method: 'PUT', body: JSON.stringify({ socialdata_api_key: sdKey.trim() }) });
        setSd('ready');
        setSdKey('');
        toastOk(t('wizSdSaved'));
      } catch (e) { toastErr(e.message); } finally { setSaving(false); }
      return;
    }
    if (stepNum === 1) {
      setSaving(true);
      try {
        await saveRoleInfo();
        // 角色信息刚被改过：之前那次「同步成功」不再代表设备上的最新资料。
        setSync({ mode: '', busy: false, ok: false, skipped: false, error: '', detail: '' });
        setStep(step + 1);
      } catch (e) { toastErr(e.message); } finally { setSaving(false); }
      return;
    }
    if (stepNum === 2) {
      setSaving(true);
      try {
        await persistBinding(persistedRole || roleDetail);
        setStep(step + 1);
      } catch (e) { toastErr(e.message); } finally { setSaving(false); }
      return;
    }
    if (stepNum === 3) {
      if (!sync.ok && !sync.skipped) { toastErr(t('wizSyncNeed')); return; }
      setStep(step + 1);
      return;
    }
    setStep((s) => Math.min(s + 1, STEPS.length - 1));
  };

  const saveSdKey = async () => {
    if (!sdKey.trim()) return false;
    setSaving(true);
    try {
      await api('/api/settings', { method: 'PUT', body: JSON.stringify({ socialdata_api_key: sdKey.trim() }) });
      // 不在这里改 sd：这个按钮可能出现在后面的角色步骤里，改 sd 会让步骤索引整体前移。
      setSdKey('');
      toastOk(t('wizSdSaved'));
      return true;
    } catch (e) {
      toastErr(e.message);
      return false;
    } finally {
      setSaving(false);
    }
  };

  const finish = () => {
    toastOk(t('wizDoneToast'));
    onDone && onDone();
  };

  const openRole = () => {
    const id = persistedRole?.id || roleId;
    onClose && onClose();
    if (id) nav('/roles/' + encodeURIComponent(id));
  };

  const stepState = (i) => (i < step ? 'done' : i === step ? 'now' : '');
  // 同步进行中不允许改目标（返回上一步 / 点侧边栏），否则回来的结果对应的是旧槽位。
  const jumpTo = (i) => { if (!sync.busy && i < step) setStep(i); };

  return (
    <dialog ref={dlgRef} className="wiz-dialog"
      onCancel={(e) => { e.preventDefault(); if (confirm(t('wizCloseConfirm'))) onClose && onClose(); }}>
      <div className="wiz">
        <div className="wiz-side">
          <div className="wiz-side-title">{t('wizTitle')}</div>
          {STEPS.map((k, i) => (
            <button key={k} type="button" className={'wiz-step ' + stepState(i)}
              onClick={() => jumpTo(i)} disabled={i > step || (sync.busy && i < step)}>
              <span className="idx">{i < step ? '✓' : i + 1}</span>
              <span className="label">{t(k)}</span>
            </button>
          ))}
        </div>

        <div className="wiz-main">
          <button className="ghost wiz-close" title={t('cancel')}
            onClick={() => { if (confirm(t('wizCloseConfirm'))) onClose && onClose(); }}>✕</button>

          <div className="wiz-content" key={step}>
            {needSd && step === 0 && (
              <KeyStep
                sdKey={sdKey} setSdKey={setSdKey}
                mode={sdMode} setMode={setSdMode}
                browser={browser}
                onOpenSettings={() => { onClose && onClose(); nav('/settings#feed/browser-login'); }}
              />
            )}

            {stepNum === 0 && (
              <MachineStep
                machines={machineList} machineId={machineId} setMachineId={setMachineId}
                adding={addingMachine} setAdding={setAddingMachine}
                onRefresh={refreshMachines}
                onCreated={(mc) => {
                  setMachineList((list) => (list.some((m) => m.id === mc.id) ? list : [...list, mc]));
                  setMachineId(mc.id);
                  setAddingMachine(false);
                }}
              />
            )}

            {stepNum === 1 && (
              <RoleStep
                roleList={roleList} roleChoice={roleChoice} setRoleChoice={setRoleChoice}
                roleDetail={roleDetail} roleLoading={roleLoading}
                roleEdit={roleEdit} setRoleEdit={setRoleEdit}
                newRole={newRole} setNewRole={setNewRole}
                platformLabel={platformLabel}
                sdMode={sdMode} setSdMode={setSdMode}
                sdKey={sdKey} setSdKey={setSdKey} saveSdKey={saveSdKey}
                browser={browser} browserLogged={browserLogged} saving={saving}
                onOpenBrowserSettings={() => { onClose && onClose(); nav('/settings#feed/browser-login'); }}
              />
            )}

            {stepNum === 2 && (
              <>
                <h4 className="wiz-title">{t('wizBindTitle')}</h4>
                <p className="wiz-sub">{t('wizBindSub')}</p>
                <div className="wiz-summary">
                  <SummaryRow label={t('homeDevices')} value={machine ? (machine.name || machine.addr) : '-'}
                    extra={machine ? (machine.creds ? t('pairedAt') : t('notPairedBold')) : ''} />
                  <SummaryRow label={t('roles')} value={selectedRole ? (selectedRole.name || selectedRole.id) : '-'}
                    extra={selectedRole ? platformLabel(selectedRole.platform) : ''} />
                </div>
                <label className="field" style={{ maxWidth: 260 }}>
                  {t('slot')}
                  <select value={slot} onChange={(e) => setSlot(+e.target.value)}>
                    {SLOTS.map((s) => (
                      <option key={s} value={s} disabled={occupied.has(s)}>
                        slot {s}{occupied.has(s) ? ' · ' + t('wizSlotTaken').replace('%s', occupied.get(s).name || occupied.get(s).id) : ''}
                      </option>
                    ))}
                  </select>
                  <span className="hint">{t('wizSlotHint')}</span>
                </label>
                {occupied.has(slot) ? <p className="err-banner">{t('wizSlotConflict')}</p> : null}
              </>
            )}

            {stepNum === 3 && (
              <>
                <h4 className="wiz-title">{t('wizSyncTitle')}</h4>
                <p className="wiz-sub">{t('wizSyncSub')}</p>
                <div className="wiz-summary">
                  <SummaryRow label={t('wizTarget')} value={`${machine ? (machine.name || machine.addr) : '-'} · slot ${slot}`} />
                  <SummaryRow label={t('roles')} value={selectedRole ? (selectedRole.name || selectedRole.id) : '-'}
                    extra={selectedRole ? (selectedRole.username ? platformLabel(selectedRole.platform) + '/@' + selectedRole.username : platformLabel(selectedRole.platform)) : ''} />
                  <SummaryRow label={t('wizResource')}
                    value={slotVoiceVrm(selectedRole)
                      ? t('assetSourceSlotBadge')
                      : `${hasVoice(selectedRole) ? t('homeHasVoice') : t('homeNoVoice')} · ${selectedRole?.local_vrm ? t('homeHasVrm') : t('homeNoVrm')}`} />
                </div>

                {!slotVoiceVrm(selectedRole) && !hasVoice(selectedRole) && (
                  <p className="hint" style={{ marginBottom: 10 }}>ℹ️ {t('wizNoVoiceHint')}</p>
                )}

                <div className="wiz-actions">
                  <button onClick={() => runSync(false)} disabled={sync.busy}>
                    {sync.busy && sync.mode === 'info' ? t('wizSyncRunning') : `⚡ ${t('wizSyncInfo')}`}
                  </button>
                  <button className="secondary" onClick={() => runSync(true)} disabled={sync.busy || !hasVoice(selectedRole)}
                    title={t('wizSyncFullHint')}>
                    {sync.busy && sync.mode === 'full' ? t('wizSyncRunning') : t('wizSyncFull')}
                  </button>
                  {!sync.busy && !sync.ok && !sync.skipped ? (
                    <button className="ghost" onClick={() => setSync({ mode: 'skip', busy: false, ok: false, skipped: true, error: '', detail: '' })}>
                      {t('wizSyncSkip')}
                    </button>
                  ) : null}
                </div>

                {sync.busy && <p className="hint">⏳ {sync.mode === 'full' ? t('wizSyncFullRunning') : t('wizSyncWaiting')}</p>}
                {sync.ok && <p className="wiz-ok">✓ {sync.mode === 'full' ? t('wizSyncFullOk') : t('wizSyncOk')}{sync.detail ? ' · ' + sync.detail : ''}</p>}
                {sync.skipped && <p className="hint">⏭ {t('wizSyncSkipped')}</p>}
                {sync.error && <p className="err-banner">{sync.error}</p>}
                <p className="hint" style={{ marginTop: 10 }}>{t('wizSyncLogHint')}</p>
              </>
            )}

            {stepNum === 4 && (
              <TweetPickStep
                role={selectedRole} machine={machine} slot={slot}
                sdReady={!needSd}
                onFinish={finish} onOpenRole={openRole}
                syncInfo={sync.ok ? (sync.mode === 'full' ? t('wizSyncFullOk') : t('wizSyncOk')) : t('wizSyncSkipped')}
              />
            )}
          </div>

          <div className="wiz-foot">
            {stepNum < 4 && (
              <button className="secondary" onClick={() => { if (confirm(t('wizCloseConfirm'))) onClose && onClose(); }}>
                {t('cancel')}
              </button>
            )}
            <span style={{ flex: 1 }}></span>
            {stepNum > 0 && stepNum < 4 && (
              <button className="secondary" disabled={sync.busy} onClick={() => setStep(step - 1)}>{t('wizPrev')}</button>
            )}
            {stepNum < 4 && (
              <button onClick={next} disabled={!canNext() || saving || (stepNum === 3 && sync.busy)}>
                {saving ? t('wizSaving') : t('wizNext')}
              </button>
            )}
          </div>
        </div>
      </div>
    </dialog>
  );
}
