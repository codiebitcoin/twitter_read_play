import { useEffect, useRef, useState } from 'react';
import { t } from '../../i18n.js';
import { api } from '../../api.js';
import { useNav } from '../../App.jsx';
import { useToastErr, useToastOk } from '../../toast.jsx';
import { notifyMessageQueued } from '../../messageQueue.js';
import { parseFilterWords, liveHasUserLevel } from './shared.js';

// 弹幕分页：每页条数 / 前端最多保留的条数（更早的从服务端翻回来）。
const PAGE = 120;
const MAX_KEEP = 600;

// Stream：配置已移到「角色设置 → Live 上游配置」，这里只展示实时弹幕和播报记录。
// 弹幕过滤（黑名单用户/黑名单词/过滤SC/最低等级）配置直接内联在本页：
// 后端判定在 livechat.Filter（通用 interface，所有平台同一套规则）；
// 前端预览用同一套规则本地过滤，被滤掉的条数实时显示。
export default function StreamPanel({ role, onSaved }) {
  const nav = useNav();
  const toastOk = useToastOk();
  const toastErr = useToastErr();
  const [sources, setSources] = useState(null);
  const [statuses, setStatuses] = useState([]);
  const [played, setPlayed] = useState(null);
  const [prefilled, setPrefilled] = useState(false);
  const [sourceId, setSourceId] = useState(role.live_source_id || '');
  const [busy, setBusy] = useState(false);

  const loadSources = async () => {
    try {
      const r = await api('/api/livechat');
      setSources(r.configs || []);
      setStatuses(r.statuses || []);
    } catch { setSources([]); setStatuses([]); }
  };
  const loadPlayed = async () => {
    try {
      const all = await api(`/api/logs?role_id=${encodeURIComponent(role.id)}&kind=broadcast`);
      setPlayed((all || []).filter((l) => l.source === 'live'));
    } catch { setPlayed([]); }
  };
  useEffect(() => { loadSources(); }, []);
  useEffect(() => {
    loadPlayed();
    const iv = setInterval(loadPlayed, 10000);
    return () => clearInterval(iv);
  }, [role.id]);

  // 角色没绑过源时，默认用第一路已启用的 Chat（通常全局也就一路）。
  useEffect(() => {
    if (prefilled || sources === null) return;
    if (role.live_source_id) { setSourceId(role.live_source_id); setPrefilled(true); return; }
    const first = (sources || []).find((c) => c.enabled) || (sources || [])[0];
    if (first) setSourceId((x) => x || first.id);
    setPrefilled(true);
  }, [sources, role.live_source_id, prefilled]);

  const st = statuses.find((s) => s.id === sourceId);
  const bound = (sources || []).find((c) => c.id === sourceId);
  const srcTitle = bound ? shortSrc(bound, 72) : '';
  // 直播源本身被停用（与角色级「暂停播报」是两回事，见下面的 toggleBroadcast）
  const configuredOff = bound && bound.enabled === false;
  const settingsURL = `/roles/${encodeURIComponent(role.id)}?tab=settings`;

  const toggleListen = async () => {
    if (!sourceId || busy) return;
    setBusy(true);
    try {
      const running = !!st?.running;
      await api(`/api/livechat/${encodeURIComponent(sourceId)}/${running ? 'stop' : 'start'}`, { method: 'POST' });
      toastOk(running ? t('streamStopped') : t('streamStarted'));
      loadSources();
      // 连接是异步的：短延时再刷两次状态，别让按钮看起来没反应。
      setTimeout(loadSources, 700);
      setTimeout(loadSources, 1800);
    } catch (e) {
      toastErr(e.message);
    } finally {
      setBusy(false);
    }
  };

  // ---- 弹幕过滤（后端 livechat.Filter 的前端镜像；改动即存到角色） ----
  const saveLive = async (extra) => {
    try {
      await api('/api/roles/' + encodeURIComponent(role.id), { method: 'PUT', body: JSON.stringify({ ...role, ...extra }) });
      toastOk(t('saved'));
      onSaved && onSaved();
      return true;
    } catch (e) { toastErr(e.message); return false; }
  };
  // 行内一键拉黑：用户名进黑名单（去重，忽略大小写），预览立即隐藏该用户所有弹幕
  const blockUser = async (user) => {
    const name = String(user || '').trim();
    if (!name) return;
    const users = parseFilterWords(role.live_block_users);
    if (users.some((u) => u.toLowerCase() === name.toLowerCase())) return;
    await saveLive({ live_block_users: [...users, name].join(', ') });
  };
  const unblockUser = async (name) => {
    const users = parseFilterWords(role.live_block_users).filter((u) => u.toLowerCase() !== String(name).toLowerCase());
    await saveLive({ live_block_users: users.join(', ') });
  };
  // 快速清空：丢掉本角色全部待播（弹幕刷屏时一键清，正在播的不动）
  const clearRoleQueue = async () => {
    try {
      const r = await api('/api/broadcast/queue?role_id=' + encodeURIComponent(role.id), { method: 'DELETE' });
      toastOk(t('streamClearedN').replace('%s', (r && r.cleared) || 0));
      notifyMessageQueued();
    } catch (e) { toastErr(e.message); }
  };
  // 播报开关：开启 / 暂停。暂停 = 继续监听弹幕但不播报；恢复时服务端会重置游标
  // （upsertRole 检测 live_enabled false→true），从最新一条接着播，不补播暂停期间的积压。
  const paused = !role.live_enabled;
  const toggleBroadcast = async () => {
    try {
      await api('/api/roles/' + encodeURIComponent(role.id), { method: 'PUT', body: JSON.stringify({ ...role, live_enabled: paused }) });
      toastOk(paused ? t('streamResumed') : t('streamPaused'));
      onSaved && onSaved();
    } catch (e) { toastErr(e.message); }
  };

  return (
    <>
      <div className="card" style={{ maxWidth: 1000 }}>
        <div className="bound-source">
          <b>{role.name || role.id} · {t('liveStream')}</b>
          {/* 播报开关状态：播报中 / 已暂停（与右边的监听连接状态互相独立） */}
          <span className={'badge ' + (paused ? 'warn' : 'ok')} title={t('streamPausedHint')}>
            {paused ? '⏸ ' + t('livePaused') : t('liveBroadcasting')}
          </span>
          <span className={'badge ' + (st?.running ? 'ok' : 'warn')}>
            {st?.running ? t('liveRunning') : t('liveStopped')}
          </span>
        </div>
        <p className="hint" style={{ margin: '4px 0 0', wordBreak: 'break-all' }}>
          {role.live_url || `— ${t('liveUrlNone')}`}
        </p>
        <div className="account-actions">
          {sourceId ? (
            <button type="button" className="mini" onClick={toggleListen} disabled={busy}>
              {busy ? '…' : (st?.running ? `■ ${t('streamStopListen')}` : `▶ ${t('streamStartListen')}`)}
            </button>
          ) : null}
          <button type="button" className={paused ? 'mini' : 'mini ghost'} onClick={toggleBroadcast}
            title={t('streamPausedHint')}>
            {paused ? `▶ ${t('streamResume')}` : `⏸ ${t('streamPause')}`}
          </button>
          <button type="button" className="mini ghost" onClick={clearRoleQueue} title={t('streamClearRoleTip')}>🧹 {t('streamClearRole')}</button>
          <button type="button" className="mini ghost" onClick={() => nav(settingsURL)}>{t('goRoleSettings')}</button>
          <button type="button" className="mini ghost" onClick={() => { loadSources(); loadPlayed(); }}>{t('refresh')}</button>
        </div>
        {paused ? <p className="hint account-warn">⏸ {t('streamPausedHint')}</p> : null}
        {!paused && configuredOff ? <p className="hint account-warn">⚠ {t('streamSourceDisabledHint')}</p> : null}
        {st?.last_error ? <p className="hint account-err">⚠ {st.last_error}</p> : null}
      </div>

      {sourceId ? (
        <StreamPreview id={sourceId} title={srcTitle} running={st?.running} role={role} onSaved={onSaved}
          levelSupported={liveHasUserLevel(bound?.kind)} />
      ) : (
        <div className="card" style={{ marginTop: 12, maxWidth: 1000 }}>
          <p className="hint" style={{ margin: 0 }}>⚠️ {t('streamNoChat')}</p>
          <button type="button" className="mini" style={{ marginTop: 10 }} onClick={() => nav(settingsURL)}>{t('streamGoLive')}</button>
        </div>
      )}

      <div className="card" style={{ marginTop: 12, maxWidth: 1000 }}>
        <b>{t('streamPlayedTitle')}</b>
        <span className="hint" style={{ marginLeft: 8 }}>{played ? played.length : '…'}</span>
        {!played ? <p className="loading">{t('loading')}</p>
          : played.length === 0 ? <p className="empty" style={{ marginTop: 8 }}>∅ {t('streamPlayedEmpty')}</p>
          : played.map((l, i) => (
            <div key={(l.tweet_id || '') + i} className="card tweet" style={{ marginTop: 8, border: '1px solid var(--line-soft)', borderRadius: 14 }}>
              <p style={{ margin: 0, whiteSpace: 'pre-wrap', wordBreak: 'break-word', fontSize: 14.5, lineHeight: 1.45 }}>{l.text}</p>
              <div className="hint" style={{ marginTop: 8, fontSize: 12, display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
                <span>{t('tabStream')}</span>
                <span>{new Date(l.time).toLocaleString()}</span>
                {l.machine_id && <span className="badge mut">@ {l.machine_id.slice(0, 12)}</span>}
                {l.ok ? <span className="badge ok">✓ {t('watchPlayedOk')}</span> : <span className="badge bad">✗ {l.error || t('watchPlayedFail')}</span>}
              </div>
            </div>
          ))}
      </div>
    </>
  );
}

function shortSrc(c, n = 48) {
  const raw = ((c && (c.title || c.channel || c.id)) || '').trim();
  return raw.length > n ? raw.slice(0, n) + '…' : raw;
}

function StreamPreview({ id, title, running, role, onSaved, levelSupported }) {
  const [msgs, setMsgs] = useState([]);
  const toastOk = useToastOk();
  const toastErr = useToastErr();
  const [newBlock, setNewBlock] = useState('');
  const listRef = useRef(null);
  const lastSeq = useRef(0);
  const [hasOlder, setHasOlder] = useState(false);
  const [loadingOlder, setLoadingOlder] = useState(false);

  // 角色过滤配置（后端 livechat.Filter 的镜像；保存即生效，播报侧同一套规则）
  const blockUsers = parseFilterWords(role?.live_block_users);
  const blockWords = parseFilterWords(role?.live_filter_words);
  const onlySC = !!role?.live_only_sc;
  const noSC = !!role?.live_no_sc;
  const minLevel = +role?.live_min_level || 0;

  const saveLive = async (extra) => {
    try {
      await api('/api/roles/' + encodeURIComponent(role.id), { method: 'PUT', body: JSON.stringify({ ...role, ...extra }) });
      toastOk(t('saved'));
      onSaved && onSaved();
    } catch (e) { toastErr(e.message); }
  };
  const blockUser = async (name) => {
    const u = String(name || '').trim();
    if (!u) return;
    if (blockUsers.some((x) => x.toLowerCase() === u.toLowerCase())) return;
    await saveLive({ live_block_users: [...blockUsers, u].join(', ') });
  };
  const unblockUser = async (name) => {
    const rest = blockUsers.filter((x) => x.toLowerCase() !== String(name).toLowerCase());
    await saveLive({ live_block_users: rest.join(', ') });
  };

  // 与 livechat.Filter.Pass 同规则的本地判定（预览也滤，所见即所播）
  const pass = (m) => {
    const u = String(m.user || '').toLowerCase();
    if (blockUsers.some((b) => b.toLowerCase() === u)) return false;
    const special = !!String(m.amount || '').trim() ||
      ['superchat', 'cheer', 'gift', 'sub', 'subscription', 'member'].includes(String(m.kind || '').toLowerCase());
    if (onlySC && !special) return false;
    if (noSC && special) return false;
    if (minLevel > 0 && (+m.user_level || 0) > 0 && +m.user_level < minLevel) return false;
    const hay = String((m.user || '') + ' ' + (m.text || '') + ' ' + (m.amount || '')).toLowerCase();
    if (blockWords.some((w) => w && hay.includes(w.toLowerCase()))) return false;
    return true;
  };
  const shown = msgs.filter(pass);
  const hidden = msgs.length - shown.length;

  useEffect(() => {
    let alive = true;
    lastSeq.current = 0;
    setMsgs([]);
    setHasOlder(false);
    const pull = async () => {
      if (!alive) return;
      const first = lastSeq.current === 0;
      const box = listRef.current;
      // 只有原本就贴着底部时才跟着滚，否则用户正在翻历史会被拽走。
      const atBottom = !box || (box.scrollHeight - box.scrollTop - box.clientHeight < 80);
      try {
        const r = await api(`/api/livechat/${encodeURIComponent(id)}/messages?after=${lastSeq.current}&limit=${PAGE}`);
        if (!alive) return;
        if (first) setHasOlder(!!r.has_older);
        if (!r.messages?.length) return;
        lastSeq.current = r.latest_seq || r.messages[r.messages.length - 1].seq;
        setMsgs((x) => {
          const seen = new Set(x.map((m) => m.seq));
          const next = [...x, ...r.messages.filter((m) => !seen.has(m.seq))];
          if (next.length <= MAX_KEEP) return next;
          // 前端裁掉的部分还能从服务端翻回来，标一下让「加载更早」可用。
          setHasOlder(true);
          return next.slice(next.length - MAX_KEEP);
        });
        if (atBottom) {
          requestAnimationFrame(() => {
            const el = listRef.current;
            if (el) el.scrollTop = el.scrollHeight;
          });
        }
      } catch { /* 预览失败不挡配置 */ }
    };
    pull();
    const timer = setInterval(pull, 2000);
    return () => { alive = false; clearInterval(timer); };
  }, [id]);

  // 向上翻一页：before=当前最早一条的 seq。补完补偿滚动位置，否则视口会跳走。
  const loadOlder = async () => {
    if (loadingOlder || !hasOlder) return;
    const before = msgs[0]?.seq;
    if (!before) { setHasOlder(false); return; }
    setLoadingOlder(true);
    const box = listRef.current;
    const prevHeight = box ? box.scrollHeight : 0;
    try {
      const r = await api(`/api/livechat/${encodeURIComponent(id)}/messages?before=${before}&limit=${PAGE}`);
      const older = r.messages || [];
      if (!older.length) {
        setHasOlder(false);
      } else {
        setMsgs((x) => {
          const seen = new Set(x.map((m) => m.seq));
          return [...older.filter((m) => !seen.has(m.seq)), ...x];
        });
        setHasOlder(!!r.has_older);
        requestAnimationFrame(() => {
          const el = listRef.current;
          if (el) el.scrollTop += el.scrollHeight - prevHeight;
        });
      }
    } catch { /* 翻页失败保持现状 */ }
    setLoadingOlder(false);
  };

  return (
    <div className="card live-chat" style={{ marginTop: 12, maxWidth: 1000 }}>
      <div className="bound-source" style={{ marginBottom: 8 }}>
        <b>{title || t('liveStream')}</b>
        <span className="hint">{t('streamChatRealtime')}</span>
        {hidden > 0 && <span className="badge mut" title={t('streamHiddenTip')}>🚫 {t('streamHiddenN').replace('%s', hidden)}</span>}
        {running === true && <span className="badge ok">{t('liveRunning')}</span>}
        {running === false && <span className="badge mut">{t('liveStopped')}</span>}
      </div>

      {/* 过滤设置条：黑名单用户（chips，可点 × 移除）/ 黑名单词 / 过滤SC / 最低等级（仅支持等级的平台显示） */}
      <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap', margin: '0 0 8px', padding: '8px 10px', background: 'var(--bg-soft)', borderRadius: 10 }}>
        <span className="hint">🚫 {t('liveBlockUsers')}</span>
        {blockUsers.map((u) => (
          <span key={u} className="badge warn" style={{ display: 'inline-flex', gap: 4, alignItems: 'center' }}>
            {u}
            <button type="button" className="mini ghost" style={{ padding: '0 2px' }} title={t('liveUnblock')}
              onClick={() => unblockUser(u)}>×</button>
          </span>
        ))}
        <input className="mini" style={{ width: 130 }} placeholder={t('liveBlockUserPh')} value={newBlock}
          onKeyDown={(e) => { if (e.key === 'Enter' && newBlock.trim()) { blockUser(newBlock.trim()); setNewBlock(''); } }}
          onChange={(e) => setNewBlock(e.target.value)} />
        <button type="button" className="mini ghost" disabled={!newBlock.trim()}
          onClick={() => { blockUser(newBlock.trim()); setNewBlock(''); }}>＋</button>
        <span style={{ flex: 1 }} />
        <input className="mini" style={{ width: 180 }} placeholder={t('liveBlockWordsPh')} defaultValue={role?.live_filter_words || ''}
          title={t('liveBlockWordsTip')}
          onKeyDown={(e) => { if (e.key === 'Enter') saveLive({ live_filter_words: e.target.value }); }}
          onBlur={(e) => { if (e.target.value !== (role?.live_filter_words || '')) saveLive({ live_filter_words: e.target.value }); }} />
        <label className="hint" style={{ display: 'inline-flex', gap: 5, alignItems: 'center', cursor: 'pointer' }} title={t('liveNoSCTip')}>
          <input type="checkbox" checked={noSC}
            onChange={(e) => saveLive({ live_no_sc: e.target.checked, ...(e.target.checked ? { live_only_sc: false } : {}) })} />
          {t('liveNoSC')}
        </label>
        {levelSupported && (
          <label className="hint" style={{ display: 'inline-flex', gap: 5, alignItems: 'center' }} title={t('liveMinLevelTip')}>
            {t('liveMinLevel')}
            <input className="mini" type="number" min="0" max="200" style={{ width: 64 }} value={minLevel}
              onChange={(e) => saveLive({ live_min_level: Math.max(0, +e.target.value || 0) })} />
          </label>
        )}
      </div>

      {shown.length === 0 && <p className="hint">{t('liveNoMessages')}</p>}
      <div
        ref={listRef}
        onScroll={() => {
          const el = listRef.current;
          if (el && el.scrollTop < 24) loadOlder();
        }}
        style={{ maxHeight: 520, overflowY: 'auto', paddingRight: 4 }}
      >
        {hasOlder ? (
          <div style={{ textAlign: 'center', padding: '2px 0 8px' }}>
            <button type="button" className="mini ghost" disabled={loadingOlder} onClick={loadOlder}>
              {loadingOlder ? '…' : `↑ ${t('streamLoadOlder')}`}
            </button>
          </div>
        ) : null}
        {shown.map((m) => (
          <div key={m.seq} className={'live-msg' + (m.kind && m.kind !== 'text' ? ' live-msg-' + m.kind : '')}>
            <span className="hint">{m.at ? new Date(m.at).toLocaleTimeString() : ''} </span>
            <b>{m.user}</b>
            {m.amount ? <span className="badge ok" style={{ marginLeft: 6 }}>{m.amount}</span> : null}
            {m.kind && m.kind !== 'text' ? <span className="badge mut" style={{ marginLeft: 6 }}>{m.kind}</span> : null}
            {(+m.user_level || 0) > 0 ? <span className="badge mut" style={{ marginLeft: 6, fontSize: 11 }}>Lv.{m.user_level}</span> : null}
            {' '}：{m.text}
            <button type="button" className="mini ghost" style={{ marginLeft: 8, padding: '0 4px' }} title={t('liveBlockTip')}
              onClick={() => blockUser(m.user)}>🚫</button>
          </div>
        ))}
      </div>
    </div>
  );
}
