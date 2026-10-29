import { useEffect, useRef, useState } from 'react';
import { api } from '../../api.js';
import { t } from '../../i18n.js';
import { notifyMessageQueued } from '../../messageQueue.js';
import { useToastErr, useToastOk } from '../../toast.jsx';
import { getProbeVoice, setProbeVoice } from '../../voiceProbePref.js';
import { putRole, usesCustomVoiceVrm } from './shared.js';

export default function StatusTab({ role, machines, onSaved, goTab }) {
  const toastOk = useToastOk();
  const toastErr = useToastErr();
  const [bindings, setBindings] = useState(role.bindings || []);
  const [addM, setAddM] = useState('');
  const [addSlot, setAddSlot] = useState(1);
  const [allRoles, setAllRoles] = useState([]);   // 全部角色：计算各机器槽位被谁占用（添加绑定时禁用+标注）
  const [voiceGroups, setVoiceGroups] = useState({}); // machine_id -> [{group_key, voice_ids, clone_voice_ids}]
  const [expanded, setExpanded] = useState({});       // "machine:slot" -> bool
  const [voiceCheck, setVoiceCheck] = useState({});   // "machine:slot" -> {loading, result, error}
  const [cloneTasks, setCloneTasks] = useState([]);   // voice_clone 后台任务（running → 按钮变灰 + 「克隆中」）

  // 轮询克隆任务：有进行中的克隆时 4s，空闲时 20s 兜底（新克隆由其它端触发也能看到）
  const anyCloning = cloneTasks.some((t) => t.status === 'running');
  useEffect(() => {
    let alive = true;
    const poll = async () => {
      try {
        const ts = await api('/api/clone/tasks');
        if (alive) setCloneTasks((Array.isArray(ts) ? ts : []).filter(Boolean));
      } catch { /* 忽略瞬时错误，下轮再拉 */ }
    };
    poll();
    const iv = setInterval(poll, anyCloning ? 4000 : 20000);
    return () => { alive = false; clearInterval(iv); };
  }, [anyCloning]);

  // role prop 刷新（onSaved 后父级重新拉取）→ 同步本地绑定表：
  // 否则克隆/检查回填的 voice_id 不会显示，行内一直是「未记录」。
  useEffect(() => { setBindings(role.bindings || []); }, [role.bindings]);

  // 任务结束（running → done/failed）：刷新角色回填 voice_id，并把结果写到该绑定行内 + 提示
  const prevRunningIds = useRef(null);
  useEffect(() => {
    const prev = prevRunningIds.current;
    prevRunningIds.current = cloneTasks.filter((t) => t.status === 'running').map((t) => t.id);
    if (!prev) return; // 首次加载不提示
    for (const id of prev) {
      const task = cloneTasks.find((x) => x.id === id);
      if (!task || task.status === 'running') continue;
      const k = task.machine_id + ':' + task.slot;
      onSaved && onSaved(); // 绑定 voice_id 已回填，刷新角色
      if (task.status === 'done') {
        // 行内持久展示克隆结果（不只看一闪而过的 toast）
        setVoiceCheck((s) => ({ ...s, [k]: { result: { recorded: true, exists: true, voice_id: task.voice_id, group_key: task.group_key } } }));
        toastOk(t('clonedHere') + ' voice_id=' + task.voice_id);
      } else {
        // 失败：重克隆失败时后端会恢复原记录（task.voice_id 即保留的旧音色）
        const kept = task.voice_id ? ' · ' + t('voiceKept') + ' voice_id=' + task.voice_id : '';
        setVoiceCheck((s) => ({ ...s, [k]: { error: (task.error || 'clone failed') + kept } }));
        toastErr((task.role_name ? task.role_name + ': ' : '') + (task.error || 'clone failed') + kept);
      }
    }
  }, [cloneTasks]);

  // 该绑定正在克隆的任务 / 该机器任意进行中的克隆（克隆槽 21-25 是设备级资源，整机互斥）
  const runningTask = (b) => cloneTasks.find((t) => t.status === 'running' && t.machine_id === b.machine_id && +t.slot === +b.slot);
  const machineTask = (b) => cloneTasks.find((t) => t.status === 'running' && t.machine_id === b.machine_id);

  useEffect(() => {
    let alive = true;
    (async () => {
      const out = {};
      await Promise.all((machines || []).map(async (m) => {
        if (!m.creds) return;
        try {
          const raw = await api(`/api/machines/${encodeURIComponent(m.id)}/voices`);
          const groups = Array.isArray(raw) ? raw : (raw && raw.groups) || [];
          out[m.id] = groups.filter((g) => g.group_key);
        } catch { /* 设备离线时下拉退化为自由文本 */ }
      }));
      if (alive) setVoiceGroups(out);
    })();
    return () => { alive = false; };
  }, [machines]);

  const save = async (next) => {
    setBindings(next);
    try {
      await putRole(role, { bindings: next });
      toastOk(t('saved'));
      onSaved && onSaved();
    } catch (e) { toastErr(e.message); }
  };
  const setBindingField = async (i, field, value) => {
    const next = bindings.map((b, j) => (j === i ? { ...b, [field]: value } : b));
    setBindings(next);
    try {
      await putRole(role, { bindings: next });
      toastOk(t('saved'));
      onSaved && onSaved();
      return true;
    } catch (e) { toastErr(e.message); return false; }
  };
  const toggle = (i) => {
    const next = bindings.map((b, j) => (j === i ? { ...b, enabled: !b.enabled } : b));
    save(next);
  };
  const del = (i) => {
    if (!confirm(t('deleteConfirm'))) return;
    save(bindings.filter((_, j) => j !== i));
  };
  const add = () => {
    if (!addM || +addSlot <= 0) { toastErr(t('slotBindingTitle')); return; }
    if (bindings.some((b) => b.machine_id === addM && b.slot === +addSlot)) return;
    if (occupied.has(+addSlot)) { toastErr(t('wizSlotConflict')); return; }
    // 新机器/槽位：voice_id / group_key 保持为空，由该机器自己 clone 后记录
    save([...bindings, { machine_id: addM, slot: +addSlot, enabled: true, voice_id: '', group_key: '' }]);
  };
  const mInfo = (id) => machines.find((m) => m.id === id);
  const mLabel = (id) => { const m = mInfo(id); return m ? (m.name || m.addr) : id; };
  const bKey = (b) => b.machine_id + ':' + b.slot;

  // 槽位占用表（同机同槽只能绑一个角色，服务端硬约束）：
  // 列出全部角色的绑定（含停用的——停用仍占槽），排除本角色自己的绑定。
  // 供「添加绑定」的槽位下拉用：被占用槽位 disabled 并标注占用者，从源头挡住 400 槽位冲突。
  useEffect(() => {
    let alive = true;
    api('/api/roles').then((rs) => { if (alive) setAllRoles(Array.isArray(rs) ? rs : []); }).catch(() => {});
    return () => { alive = false; };
  }, []);
  const occupiedFor = (machineId) => {
    const out = new Map();
    if (!machineId) return out;
    for (const r of allRoles) {
      if (r.id === role.id) continue; // 自己的绑定不算冲突（add() 里另有去重）
      for (const b of r.bindings || []) {
        if (b.machine_id === machineId && b.slot > 0) out.set(b.slot, r);
      }
    }
    return out;
  };
  const occupied = occupiedFor(addM);
  // 换目标机器时自动选第一个空闲槽位（全占则保持原值，由禁用态提示）
  const changeAddM = (mid) => {
    setAddM(mid);
    const occ = occupiedFor(mid);
    const free = [1, 2, 3, 4, 5].find((s) => !occ.has(s));
    if (free) setAddSlot(free);
  };

  const toggleExpand = (b) => setExpanded((s) => ({ ...s, [bKey(b)]: !s[bKey(b)] }));

  // 检查该绑定的 voice_id 在设备上是否可用（machine/slot 1:1）
  const checkVoice = async (b) => {
    const k = bKey(b);
    setVoiceCheck((s) => ({ ...s, [k]: { loading: true } }));
    try {
      const res = await api(`/api/roles/${encodeURIComponent(role.id)}/voice?machine_id=${encodeURIComponent(b.machine_id)}&slot=${b.slot}`);
      setVoiceCheck((s) => ({ ...s, [k]: { result: res } }));
    } catch (e) {
      setVoiceCheck((s) => ({ ...s, [k]: { error: e.message } }));
    }
  };

  // 作废该绑定的 voice 记录并重新 clone。后台异步执行（分钟级）：
  // 立即返回任务记录，克隆期间按钮变灰 + 显示「克隆中」，结束由任务轮询 effect 提示结果。
  // 注意：api() 对 JSON 错误体（如 409 CLONE_BUSY）不抛异常而是原样返回，需显式判断。
  const recloneVoice = async (b) => {
    if (!confirm(t('recloneConfirm'))) return;
    const k = bKey(b);
    setVoiceCheck((s) => ({ ...s, [k]: { ...(s[k] || {}), loading: false, error: null, recloning: true } }));
    const stopRecloning = () => setVoiceCheck((s) => { const c = { ...(s[k] || {}) }; delete c.recloning; return { ...s, [k]: c }; });
    try {
      const res = await api(`/api/roles/${encodeURIComponent(role.id)}/voice/reclone`, { method: 'POST', body: JSON.stringify({ machine_id: b.machine_id, slot: b.slot }) });
      if (!res || res.error || !res.task) {
        stopRecloning();
        setVoiceCheck((s) => ({ ...s, [k]: { ...((s[k] || {})), error: (res && res.error) || 'reclone failed' } }));
        return;
      }
      setCloneTasks((ts) => (ts.some((x) => x && x.id === res.task.id) ? ts : [res.task, ...ts]));
      stopRecloning();
    } catch (e) {
      stopRecloning();
      setVoiceCheck((s) => ({ ...s, [k]: { ...((s[k] || {})), error: e.message } }));
    }
  };

  // 试听：该绑定 voice 在对应机器上 tts_audition（设备外放）
  const auditionVoice = async (b) => {
    const k = bKey(b);
    setVoiceCheck((s) => ({ ...s, [k]: { loading: true } }));
    try {
      const res = await api(`/api/roles/${role.id}/voice/audition`, { method: 'POST', body: JSON.stringify({ machine_id: b.machine_id, slot: b.slot }) });
      if (res && res.error) {
        setVoiceCheck((s) => ({ ...s, [k]: { error: res.error } }));
        return;
      }
      setVoiceCheck((s) => ({ ...s, [k]: { result: { ...(voiceCheck[k]?.result || {}), audition: true, voice_id: res.voice_id } } }));
      toastOk(t('auditionSent') + ' (' + (b.voice_id || res.voice_id) + ')');
    } catch (e) {
      setVoiceCheck((s) => ({ ...s, [k]: { error: e.message } }));
    }
  };

  // 上传样本并在这台机器上 clone（machine/slot 1:1，成功后回填该绑定）
  const cloneHere = async (b, file) => {
    if (!file) { toastErr(t('pickAudioFirst')); return; }
    const k = bKey(b);
    setVoiceCheck((s) => ({ ...s, [k]: { loading: true } }));
    try {
      const fd = new FormData();
      fd.append('file', file);
      fd.append('machine_id', b.machine_id);
      fd.append('slot', b.slot);
      const res = await api(`/api/roles/${role.id}/clone`, { method: 'POST', body: fd });
      if (res && (res.error || !res.voice_id)) {
        setVoiceCheck((s) => ({ ...s, [k]: { error: (res && res.error) || 'clone failed' } }));
        return;
      }
      setVoiceCheck((s) => ({ ...s, [k]: { result: { recorded: true, exists: true, voice_id: res.voice_id, group_key: res.group_key } } }));
      toastOk(t('clonedHere') + ' voice_id=' + res.voice_id);
      onSaved && onSaved();
    } catch (e) {
      setVoiceCheck((s) => ({ ...s, [k]: { error: e.message } }));
    }
  };

  // group_key 下拉：设备分组 + 当前已存值（可能不在分组列表里）；支持自定义输入
  const GroupKeySelect = ({ b, i }) => {
    const groups = voiceGroups[b.machine_id] || [];
    const opts = groups.some((g) => g.group_key === b.group_key) || !b.group_key
      ? groups.map((g) => g.group_key)
      : [b.group_key, ...groups.map((g) => g.group_key)];
    return (
      <span style={{ display: 'inline-flex', gap: 4, alignItems: 'center' }}>
        <select
          className="mini" style={{ maxWidth: 260 }}
          value={b.group_key || ''}
          onChange={(e) => {
            const g = e.target.value;
            // 换组后原 voice_id 大概率不属于新组 → 一并清掉，让 voice 下拉跟着新组刷新
            const ids = voicesOfGroup(b.machine_id, g);
            const keepVoice = g && b.voice_id && ids.includes(b.voice_id);
            const next = bindings.map((x, j) => (j === i ? { ...x, group_key: g, ...(keepVoice ? {} : { voice_id: '' }) } : x));
            save(next);
          }}
          title={t('groupKeyHint')}
        >
          <option value="">{t('voiceAutoClone')}</option>
          {opts.map((g) => (
            <option key={g} value={g}>{mLabel(b.machine_id)} / slot {b.slot} · {g}</option>
          ))}
        </select>
        <input
          className="mini" style={{ width: 150 }} placeholder={t('customGroupKey')}
          defaultValue={b.group_key || ''}
          onKeyDown={(e) => { if (e.key === 'Enter') setBindingField(i, 'group_key', e.target.value.trim()); }}
          onBlur={(e) => { const v = e.target.value.trim(); if (v !== (b.group_key || '')) setBindingField(i, 'group_key', v); }}
        />
      </span>
    );
  };

  // 某机器某分组下的可用 voice_id 列表（voice_ids + clone_voice_ids 去重）
  const voicesOfGroup = (machineID, groupKey) => {
    const g = (voiceGroups[machineID] || []).find((x) => x.group_key === groupKey);
    if (!g) return [];
    return [...new Set([...(g.voice_ids || []), ...(g.clone_voice_ids || [])])];
  };

  // voice_id 下拉：选项来自所选 group（通过 group 选择 voice）；选中即保存到该绑定并立即试听
  const VoiceIDSelect = ({ b, i }) => {
    const ids = voicesOfGroup(b.machine_id, b.group_key);
    const opts = ids.includes(b.voice_id) || !b.voice_id ? ids : [b.voice_id, ...ids];
    const noGroup = !b.group_key || !ids.length;
    return (
      <span style={{ display: 'inline-flex', gap: 4, alignItems: 'center' }}>
        <select
          className="mini" style={{ maxWidth: 220 }} disabled={noGroup && !b.voice_id}
          value={b.voice_id || ''}
          title={noGroup ? t('voiceIdPickGroup') : t('voiceIdHint')}
          onChange={async (e) => {
            const v = e.target.value;
            if (!v) return;
            const ok = await setBindingField(i, 'voice_id', v);
            if (ok) auditionVoice({ ...b, voice_id: v }); // 选完即试听
          }}
        >
          <option value="">{noGroup ? t('voiceAutoClone') : t('selectVoice')}</option>
          {opts.map((v) => (
            <option key={v} value={v}>{mLabel(b.machine_id)} / slot {b.slot} · voice {v}</option>
          ))}
        </select>
        <input
          className="mini" style={{ width: 110 }} placeholder={t('customVoiceId')}
          defaultValue={b.voice_id || ''}
          onKeyDown={(e) => { if (e.key === 'Enter') setBindingField(i, 'voice_id', e.target.value.trim()); }}
          onBlur={(e) => { const v = e.target.value.trim(); if (v !== (b.voice_id || '')) setBindingField(i, 'voice_id', v); }}
        />
      </span>
    );
  };

  return (
    <div className="card" style={{ maxWidth: 900 }}>
      <p className="hint" style={{ marginTop: 0 }}>{t('bindingsHint')}</p>
      <table>
        <thead><tr><th></th><th></th><th>{t('bindMachine')}</th><th>{t('slot')}</th><th>{t('statusCol')}</th><th></th></tr></thead>
        <tbody>
          {bindings.length ? bindings.flatMap((b, i) => {
            const m = mInfo(b.machine_id);
            const k = bKey(b);
            const vc = voiceCheck[k] || {};
            const isOpen = !!expanded[k];
            const rows = [
              <tr key={k}>
                <td><input type="checkbox" checked={!!b.enabled} onChange={() => toggle(i)} title={t('bindingEnable')} /></td>
                <td><button className="mini ghost" onClick={() => toggleExpand(b)} title={t('expandVoice')}>{isOpen ? '▾' : '▸'}</button></td>
                <td><b>{m ? (m.name || m.addr) : b.machine_id}</b></td>
                <td>slot {b.slot}</td>
                <td>{m && m.creds ? <span className="badge ok">{t('pairedAt')}</span> : <span className="badge warn">{t('notPaired')}</span>}</td>
                <td><button className="mini danger" onClick={() => del(i)}>{t('del')}</button></td>
              </tr>,
            ];
            if (isOpen) {
              rows.push(
                <tr key={k + ':x'}>
                  <td></td>
                  <td colSpan="5" style={{ background: 'var(--bg-soft)' }}>
                    <div className="form-grid" style={{ marginTop: 8 }}>
                      <label className="field">voice_id<VoiceIDSelect b={b} i={i} /></label>
                      <label className="field">group_key<GroupKeySelect b={b} i={i} /></label>
                    </div>
                    {!usesCustomVoiceVrm(role) && (
                      <p className="hint" style={{ margin: '6px 0 0' }}>ℹ️ {t('assetSourceSlotBindingHint')}</p>
                    )}
                    <div className="row" style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
                      {(() => {
                        const mt = machineTask(b);           // 该机器任意进行中的克隆（设备级互斥）
                        const own = runningTask(b);          // 本绑定的克隆任务
                        const secs = mt ? Math.max(0, Math.round((Date.now() - new Date(mt.started_at).getTime()) / 1000)) : 0;
                        return (
                          <>
                            <button className="mini" onClick={() => checkVoice(b)} disabled={vc.loading || !!mt || !!vc.recloning}>🔍 {t('checkVoiceId')}</button>
                            <button className="mini" onClick={() => auditionVoice(b)} disabled={vc.loading || !!mt || !b.voice_id} title={b.voice_id ? '' : t('voiceNotRecorded')}>🔊 {t('auditionVoice')}</button>
                            <button className="mini" onClick={() => recloneVoice(b)} disabled={vc.loading || !!mt || !!vc.recloning} title={mt && !own ? t('cloningOther') : ''}>♻️ {t('recloneVoice')}</button>
                            <label className="mini" style={{ display: 'inline-flex', gap: 4, alignItems: 'center', cursor: mt ? 'not-allowed' : 'pointer', opacity: mt ? 0.5 : 1 }}>
                              🎙 {t('cloneHere')}
                              <input type="file" accept="audio/*" style={{ display: 'none' }} disabled={!!mt}
                                onChange={(e) => { const f = e.target.files[0]; e.target.value = ''; if (f) cloneHere(b, f); }} />
                            </label>
                            {vc.loading && <span className="hint">…</span>}
                            {own && <span className="badge warn">⏳ {t('cloning')} · {secs}s</span>}
                            {mt && !own && <span className="badge warn" title={t('cloningOther')}>⏳ {t('cloningOther')}</span>}
                          </>
                        );
                      })()}
                      {vc.error && <span className="badge bad">{vc.error}</span>}
                      {vc.result && (
                        vc.result.recorded
                          ? (vc.result.exists
                              ? <span className="badge ok">✓ voice_id={vc.result.voice_id} · {vc.result.group_key || '-'} {t('voiceUsable')}</span>
                              : <span className="badge bad">✗ voice_id={vc.result.voice_id} {t('voiceMissing')}</span>)
                          : <span className="badge mut">{t('voiceNotRecorded')}</span>
                      )}
                    </div>
                  </td>
                </tr>,
              );
            }
            return rows;
          }) : <tr><td colSpan="6" className="hint">{t('noBindings')}</td></tr>}
        </tbody>
      </table>
      <div className="card-actions" style={{ justifyContent: 'flex-start', flexWrap: 'wrap', gap: 8, alignItems: 'center' }}>
        <select className="mini" value={addM} onChange={(e) => changeAddM(e.target.value)}>
          <option value="">-- {t('bindMachine')} --</option>
          {machines.map((m) => <option key={m.id} value={m.id}>{m.name || m.addr}</option>)}
        </select>
        {/* 槽位下拉：被其它角色占用的槽位禁用并标注占用者（同机同槽不能绑两个角色） */}
        <select className="mini" value={addSlot} onChange={(e) => setAddSlot(+e.target.value)} disabled={!addM} style={!addM ? { opacity: 0.5 } : undefined}>
          {[1, 2, 3, 4, 5].map((s) => (
            <option key={s} value={s} disabled={occupied.has(s)}>
              slot {s}{occupied.has(s) ? ' · ' + t('wizSlotTaken').replace('%s', occupied.get(s).name || occupied.get(s).id) : ''}
            </option>
          ))}
        </select>
        <button className="mini" onClick={add} disabled={!addM || occupied.has(+addSlot)}>＋ {t('bindingAdd')}</button>
      </div>
      {addM && occupied.has(+addSlot) ? <p className="err-banner">{t('wizSlotConflict')}</p> : null}
      <p className="hint" style={{ marginTop: 6 }}>{t('wizSlotHint')}</p>
      <p className="hint" style={{ marginTop: 6 }}>{t('bindingVoiceHint')}</p>
      {role.monitor_way === 'callback' && !role.monitor_id && (
        <p className="hint" style={{ marginTop: 8 }}>ℹ️ {t('callbackNoMonitorHint')}</p>
      )}
      <SyncStatusPanel role={role} anyCloning={anyCloning} machines={machines}
        onCloneStarted={(task) => setCloneTasks((ts) => (ts.some((x) => x && x.id === task.id) ? ts : [task, ...ts]))} />
    </div>
  );
}

// Codie 同步状态：最近一次失败原因（醒目展示）+ Check 校验修复按钮。
// Check&Fix 分段推进（配置→VRM→声音各自完成后立即 PATCH 设备），总结论「check done」
// 只在全部阶段结束时出现 —— 前端以它为准停止等待、展示 ok。
// 旁边三个独立按钮：PATCH设置 / VRM上传 / 强制VOICE_CLONE。
// 四个操作的状态一律进全局 MessageQueue 面板（不再弹成功 toast），这里只保留发起时的即时报错。
function SyncStatusPanel({ role, anyCloning, machines, onCloneStarted }) {
  const toastErr = useToastErr();
  const [logs, setLogs] = useState(null);
  const [report, setReport] = useState(null);
  const [busy, setBusy] = useState(false);
  const [btnBusy, setBtnBusy] = useState(''); // apply | vrm | clone（三按钮各自进行中）
  // 机器勾选：默认全选，只有勾选的机器才执行 Check / PATCH / VRM / 克隆操作。
  // 勾选结果按角色记在 localStorage，刷新/重进页面不丢失（避免刷新后又全量重跑）。
  const [sel, setSel] = useState(null); // null=未初始化（初始化前视为全选）
  // 试听验证：先确认已记录的克隆能播再决定是否重克隆（避免白克隆；代价是设备外放一小段）
  const [probe, setProbe] = useState(getProbeVoice());
  // Check 首次确认弹窗：每个角色（每种 Voice/VRM 模式）只在第一次点 Check 时弹一次，
  // 之后记住不再打扰。非默认（角色自定义）时弹窗明确提示会覆盖设备上的角色与声音。
  const customMode = usesCustomVoiceVrm(role);
  const checkConfirmKey = 'trp_checkfirst_' + role.id + '_' + (customMode ? 'custom' : 'slot');
  const [confirmCheck, setConfirmCheck] = useState(false);

  // 角色涉及的全部机器（bindings 去重；显示用 machines 的名字，找不到就显示 id）
  const bindMachines = [...new Set((role.bindings || []).map((b) => b.machine_id).filter(Boolean))];
  const mName = (id) => { const m = (machines || []).find((x) => x.id === id); return m ? (m.name || m.addr) : id; };
  const selKey = 'trp_syncsel_' + role.id;
  const saveSel = (set) => { try { localStorage.setItem(selKey, JSON.stringify([...set])); } catch { /* 忽略隐私模式 */ } };
  // 进入页面 / 切换角色时恢复上次勾选；没存过才默认全选
  useEffect(() => {
    try {
      const raw = localStorage.getItem('trp_syncsel_' + role.id);
      if (raw) setSel(new Set(JSON.parse(raw)));
      else setSel(null); // null = 全选
    } catch { setSel(null); }
  }, [role.id]);
  const checked = (id) => (sel ? sel.has(id) : true); // 未初始化视为全选
  const toggleM = (id) => setSel((s) => {
    const next = new Set(s || bindMachines);
    if (next.has(id)) next.delete(id); else next.add(id);
    saveSel(next);
    return next;
  });
  const selBindings = (role.bindings || []).filter((b) => checked(b.machine_id));

  const load = async () => {
    try {
      const all = await api(`/api/logs?role_id=${encodeURIComponent(role.id)}&kind=sync`);
      setLogs(all || []);
    } catch { setLogs([]); }
  };
  useEffect(() => { load(); }, [role.id]);

  // 最近一条 sync 成功即隐藏失败横幅（失败只在该失败仍是最新状态时展示）
  const logsSorted = logs || [];
  const lastFail = logsSorted.length && !logsSorted[0].ok ? logsSorted[0] : null;
  const runCheck = async () => {
    if (!selBindings.length) { toastErr(t('noMachineSelected')); return; }
    setBusy(true);
    try {
      // 只对勾选的机器逐个下发（后端支持 machine_id 过滤）
      for (const id of selBindings.map((b) => b.machine_id)) {
        await api(`/api/roles/${role.id}/check`, { method: 'POST', body: JSON.stringify({ machine_id: id, probe_voice: probe }) });
      }
      // 异步校验：状态进 MessageQueue 面板跟踪；这里继续轮询日志直到「check done」
      // （全部阶段结束的最终结论；中途的 check[1/3]/[2/3] 阶段日志不算完成）以解除按钮 busy。
      // 克隆分钟级,上限 10 分钟。
      let n = 0;
      const iv = setInterval(async () => {
        n++;
        await load();
        const latest = (logs || [])[0];
        const done = (latest && ((latest.detail || '') + (latest.error || '')).includes('check done'));
        if (done || n > 200) { clearInterval(iv); setBusy(false); }
      }, 3000);
    } catch (e) { toastErr(e.message); setBusy(false); }
    notifyMessageQueued();
  };
  // Check 入口：首次点击先确认（非默认模式会覆盖设备角色/声音），确认过就直接跑。
  const check = () => {
    if (!selBindings.length) { toastErr(t('noMachineSelected')); return; }
    let seen = false;
    try { seen = localStorage.getItem(checkConfirmKey) === '1'; } catch { /* 隐私模式：每次都问 */ }
    if (seen) { runCheck(); return; }
    setConfirmCheck(true);
  };
  const confirmCheckRun = () => {
    try { localStorage.setItem(checkConfirmKey, '1'); } catch { /* 忽略隐私模式 */ }
    setConfirmCheck(false);
    runCheck();
  };

  // PATCH设置：仅下发基础配置（prompt/name/…+已记录 voice），不等 VRM/克隆
  const applyConfig = async () => {
    if (!selBindings.length) { toastErr(t('noMachineSelected')); return; }
    setBtnBusy('apply');
    const errs = [];
    for (const id of selBindings.map((b) => b.machine_id)) {
      try {
        const res = await api(`/api/roles/${role.id}/apply`, { method: 'POST', body: JSON.stringify({ machine_id: id }) });
        if (res && res.error) errs.push(mName(id).slice(0, 16) + ': ' + res.error);
      } catch (e) { errs.push(mName(id).slice(0, 16) + ': ' + e.message); }
    }
    if (errs.length) toastErr(errs.join('；'));
    else notifyMessageQueued(); // PATCH设置状态进 MessageQueue，不再弹 toast
    setTimeout(load, 1500);
    setBtnBusy('');
  };
  // VRM上传：仅上传激活的本地 VRM（完成自动 apply）
  const pushVrm = async () => {
    if (!selBindings.length) { toastErr(t('noMachineSelected')); return; }
    setBtnBusy('vrm');
    const errs = [];
    for (const id of selBindings.map((b) => b.machine_id)) {
      try {
        const res = await api(`/api/roles/${role.id}/vrm/push`, { method: 'POST', body: JSON.stringify({ machine_id: id }) });
        if (res && res.error) errs.push(mName(id).slice(0, 16) + ': ' + res.error);
      } catch (e) { errs.push(mName(id).slice(0, 16) + ': ' + e.message); }
    }
    if (errs.length) toastErr(errs.join('；'));
    else notifyMessageQueued(); // VRM上传状态进 MessageQueue，不再弹 toast
    setTimeout(load, 1500);
    setBtnBusy('');
  };
  // 强制VOICE_CLONE：逐绑定触发重新克隆（后台任务：克隆中按钮变灰 + 「克隆中」徽标）
  const forceClone = async () => {
    if (!selBindings.length) { toastErr(t('noMachineSelected')); return; }
    if (!confirm(t('recloneConfirmAll'))) return;
    setBtnBusy('clone');
    const errs = [];
    for (const b of selBindings) {
      if (!b.enabled) continue;
      try {
        const res = await api(`/api/roles/${encodeURIComponent(role.id)}/voice/reclone`,
          { method: 'POST', body: JSON.stringify({ machine_id: b.machine_id, slot: b.slot }) });
        if (res && res.error) errs.push((b.machine_id || '').slice(0, 12) + ': ' + res.error);
        else if (res && res.task) onCloneStarted && onCloneStarted(res.task);
      } catch (e) { errs.push((b.machine_id || '').slice(0, 12) + ': ' + e.message); }
    }
    if (errs.length) toastErr(errs.join('；'));
    else notifyMessageQueued(); // 克隆任务进 MessageQueue（面板里跟踪 running→done/failed）
    setBtnBusy('');
  };

  // 前置资源：角色自定义模式下没有本地 VRM / 语音样本时，同步类按钮点了也没用 —— 变灰并引导去 Persona 上传。
  // 原 slot 模式本来就不上传 VRM / 不克隆声音，不算「缺资源」，Check 照常可用（只核对人设文本）。
  const slotMode = !customMode;
  const hasVrm = !!role.local_vrm;
  const hasVoice = !!role.local_voice;
  const missingAll = !slotMode && !hasVrm && !hasVoice;
  const GRAY = { opacity: 0.55 };

  return (
    <div style={{ marginTop: 12 }}>
      <b>{t('syncLogTitle')}</b>
      {/* 机器勾选：最前面一排，默认全选；只有勾选的机器才执行下面的命令操作 */}
      {bindMachines.length > 0 && (
        <div style={{ display: 'flex', gap: 10, alignItems: 'center', margin: '8px 0', flexWrap: 'wrap' }}>
          {bindMachines.map((id) => (
            <label key={id} style={{ display: 'inline-flex', gap: 5, alignItems: 'center', cursor: 'pointer', fontSize: 13 }}>
              <input type="checkbox" checked={checked(id)} onChange={() => toggleM(id)} />
              {mName(id)}
            </label>
          ))}
          <button className="mini ghost" onClick={() => { const n = new Set(bindMachines); setSel(n); saveSel(n); }}>{t('selectAll')}</button>
          <button className="mini ghost" onClick={() => { const n = new Set(); setSel(n); saveSel(n); }}>{t('selectNone')}</button>
        </div>
      )}
      <div style={{ display: 'flex', gap: 8, alignItems: 'center', margin: '8px 0', flexWrap: 'wrap' }}>
        {/* 缺前置资源时按钮变「灰」但仍可点：点了跳到 Persona 对应的上传区。
            没 VRM 且没语音样本时，Check&Fix / PATCH 也无从下手，同样变灰。 */}
        <button className={'mini' + (missingAll ? ' ghost' : '')} style={missingAll ? GRAY : undefined}
          title={missingAll ? t('needVrmVoiceHint') : t('checkRun')}
          disabled={!missingAll && busy}
          onClick={missingAll ? () => goTab && goTab('persona') : check}>{busy && !missingAll ? '…' : '🔍 ' + t('checkBtn')}</button>
        <button className={'mini' + (missingAll ? ' ghost' : '')} style={missingAll ? GRAY : undefined}
          title={missingAll ? t('needVrmVoiceHint') : t('patchConfigHint')}
          disabled={!missingAll && (!!btnBusy || busy)}
          onClick={missingAll ? () => goTab && goTab('persona') : applyConfig}>{btnBusy === 'apply' && !missingAll ? '…' : '🧩 ' + t('patchConfigBtn')}</button>
        <button className={'mini' + (!hasVrm || slotMode ? ' ghost' : '')} style={!hasVrm || slotMode ? GRAY : undefined}
          title={slotMode ? t('assetSourceSlotShort') : (!hasVrm ? t('needVrmHint') : t('vrmPushHint'))}
          disabled={!slotMode && hasVrm && (!!btnBusy || busy)}
          onClick={!hasVrm || slotMode ? () => goTab && goTab('persona', 'persona-vrm') : pushVrm}>{btnBusy === 'vrm' && hasVrm && !slotMode ? '…' : '📦 ' + t('vrmPushBtn')}</button>
        <button className={'mini' + (!hasVoice || slotMode ? ' ghost' : '')} style={!hasVoice || slotMode ? GRAY : undefined}
          title={slotMode ? t('assetSourceSlotShort') : (!hasVoice ? t('needVoiceHint') : (anyCloning ? t('cloningOther') : t('forceCloneHint')))}
          disabled={!slotMode && hasVoice && (!!btnBusy || busy || anyCloning)}
          onClick={!hasVoice || slotMode ? () => goTab && goTab('persona', 'persona-voice') : forceClone}>{btnBusy === 'clone' && hasVoice && !slotMode ? '…' : '🎙 ' + t('forceCloneBtn')}</button>
        {!slotMode && (
          <label className="hint" style={{ display: 'inline-flex', gap: 6, alignItems: 'center', cursor: 'pointer' }} title={t('probeVoiceHint')}>
            <input
              type="checkbox"
              checked={probe}
              onChange={(e) => { setProbe(e.target.checked); setProbeVoice(e.target.checked); }}
            />
            {t('probeVoice')}
          </label>
        )}
        <span className="hint">{slotMode ? t('checkRunSlotMode') : t('checkRun')}</span>
      </div>
      {confirmCheck && (
        <dialog open style={{ position: 'fixed', inset: 0, margin: 'auto', zIndex: 60, maxWidth: 520 }}>
          <h3>{t('checkFirstTitle')}</h3>
          <p className="hint" style={{ marginTop: 0 }}>
            {customMode ? t('checkFirstCustom') : t('checkFirstSlot')}
          </p>
          <div className="row">
            <button className="secondary" onClick={() => setConfirmCheck(false)}>{t('cancel')}</button>
            <button onClick={confirmCheckRun}>{t('checkFirstContinue')}</button>
          </div>
        </dialog>
      )}
      {lastFail && (
        <div style={{ marginTop: 8 }}>
          <div className="err-banner">⚠️ {t('syncFailTitle')} — {new Date(lastFail.time).toLocaleString()} @ {lastFail.machine_id}</div>
          <pre style={{ margin: '6px 0 0', padding: 10, background: 'var(--bg-soft)', border: '1px solid var(--line-soft)', borderRadius: 8,
            fontSize: 12, lineHeight: 1.5, whiteSpace: 'pre-wrap', wordBreak: 'break-all', maxHeight: 220, overflowY: 'auto', color: 'var(--danger, #d33)' }}>
{`[${new Date(lastFail.time).toLocaleString()}] ${lastFail.error || 'failed'}${lastFail.detail ? '\n' + lastFail.detail : ''}`}
          </pre>
        </div>
      )}
      {!lastFail && logs && logs.length > 0 && <p className="hint">{t('syncNoFail')}</p>}
      {(report || []).map((r) => (
        <div key={r.machine_id + ':' + r.slot} style={{ marginTop: 8 }}>
          <div style={{ fontWeight: 600 }}>{r.machine_id} · slot {r.slot} {r.ok ? <span className="badge ok">✓ {t('checkOk')}</span> : <span className="badge warn">{(r.issues || []).length} {t('checkIssue')}</span>}</div>
          <pre style={{ margin: '6px 0 0', padding: 10, background: 'var(--bg-soft)', border: '1px solid var(--line-soft)', borderRadius: 8,
            fontSize: 12, lineHeight: 1.5, whiteSpace: 'pre-wrap', wordBreak: 'break-all', maxHeight: 220, overflowY: 'auto',
            color: r.ok ? 'var(--fg)' : 'var(--danger, #d33)' }}>
{[...(r.issues || []).map((x) => '⚠ ' + x), ...(r.fixed || []).map((x) => '🔧 ' + x)].join('\n') || '✓ ' + t('checkOk')}
          </pre>
        </div>
      ))}
    </div>
  );
}
