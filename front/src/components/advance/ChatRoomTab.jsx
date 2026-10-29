import { useEffect, useMemo, useState } from 'react';
import { api } from '../../api.js';
import { t } from '../../i18n.js';
import { useToastErr, useToastOk } from '../../toast.jsx';

// ChatRoom：多角色对话接播。
//   左侧选角色（有启用绑定的才可选）→ 角色下可勾选播报机器（默认**全部**机器都播）
//   → 右侧逐行编辑 [说话人 + 台词] → 播放。
//   播放 = POST /api/chatroom/play 逐行入队播报队列（同一 group_id）：一行默认下发到
//   该角色全部启用绑定（多机并行），组内按行严格接播（本行全部机器播完才接下一行）；
//   勾选了机器子集时带 machines 只在所选机器上播。轮询 /api/broadcast/queue
//   按 group_id 过滤展示每行状态。脚本可保存/打开/重命名/删除（后端持久化）。
//
// 草稿兜底：localStorage 存「最后打开的脚本 id + 未保存内容」，刷新不丢；正本在后端。
// 机器勾选是运行时选择（不进脚本/草稿），刷新后回到默认全选。

const DRAFT_KEY = 'chatroom_draft_v1';

function loadDraft() {
  try { return JSON.parse(localStorage.getItem(DRAFT_KEY)) || null; } catch { return null; }
}
function saveDraft(v) {
  try { localStorage.setItem(DRAFT_KEY, JSON.stringify(v)); } catch { /* 忽略 */ }
}

export default function ChatRoomTab() {
  const toastOk = useToastOk();
  const toastErr = useToastErr();

  const [roles, setRoles] = useState([]);
  const [machines, setMachines] = useState([]);
  const [scripts, setScripts] = useState([]);
  const [scriptID, setScriptID] = useState('');     // 当前打开的脚本（空=未保存草稿）
  const [scriptName, setScriptName] = useState('');
  const [lines, setLines] = useState([{ role_id: '', text: '' }]);
  const [groupID, setGroupID] = useState('');       // 正在播/最近一次播的会话
  const [qitems, setQitems] = useState([]);         // 队列快照
  const [playing, setPlaying] = useState(false);
  const [dirty, setDirty] = useState(false);        // 有未保存修改（提示用）
  // 播放节奏：每行播完后的停顿（秒）+ 播放轮数（forever=一直重复）
  const [intervalSec, setIntervalSec] = useState(0);
  const [repeats, setRepeats] = useState(1);
  const [forever, setForever] = useState(false);
  // 每个角色勾选的播报机器：{ role_id: { machine_id: bool } }；角色没记录 = 全部（默认）
  const [selMachines, setSelMachines] = useState({});

  const load = async () => {
    try {
      const [rs, ms] = await Promise.all([api('/api/roles'), api('/api/machines')]);
      setRoles(rs); setMachines(ms);
      try { const s = await api('/api/chatroom/scripts'); setScripts(s.scripts || []); } catch { /* 脚本接口失败不拦 */ }
    } catch (e) { toastErr(e.message); }
  };
  useEffect(() => { load(); }, []);

  // 草稿恢复（只跑一次，在 roles 拉到前后都行：行里存 role_id）
  useEffect(() => {
    const d = loadDraft();
    if (!d) return;
    if (Array.isArray(d.lines) && d.lines.length) setLines(d.lines);
    if (d.scriptID) setScriptID(d.scriptID);
    if (d.scriptName) setScriptName(d.scriptName);
    if (typeof d.intervalSec === 'number') setIntervalSec(d.intervalSec);
    if (typeof d.repeats === 'number' && d.repeats > 0) setRepeats(d.repeats);
    if (typeof d.forever === 'boolean') setForever(d.forever);
  }, []);

  // 行内容变化 → 存草稿 + 标脏
  useEffect(() => {
    saveDraft({ lines, scriptID, scriptName, intervalSec, repeats, forever });
  }, [lines, scriptID, scriptName, intervalSec, repeats, forever]);
  const markDirty = () => { setDirty(true); };

  // 有 group 在播时轮询队列快照（播完停轮询，留最终状态展示）
  useEffect(() => {
    if (!groupID) return;
    let stop = false;
    const tick = async () => {
      try {
        const r = await api('/api/broadcast/queue');
        if (stop) return;
        const mine = (r.items || []).filter((it) => it.group_id === groupID);
        setQitems(mine);
        const unfinished = mine.some((it) => it.status === 'pending' || it.status === 'running');
        setPlaying(unfinished);
        if (!unfinished) stop = true;
      } catch { /* 拉失败下次再试 */ }
    };
    tick();
    const id = setInterval(tick, 1500);
    return () => { stop = true; clearInterval(id); };
  }, [groupID]);

  // 可选角色：有启用绑定（机器+槽位齐全）的
  const usableRoles = useMemo(() => roles.filter((r) => (r.bindings || []).some((b) => b.enabled && b.machine_id && b.slot > 0)), [roles]);
  const roleName = (id) => roles.find((r) => r.id === id)?.name || id;
  const machineName = (id) => {
    const m = machines.find((x) => x.id === id);
    return m ? (m.name || m.addr) : id;
  };
  // 角色的启用绑定 / 可播机器列表（去重；与后端 enabledBindings 同口径）
  const roleMachines = (rid) => {
    const r = roles.find((x) => x.id === rid);
    if (!r) return [];
    return [...new Set((r.bindings || []).filter((b) => b.enabled && b.machine_id && b.slot > 0).map((b) => b.machine_id))];
  };
  // 该角色本行的目标机器：没勾过 = 全部（默认）；勾过 = 勾选的（与当前机器列表取交）
  const pickMachines = (rid) => {
    const all = roleMachines(rid);
    const sel = selMachines[rid];
    return sel ? all.filter((mid) => sel[mid]) : all;
  };
  const toggleMachine = (rid, mid) => {
    setSelMachines((cur) => {
      const all = roleMachines(rid);
      const prev = cur[rid] || Object.fromEntries(all.map((m) => [m, true]));
      return { ...cur, [rid]: { ...prev, [mid]: !prev[mid] } };
    });
  };

  const setLine = (i, patch) => {
    markDirty();
    setLines((ls) => ls.map((l, j) => (j === i ? { ...l, ...patch } : l)));
  };
  const addLine = () => {
    markDirty();
    // 新行默认沿用上一行的说话人，顺手
    const last = lines[lines.length - 1];
    setLines((ls) => [...ls, { role_id: last ? last.role_id : '', text: '' }]);
  };
  const removeLine = (i) => {
    markDirty();
    setLines((ls) => (ls.length <= 1 ? [{ role_id: '', text: '' }] : ls.filter((_, j) => j !== i)));
  };
  const moveLine = (i, d) => {
    markDirty();
    setLines((ls) => {
      const j = i + d;
      if (j < 0 || j >= ls.length) return ls;
      const out = [...ls];
      [out[i], out[j]] = [out[j], out[i]];
      return out;
    });
  };
  // 按所选角色轮流填充空行说话人
  const fillRotate = () => {
    if (!usableRoles.length) return;
    markDirty();
    setLines((ls) => ls.map((l, i) => ({ ...l, role_id: l.role_id || usableRoles[i % usableRoles.length].id })));
  };
  // 随机编排：对现有各行随机重排说话人（允许同一角色多次出现，如 A→B→A→C→A），
  // 避免相邻两行同一角色（独白没意义）；台词原样保留。
  const shuffleOrchestrate = () => {
    markDirty();
    setLines((ls) => {
      const speakers = [...new Set(ls.map((l) => l.role_id).filter(Boolean))];
      const pool = speakers.length ? speakers : usableRoles.map((r) => r.id);
      if (!pool.length) return ls;
      const out = [];
      let prev = null;
      for (const l of ls) {
        let cand = pool.filter((p) => p !== prev);
        if (!cand.length) cand = pool;
        const pick = cand[Math.floor(Math.random() * cand.length)];
        out.push({ ...l, role_id: pick });
        prev = pick;
      }
      return out;
    });
  };

  // ---- 脚本管理 ----
  const openScript = (id) => {
    const sc = scripts.find((s) => s.id === id);
    if (!sc) return;
    setScriptID(sc.id); setScriptName(sc.name);
    setLines((sc.lines && sc.lines.length ? sc.lines : [{ role_id: '', text: '' }]).map((l) => ({ role_id: l.role_id, text: l.text })));
    setDirty(false);
  };
  const newScript = () => {
    setScriptID(''); setScriptName('');
    setLines([{ role_id: '', text: '' }]);
    setDirty(false);
  };
  const saveScript = async () => {
    try {
      let name = scriptName.trim();
      if (!name) {
        name = prompt(t('crScriptNamePrompt'), 'ChatRoom ' + new Date().toLocaleString());
        if (!name) return;
        setScriptName(name);
      }
      const r = await api('/api/chatroom/scripts', { method: 'POST', body: JSON.stringify({ id: scriptID, name, lines }) });
      setScriptID(r.script.id);
      setDirty(false);
      toastOk(t('crSaved'));
      const s = await api('/api/chatroom/scripts'); setScripts(s.scripts || []);
    } catch (e) { toastErr(e.message); }
  };
  const renameScript = async () => {
    const name = prompt(t('crScriptNamePrompt'), scriptName || '');
    if (!name) return;
    setScriptName(name);
    try {
      await api('/api/chatroom/scripts', { method: 'POST', body: JSON.stringify({ id: scriptID, name, lines }) });
      const s = await api('/api/chatroom/scripts'); setScripts(s.scripts || []);
    } catch (e) { toastErr(e.message); }
  };
  const deleteScript = async () => {
    if (!scriptID || !confirm(t('crDeleteConfirm'))) return;
    try {
      await api('/api/chatroom/scripts/' + encodeURIComponent(scriptID), { method: 'DELETE' });
      newScript();
      const s = await api('/api/chatroom/scripts'); setScripts(s.scripts || []);
    } catch (e) { toastErr(e.message); }
  };

  // ---- 播放/停止 ----
  const play = async () => {
    for (let i = 0; i < lines.length; i++) {
      if (!lines[i].role_id) { toastErr(t('crEmpty')); return; }
      if (!lines[i].text.trim()) { toastErr(t('crEmptyLine').replace('%s', i + 1)); return; }
      if (!pickMachines(lines[i].role_id).length) {
        toastErr(t('crNoMachine').replace('%s', roleName(lines[i].role_id)));
        return;
      }
    }
    try {
      // 全选就不带 machines（后端默认=该角色全部启用绑定）；勾了子集才显式下发
      const body = {
        lines: lines.map((l) => {
          const all = roleMachines(l.role_id);
          const ms = pickMachines(l.role_id);
          return ms.length === all.length
            ? { role_id: l.role_id, text: l.text }
            : { role_id: l.role_id, text: l.text, machines: ms };
        }),
        interval_seconds: Math.max(0, Number(intervalSec) || 0),
        repeats: forever ? 0 : Math.max(1, Math.floor(Number(repeats) || 1)),
        forever,
      };
      const r = await api('/api/chatroom/play', { method: 'POST', body: JSON.stringify(body) });
      setGroupID(r.group_id);
      setPlaying(true);
      toastOk(t('crQueued').replace('%s', r.queued));
    } catch (e) { toastErr(e.message); }
  };
  const stop = async () => {
    if (!groupID) { toastErr(t('crNoActiveGroup')); return; }
    try {
      await api('/api/chatroom/stop?id=' + encodeURIComponent(groupID), { method: 'POST' });
      setPlaying(false);
      toastOk(t('crStopped'));
    } catch (e) { toastErr(e.message); }
  };

  // 当前行状态（按 group_seq 取模对应行号——重复播放时 seq 会继续递增；
  // 一行多机时聚合成一个状态：有 running 即 running，全部 done 才 done，否则有 failed 报 failed）
  const statusOfLine = (i) => {
    const n = lines.length || 1;
    const mine = qitems.filter((it) => ((Number(it.group_seq) || 1) - 1) % n === i);
    if (!mine.length) return '';
    // 只看最新一轮：历史轮次的 failed/done 不该盖住本轮状态
    const lastSeq = Math.max(...mine.map((it) => Number(it.group_seq) || 1));
    const curRound = Math.ceil(lastSeq / n);
    const sts = mine.filter((it) => Math.ceil((Number(it.group_seq) || 1) / n) === curRound).map((it) => it.status);
    if (sts.some((s) => s === 'running')) return 'running';
    if (sts.every((s) => s === 'done')) return 'done';
    if (sts.some((s) => s === 'failed')) return 'failed';
    return 'pending';
  };
  // 当前轮次（用于「重复 N 次 / 一直重复」进度展示）
  const maxSeq = qitems.reduce((m, it) => Math.max(m, Number(it.group_seq) || 0), 0);
  const round = maxSeq && lines.length ? Math.ceil(maxSeq / lines.length) : 0;

  return (
    <>
      <p className="hint">{t('crHint')}</p>
      <div style={{ display: 'grid', gridTemplateColumns: '260px 1fr', gap: 16, alignItems: 'start' }}>
        {/* 左：角色多选 */}
        <div className="card">
          <h3 style={{ marginTop: 0 }}>{t('crPickRoles')}</h3>
          {!usableRoles.length && <p className="hint">{t('crNoRoles')}</p>}
          <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
            {usableRoles.map((r) => {
              const picked = roleMachines(r.id);
              return (
                <div key={r.id}>
                  <label style={{ display: 'flex', alignItems: 'center', gap: 8, cursor: 'pointer' }}>
                    <input type="checkbox"
                      checked={lines.some((l) => l.role_id === r.id)}
                      onChange={(e) => {
                        markDirty();
                        if (e.target.checked) {
                          // 勾选：末尾追加一行（空行才直接填说话人）
                          setLines((ls) => ls.some((l) => l.role_id === r.id) ? ls : [...ls, { role_id: r.id, text: '' }]);
                        } else {
                          // 取消勾选：该角色的行清掉说话人（保留台词避免误删）
                          setLines((ls) => ls.map((l) => (l.role_id === r.id ? { ...l, role_id: '' } : l)));
                        }
                      }} />
                    <span style={{ flex: 1 }}>{r.name}</span>
                    <span className="badge mut">
                      🖥 {picked.length}
                    </span>
                  </label>
                  {/* 该角色可播的机器：默认全选（= 全部机器都播），取消勾选只播所选 */}
                  {lines.some((l) => l.role_id === r.id) && picked.length > 0 && (
                    <div style={{ margin: '2px 0 0 24px', display: 'flex', flexDirection: 'column', gap: 2 }}>
                      {picked.map((mid) => (
                        <label key={mid} style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 12, cursor: 'pointer' }}>
                          <input type="checkbox" style={{ width: 'auto' }}
                            checked={!selMachines[r.id] || !!selMachines[r.id][mid]}
                            onChange={() => toggleMachine(r.id, mid)} />
                          <span>🖥 {machineName(mid)}</span>
                        </label>
                      ))}
                    </div>
                  )}
                </div>
              );
            })}
          </div>
          <button className="mini secondary" style={{ marginTop: 6, width: '100%' }} onClick={fillRotate} disabled={!usableRoles.length}>
            {t('crFillRotate')}
          </button>
          <button className="mini secondary" style={{ marginTop: 6, width: '100%' }} onClick={shuffleOrchestrate} disabled={!usableRoles.length}>
            🎲 {t('crShuffle')}
          </button>
          <p className="hint" style={{ margin: '6px 0 0' }}>{t('crShuffleHint')}</p>
        </div>

        {/* 右：脚本栏 + 对话编辑器 */}
        <div className="card">
          <div style={{ display: 'flex', gap: 6, alignItems: 'center', flexWrap: 'wrap', marginBottom: 10 }}>
            <span className="badge mut">{t('crScript')}</span>
            <select className="mini" value={scriptID} onChange={(e) => openScript(e.target.value)}>
              <option value="">{t('crScriptPick')}</option>
              {scripts.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
            </select>
            {scriptID > '' && <input className="mini" style={{ width: 140 }} value={scriptName} onChange={(e) => { markDirty(); setScriptName(e.target.value); }} />}
            <button className="mini" onClick={newScript}>{t('crScriptNew')}</button>
            <button className="mini primary" onClick={saveScript}>{t('crScriptSave')}</button>
            {scriptID > '' && <>
              <button className="mini" onClick={renameScript}>{t('crScriptRename')}</button>
              <button className="mini danger" onClick={deleteScript}>{t('crScriptDelete')}</button>
            </>}
            {dirty && <span className="badge warn">{t('crUnsavedTip')}</span>}
          </div>

          {lines.map((l, i) => {
            const st = statusOfLine(i);
            const lm = l.role_id ? pickMachines(l.role_id) : [];
            return (
              <div key={i} style={{
                display: 'flex', gap: 6, alignItems: 'flex-start', marginBottom: 6,
                outline: st === 'running' ? '2px solid var(--ok)' : st === 'failed' ? '2px solid var(--bad)' : 'none',
                borderRadius: 10, padding: 2,
              }}>
                <span style={{ width: 22, textAlign: 'right', color: 'var(--mut)', paddingTop: 8, fontSize: 12 }}>{i + 1}</span>
                <select className="mini" style={{ width: 150 }} value={l.role_id} onChange={(e) => setLine(i, { role_id: e.target.value })} disabled={st === 'running'}>
                  <option value="">{t('crSpeaker')}…</option>
                  {usableRoles.map((r) => <option key={r.id} value={r.id}>{r.name}</option>)}
                </select>
                <textarea className="mini" rows={Math.min(3, Math.max(1, Math.ceil((l.text.length || 1) / 40)))}
                  style={{ flex: 1, resize: 'vertical' }} placeholder={t('crSay')}
                  value={l.text} onChange={(e) => setLine(i, { text: e.target.value })} disabled={st === 'running'} />
                <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
                  <button className="mini ghost" title="↑" onClick={() => moveLine(i, -1)} disabled={i === 0}>↑</button>
                  <button className="mini ghost" title="↓" onClick={() => moveLine(i, 1)} disabled={i === lines.length - 1}>↓</button>
                </div>
                <button className="mini ghost" onClick={() => removeLine(i)}>✕</button>
                {lm.length === 1
                  ? <span className="badge mut" style={{ alignSelf: 'center' }}>🖥 {machineName(lm[0])}</span>
                  : lm.length > 1 && <span className="badge mut" style={{ alignSelf: 'center' }}>🖥 ×{lm.length}</span>}
                {st && <span className={'badge ' + (st === 'running' ? 'ok' : st === 'failed' ? 'bad' : st === 'done' ? 'mut' : 'warn')} style={{ alignSelf: 'center' }}>
                  {t(st === 'running' ? 'bqRunning' : st === 'failed' ? 'bqFailed' : st === 'done' ? 'bqDone' : 'bqPending')}
                </span>}
              </div>
            );
          })}

          <div style={{ display: 'flex', gap: 6, marginTop: 10, alignItems: 'center', flexWrap: 'wrap' }}>
            <button className="mini" onClick={addLine}>{t('crAddLine')}</button>
            <span style={{ flex: 1 }}></span>
            <label className="hint" title={t('crIntervalHint')} style={{ display: 'flex', alignItems: 'center', gap: 4, margin: 0 }}>
              {t('crInterval')}
              <input className="mini" type="number" min="0" step="0.5" style={{ width: 62 }}
                value={intervalSec}
                onChange={(e) => { setIntervalSec(e.target.value === '' ? 0 : Number(e.target.value)); markDirty(); }} />
              {t('crSeconds')}
            </label>
            <label className="hint" style={{ display: 'flex', alignItems: 'center', gap: 4, margin: 0 }}>
              {t('crRepeat')}
              <input className="mini" type="number" min="1" step="1" style={{ width: 56 }}
                value={repeats} disabled={forever}
                onChange={(e) => { setRepeats(Math.max(1, Math.floor(Number(e.target.value) || 1))); markDirty(); }} />
              {t('crTimes')}
              <input type="checkbox" checked={forever}
                onChange={(e) => { setForever(e.target.checked); markDirty(); }} />
              {t('crRepeatForever')}
            </label>
            {round > 0 && (
              <span className="badge mut">
                {t('crRound')} {round}{forever ? ' / ∞' : (repeats > 1 ? ` / ${repeats}` : '')}
              </span>
            )}
            {playing
              ? <button className="mini danger" onClick={stop}>{t('crStop')}</button>
              : <button className="mini primary" onClick={play}>{t('crPlay')}</button>}
          </div>
        </div>
      </div>
    </>
  );
}
