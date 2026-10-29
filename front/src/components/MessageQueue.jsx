import { useEffect, useMemo, useRef, useState } from 'react';
import { api } from '../api.js';
import { t } from '../i18n.js';
import { onMessageQueued } from '../messageQueue.js';
import { useToastErr } from '../toast.jsx';

// 全局 MessageQueue 面板：两类消息统一展示、按机器分组（同机器串行消费）：
//   - broadcast：所有播报（手动点播/随机/自动新推文/自动随机）先入队再消费；
//   - op：设备操作（PATCH设置 / Check&fix / VRM上传 / voice_clone / 轮询），触发即入队、
//     后台异步执行，VRM 上传等状态统一在这里跟进，不再弹独立弹窗。
//
// 轮询节奏：有 pending/running 时 2s；空闲 30s 心跳；本页操作后由 notify 立刻拉一次。
const RUN_MS = 2000;
const IDLE_MS = 30000;

const SOURCE_KEY = { manual: 'bqSrcManual', random: 'bqSrcRandom', auto: 'bqSrcAuto', poll: 'bqSrcPoll' };

// 设备操作类型 → 展示文案（复用各按钮名）
const OP_KIND = {
  patch_config: () => '🧩 ' + t('patchConfigBtn'),
  check_fix: () => '🔍 ' + t('checkBtn'),
  upload_vrm: () => '📦 ' + t('vrmPushBtn'),
  voice_clone: () => '🎙 ' + t('forceCloneBtn'),
  poll: () => '⟳ ' + t('mqPoll'),
};

const isActive = (it) => it.status === 'pending' || it.status === 'running';

function statusLabel(s) {
  if (s === 'running') return t('bqRunning');
  if (s === 'done') return t('bqDone');
  if (s === 'failed') return t('bqFailed');
  return t('bqPending');
}

function statusClass(s) {
  if (s === 'running') return 'badge warn';
  if (s === 'done') return 'badge ok';
  if (s === 'failed') return 'badge bad';
  return 'badge mut';
}

// 一条消息的主文案：播报显示角色名，操作显示操作名
function itemTitle(it) {
  if (it.type === 'op') return (OP_KIND[it.kind] || (() => it.kind || '?'))();
  return it.role_name;
}

function pollHint(it) {
  if (it.kind !== 'poll' || it.status !== 'done') return '';
  if (it.detail === 'queued') return t('pollQueued').replace('%s', it.count || 0);
  if (it.detail === 'filtered') return t('pollFiltered');
  if (it.detail === 'none') return t('pollNone');
  return '';
}

export default function MessageQueue() {
  const [items, setItems] = useState([]);
  // 分组展开状态：默认全部展开（弹幕刷屏时用户要的是「点一下就删掉这一条」，
  // 折叠起来还得先展开机器组）；点标题可折叠，选择记在内存即可。
  const [expanded, setExpanded] = useState({});
  const [dismissedAt, setDismissedAt] = useState(0);
  const [clearing, setClearing] = useState(false);
  const pollRef = useRef(null);
  const toastErr = useToastErr();
  // toast 函数放 ref：避免成为 effect 依赖后重建轮询
  const toastRef = useRef(toastErr);
  toastRef.current = toastErr;
  // 已提示过的失败播报（首轮只登记，避免刚打开页面就刷一屏历史失败）
  const seenFailed = useRef(null);

  useEffect(() => {
    let alive = true;
    let timer = null;
    let delay = 0;
    const poll = async () => {
      try {
        const res = await api('/api/messages/queue');
        if (!alive) return;
        const list = res && Array.isArray(res.items) ? res.items : [];
        setItems(list);
        // 新失败的「播报」→ 立刻弹 toast（设备受理了却没出声，用户得马上知道）。
        // 操作类失败只在队列里展示（操作状态一律不弹窗，见 StatusTab/Persona 的约定）。
        if (seenFailed.current === null) {
          seenFailed.current = new Set(list.filter((it) => it.type === 'broadcast' && it.status === 'failed').map((it) => it.id));
        } else {
          for (const it of list) {
            if (it.type !== 'broadcast' || it.status !== 'failed' || seenFailed.current.has(it.id)) continue;
            seenFailed.current.add(it.id);
            const who = it.machine_name || it.machine_id || '';
            const msg = `${t('bqFailedToast')} · ${who}${it.role_name ? ' · ' + it.role_name : ''}${it.error ? ': ' + it.error : ''}`;
            toastRef.current(msg);
          }
        }
        const want = list.some(isActive) ? RUN_MS : IDLE_MS;
        if (want !== delay) {
          delay = want;
          if (timer) clearInterval(timer);
          timer = setInterval(poll, want);
        }
      } catch { /* 未登录/网络抖动：忽略 */ }
    };
    pollRef.current = poll;
    poll();
    timer = setInterval(poll, IDLE_MS);
    delay = IDLE_MS;
    const off = onMessageQueued(() => { if (alive) poll(); });
    return () => { alive = false; off(); pollRef.current = null; if (timer) clearInterval(timer); };
  }, []);

  // 按机器分组（同机器串行，所以组内顺序就是执行顺序；播报在前、操作在后）
  const groups = useMemo(() => {
    const map = new Map();
    for (const it of items) {
      const key = it.machine_id || it.machine_name || '-';
      if (!map.has(key)) map.set(key, { key, name: it.machine_name || it.machine_id || t('bqNoMachine'), items: [] });
      map.get(key).items.push(it);
    }
    return [...map.values()];
  }, [items]);

  const activeCount = items.filter(isActive).length;
  // 关闭（✕）之后只显示「新入队的」和「新失败的」，这样关掉不会挡住后续队列，
  // 但已经在跑的队列仍然可见（dismissedAt 初始为 0，等于全部可见）。
  const visible = items.some((it) => {
    const enq = it.enqueued_at || it.started_at ? +new Date(it.enqueued_at || it.started_at) : 0;
    if (enq > dismissedAt) return true;
    const fin = it.finished_at ? +new Date(it.finished_at) : 0;
    return it.status === 'failed' && fin > dismissedAt;
  });
  if (!visible) return null;

  const clearQueue = async (machineID) => {
    setClearing(true);
    try {
      const qs = machineID ? '?machine_id=' + encodeURIComponent(machineID) : '';
      await api('/api/broadcast/queue' + qs, { method: 'DELETE' });
    } catch { /* 忽略：下一轮轮询会刷新真实状态 */ }
    setClearing(false);
    if (pollRef.current) pollRef.current();
  };
  // 单条删除（只有待播能删，正在播的删不掉，服务端会返回 removed=false；操作条目不可删）
  const removeItem = async (id) => {
    setClearing(true);
    try { await api('/api/broadcast/queue/' + encodeURIComponent(id), { method: 'DELETE' }); } catch { /* 同上 */ }
    setClearing(false);
    if (pollRef.current) pollRef.current();
  };
  const toggle = (key) => setExpanded((e) => ({ ...e, [key]: !e[key] }));

  return (
    <div style={{ position: 'fixed', right: 16, bottom: 16, zIndex: 900, width: 380, maxWidth: '92vw' }}>
      <div className="card" style={{ padding: 12, boxShadow: '0 8px 28px rgba(0,0,0,.45)' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          <b>📨 {t('mqTitle')}</b>
          <span className="hint">{activeCount ? t('bqActiveN').replace('%s', activeCount) : t('bqIdle')}</span>
          <span style={{ flex: 1 }} />
          {activeCount > 0 && (
            <button className="mini ghost" disabled={clearing} onClick={() => clearQueue('')} title={t('bqClearAll')}>{t('bqClearAll')}</button>
          )}
          <button className="mini ghost" onClick={() => setDismissedAt(Date.now())}>✕</button>
        </div>

        <div style={{ maxHeight: '46vh', overflowY: 'auto', marginTop: 6 }}>
          {groups.map((g) => {
            const pending = g.items.filter((it) => it.type === 'broadcast' && it.status === 'pending').length;
            const running = g.items.filter(isActive).length;
            const failed = g.items.filter((it) => it.status === 'failed').length;
            const open = expanded[g.key] !== false; // 默认展开：单条删除/状态不用先点开分组
            return (
              <div key={g.key} style={{ borderTop: '1px solid var(--line-soft)', paddingTop: 6, marginTop: 6 }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: 6, cursor: 'pointer' }} onClick={() => toggle(g.key)}>
                  <span className="hint">{open ? '▾' : '▸'}</span>
                  <b style={{ fontSize: 13.5 }}>{g.name}</b>
                  <span className="hint">
                    {pending ? `${pending} ${t('bqPending')}` : ''}
                    {running ? `${pending ? ' · ' : ''}${running} ${t('bqRunning')}` : ''}
                    {failed ? `${pending || running ? ' · ' : ''}${failed} ${t('bqFailed')}` : ''}
                  </span>
                  <span style={{ flex: 1 }} />
                  {pending > 0 && (
                    <button className="mini ghost" disabled={clearing} onClick={(e) => { e.stopPropagation(); clearQueue(g.key); }}>{t('bqClearGroup')}</button>
                  )}
                </div>
                {open && g.items.map((it) => {
                  const hint = pollHint(it);
                  return (
                  <div key={it.id} style={{ padding: '6px 0 6px 18px' }}>
                    <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
                      <span className={statusClass(it.status)}>{statusLabel(it.status)}</span>
                      {it.type === 'broadcast' && it.status === 'done' && it.error && <span className="badge warn">{t('bqUnverified')}</span>}
                      <b style={{ fontSize: 13 }}>{itemTitle(it)}</b>
                      {it.role_name && it.type === 'op' && <span className="hint">{it.role_name}</span>}
                      {hint && <span className="hint">{hint}</span>}
                      {it.slot > 0 && <span className="hint">slot {it.slot}</span>}
                      {it.type === 'broadcast' && <span className="hint">{t(SOURCE_KEY[it.source] || 'bqSrcManual')}</span>}
                      <span style={{ flex: 1 }} />
                      {it.type === 'broadcast' && it.status === 'pending' && (
                        <button className="mini ghost" disabled={clearing} title={t('bqDeleteItem')}
                          onClick={() => removeItem(it.id)}>🗑</button>
                      )}
                    </div>
                    {it.type === 'broadcast' && it.preview && <div className="hint" style={{ marginTop: 2, whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>{it.preview}</div>}
                    {it.error && (
                      <pre className={it.status === 'failed' ? 'badge bad' : 'badge warn'}
                        style={{ marginTop: 4, whiteSpace: 'pre-wrap' }}>{it.error}</pre>
                    )}
                  </div>
                  );
                })}
              </div>
            );
          })}
        </div>
      </div>
    </div>
  );
}