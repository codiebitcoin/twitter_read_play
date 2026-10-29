import { useEffect, useRef, useState } from 'react';
import { api } from '../../api.js';
import { t } from '../../i18n.js';
import { useToastErr, useToastOk } from '../../toast.jsx';
import { notifyMessageQueued } from '../../messageQueue.js';
import { cmpIDDesc } from './shared.js';
import TweetCard from './TweetCard.jsx';

// hasPendingTranslation 是否还有需要补翻译的推文（纯媒体推文无正文，永远不会有翻译）。
// 只在用户点「刷新推文」后用来判断翻译补显是否结束，避免无脑轮询。
const hasPendingTranslation = (rows) => rows.some((tw) => {
  const text = (tw.edited || tw.text || tw.quoted_text || tw.retweet_text || '').trim();
  return text !== '' && !tw.translation;
});

// ---- Feed：原推文（broadcast 界面） ----
// 无勾选：主列表就是全量帖子的正常分页；
// Random=CheckedOnly 时顶部多一个 Checked（随机池）区，卡片上可挂/摘（toggleSel）;
// 关键字搜索走后端 kw 过滤，与游标分页兼容。
export default function FeedTab({ role }) {
  const PAGE = 50;
  const [tweets, setTweets] = useState(null);
  const [checked, setChecked] = useState(null);       // random 池（仅 random_only_checked 时拉取/展示）
  const [checkedTotal, setCheckedTotal] = useState(0); // 池总数（列表分页只加载前几页）
  const [checkedNoMore, setCheckedNoMore] = useState(true);
  const [checkedBusy, setCheckedBusy] = useState(false);
  const [bgPool, setBgPool] = useState(null);         // parody 背景池（数据与 random 隔离）
  const [bgTotal, setBgTotal] = useState(0);
  const [bgNoMore, setBgNoMore] = useState(true);
  const [bgBusy, setBgBusy] = useState(false);
  const [kw, setKw] = useState('');             // 生效中的搜索关键字
  const [kwInput, setKwInput] = useState('');
  const [err, setErr] = useState('');
  const [loadingMore, setLoadingMore] = useState(false);
  const [noMore, setNoMore] = useState(false);
  const toastOk = useToastOk();
  const toastErr = useToastErr();

  const fetchPage = async (before = '', force = false, query = kw) => {
    const qs = new URLSearchParams();
    if (before) qs.set('before', before);
    qs.set('limit', PAGE);
    if (query) qs.set('kw', query);
    if (force) qs.set('force', '1');
    return api(`/api/roles/${encodeURIComponent(role.id)}/tweets?${qs}`);
  };

  // 翻译补显轮询：只在用户点「刷新推文」后启动，最多 5 次 × 3s，翻译补齐即停。
  // 这里只读 SQLite 缓存（不产生上游计费）；不点按钮就完全不发请求，绝不常驻轮询。
  const trPollRef = useRef(null);
  const stopTrPoll = () => {
    if (trPollRef.current) { clearInterval(trPollRef.current); trPollRef.current = null; }
  };
  // 池区也是游标分页（before=本页最后一条 tid）+ 单独一次 COUNT：
  // 勾选上千条时不再一次渲染上千张卡片，标题仍显示真实总数。
  const poolPage = (pool, before = '') => {
    const qs = new URLSearchParams({ checked: '1', pool, limit: String(PAGE) });
    if (before) qs.set('before', before);
    return api(`/api/roles/${encodeURIComponent(role.id)}/tweets?${qs}`);
  };
  const poolCount = async (pool) => {
    const res = await api(`/api/roles/${encodeURIComponent(role.id)}/tweets?checked=1&pool=${pool}&count=1`);
    return (res && res.total) || 0;
  };
  const loadChecked = async () => {
    if (role.random_only_checked) {
      try {
        const [rows, total] = await Promise.all([poolPage('random'), poolCount('random')]);
        setChecked(rows); setCheckedTotal(total); setCheckedNoMore(rows.length >= total);
      } catch (_) {}
    }
    if (role.parody_enabled) {
      try {
        const [rows, total] = await Promise.all([poolPage('parody'), poolCount('parody')]);
        setBgPool(rows); setBgTotal(total); setBgNoMore(rows.length >= total);
      } catch (_) {}
    }
  };
  // loadMorePool 池区「加载更早的推文」：pool=random|parody，只翻本地缓存（不产生上游计费）。
  const loadMorePool = async (pool) => {
    const isBg = pool === 'parody';
    const list = isBg ? bgPool : checked;
    if (!list?.length || (isBg ? bgBusy : checkedBusy) || (isBg ? bgNoMore : checkedNoMore)) return;
    (isBg ? setBgBusy : setCheckedBusy)(true);
    try {
      const more = await poolPage(pool, list[list.length - 1].id);
      (isBg ? setBgPool : setChecked)((cs) => [...cs, ...more]);
      if (more.length < PAGE) (isBg ? setBgNoMore : setCheckedNoMore)(true);
    } catch (e) { setErr(e.message); }
    (isBg ? setBgBusy : setCheckedBusy)(false);
  };
  const load = async (force = false, query = kw) => {
    if (force) setTweets(null);
    setErr('');
    try {
      // 默认读 SQLite 缓存秒开；force=1 才会同步拉上游（用户点「刷新推文」）
      const rows = await fetchPage('', force, query);
      setTweets(rows);
      setNoMore(false);
      return rows;
    } catch (e) { setErr(e.message); return null; }
  };
  const pollTranslations = () => {
    stopTrPoll();
    let ticks = 0;
    trPollRef.current = setInterval(async () => {
      ticks += 1;
      const rows = await load(false);
      if (ticks >= 5 || (rows && !hasPendingTranslation(rows))) stopTrPoll();
    }, 3000);
  };
  // refresh 用户点「刷新推文」：拉上游最新（计费），随后短时补显后台翻译。
  const refresh = async () => {
    stopTrPoll();
    await load(true);
    loadChecked();
    pollTranslations();
  };
  const loadMore = async () => {
    if (!tweets?.length || loadingMore || noMore) return;
    stopTrPoll(); // 用户翻页时不要被补显轮询覆盖回第一页
    setLoadingMore(true);
    setErr('');
    try {
      const qs = new URLSearchParams({ before: tweets[tweets.length - 1].id, limit: PAGE, more: '1' });
      if (kw) qs.set('kw', kw);
      const more = await api(`/api/roles/${encodeURIComponent(role.id)}/tweets?${qs}`);
      setTweets((cur) => [...cur, ...more]);
      if (more.length < PAGE) setNoMore(true);
    } catch (e) { setErr(e.message); }
    setLoadingMore(false);
  };
  useEffect(() => {
    load(false);
    loadChecked();
    return () => stopTrPoll();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [role.id, role.random_only_checked]);

  // 关键字搜索：走后端 kw 过滤，重置到第一页;清除后回到全量分页。
  const applySearch = async () => {
    const q = kwInput.trim();
    stopTrPoll();
    setKw(q);
    setNoMore(false);
    await load(false, q);
  };
  const clearSearch = async () => {
    setKwInput('');
    stopTrPoll();
    setKw('');
    setNoMore(false);
    await load(false, '');
  };

  // 注意：api() 对 JSON 错误体（如 {error:"tweet has no readable text..."}）不抛异常，
  // 必须显式判断 res.error，否则失败也弹 ✓（用户表现为「点了没反应/播放不了」）。
  const pollNow = async () => {
    try {
      const res = await api(`/api/roles/${role.id}/poll`, { method: 'POST' });
      if (res && res.error) toastErr(res.error); else notifyMessageQueued();
    } catch (e) { toastErr(e.message); }
  };
  const broadcastRandom = async () => {
    try {
      const res = await api(`/api/roles/${role.id}/broadcast`, { method: 'POST', body: '{}' });
      if (res && res.error) toastErr(res.error);
      else { toastOk(t('bqQueued').replace('%s', res.queued || 0)); notifyMessageQueued(); }
    } catch (e) { toastErr(e.message); }
  };
  const broadcast = async (tw) => {
    try {
      const res = await api(`/api/roles/${role.id}/broadcast`, { method: 'POST', body: JSON.stringify({ tweet_id: tw.id }) });
      if (res && res.error) toastErr(res.error);
      else { toastOk(t('bqQueued').replace('%s', res.queued || 0)); notifyMessageQueued(); }
    } catch (e) { toastErr(e.message); }
  };
  const [editing, setEditing] = useState('');
  const [editText, setEditText] = useState('');
  const startEdit = (tw) => {
    setEditing(tw.id);
    setEditText(tw.edited || tw.text || '');
  };
  const saveEdit = async (tw) => {
    try {
      await api(`/api/roles/${role.id}/tweets/edit`, { method: 'POST', body: JSON.stringify({ id: tw.id, text: editText }) });
      setTweets((ts) => ts.map((x) => (x.id === tw.id ? { ...x, edited: editText.trim(), text: x.text } : x)));
      setChecked((cs) => (cs ? cs.map((x) => (x.id === tw.id ? { ...x, edited: editText.trim(), text: x.text } : x)) : cs));
      setEditing('');
      toastOk(t('saved'));
    } catch (e) { toastErr(e.message); }
  };
  // 挂/摘池。pool: random（selected）| parody（selected_parody），两池数据隔离。
  // 主列表与顶部池区都用它：主列表改本行状态，池区还要同步该池的列表。
  // wasHooked 取卡片复选框的当前值（tw.selected/selected_parody），据此把池总数 ±1 ——
  // 勾上的旧推文可能不在已加载的那一页里，只按列表长度算计数会错。
  const toggleSel = async (tw, sel, pool) => {
    const wasHooked = pool === 'parody' ? !!tw.selected_parody : tw.selected !== false;
    try {
      await api(`/api/roles/${role.id}/tweets/select`, { method: 'POST', body: JSON.stringify({ id: tw.id, selected: sel, pool }) });
      // 主列表：把这一行的池状态改掉（复选框要立刻反映）
      setTweets((ts) => (ts ? ts.map((x) => (x.id === tw.id
        ? (pool === 'parody' ? { ...x, selected_parody: sel } : { ...x, selected: sel }) : x)) : ts));
      const setPool = pool === 'parody' ? setBgPool : setChecked;
      setPool((cs) => (sel
        ? (cs || []).some((x) => x.id === tw.id) ? cs : [...(cs || []), { ...tw, selected: true, selected_parody: pool === 'parody' }].sort(cmpIDDesc)
        : (cs || []).filter((x) => x.id !== tw.id)));
      if (sel !== wasHooked) (pool === 'parody' ? setBgTotal : setCheckedTotal)((n) => Math.max(0, n + (sel ? 1 : -1)));
    } catch (e) { toastErr(e.message); }
  };

  // renderTweet 单条推文卡片（展示部分走共享 TweetCard，与勾选池的卡片布局一致；
  // 这里只接插槽：左侧挂/摘复选框、底部编辑/播报按钮、编辑态接线）。
  const renderTweet = (tw, hookPool = '') => {
    const left = hookPool ? (
      <input type="checkbox" checked title="挂钩/摘出该池" style={{ marginTop: 10, flexShrink: 0 }}
        onChange={(e) => toggleSel(tw, e.target.checked, hookPool)} />
    ) : (
      /* 主列表：勾选直接挂/摘池 —— random 池（参与随机播报，selected）
         与 parody 背景池（selected_parody，开了 parody 模式才显示）。 */
      <>
        <input type="checkbox" checked={tw.selected !== false} title={t('selHint')} style={{ marginTop: 10, flexShrink: 0 }}
          onChange={(e) => toggleSel(tw, e.target.checked, 'random')} />
        {role.parody_enabled && (
          <input type="checkbox" checked={!!tw.selected_parody} title={t('bgPoolHook')} style={{ marginTop: 10, flexShrink: 0 }}
            onChange={(e) => toggleSel(tw, e.target.checked, 'parody')} />
        )}
      </>
    );
    return (
      <TweetCard key={tw.id} tw={tw} role={role} left={left}
        editing={editing === tw.id} editText={editText}
        onEditText={setEditText}
        onSaveEdit={() => saveEdit(tw)}
        onCancelEdit={() => setEditing('')}
        onResetEdit={async () => {
          try {
            await api(`/api/roles/${role.id}/tweets/edit`, { method: 'POST', body: JSON.stringify({ id: tw.id, text: '' }) });
            setTweets((ts) => ts.map((x) => (x.id === tw.id ? { ...x, edited: '' } : x)));
            setChecked((cs) => (cs ? cs.map((x) => (x.id === tw.id ? { ...x, edited: '' } : x)) : cs));
            setEditing('');
            toastOk(t('saved'));
          } catch (e) { toastErr(e.message); }
        }}
        footer={<>
          <button className="mini ghost" onClick={() => startEdit(tw)}>{t('edit')}</button>
          <button className="mini" onClick={() => broadcast(tw)}>{t('broadcastThis')}</button>
        </>} />
    );
  };

  return (
    <>
      <div className="bar">
        <button className="mini" onClick={refresh}>{t('refreshTweets')}</button>
        <button className="mini" onClick={pollNow}>{t('pollNow')}</button>
        <button className="mini" onClick={broadcastRandom}>{t('broadcastRandom')}</button>
        <input className="mini" style={{ maxWidth: 220 }} value={kwInput}
          onChange={(e) => setKwInput(e.target.value)}
          onKeyDown={(e) => { if (e.key === 'Enter') applySearch(); }}
          placeholder={t('searchTweetsPh')} />
        <button className="mini" disabled={!kwInput.trim()} onClick={applySearch}>{t('searchTweets')}</button>
        {kw && <button className="mini ghost" onClick={clearSearch}>{t('searchClear')}</button>}
        {kw && <span className="hint">🔍 {kw}</span>}
        <span className="hint">{t('tweetHint')}</span>
      </div>
      {err ? <p className="err-banner">{err}</p>
        : !tweets ? <p className="loading">{t('loading')}</p>
        : (
          <>
            {/* 顶部固定显示各勾选池（可挂/摘,数据隔离）,下面才是全量分页 */}
            {role.random_only_checked && (
              <div style={{ marginBottom: 14 }}>
                <div className="bar" style={{ padding: '4px 0' }}>
                  <b style={{ fontSize: 13 }}>✅ {t('checkedSec')}</b>
                  <span className="hint">
                    {checkedTotal > (checked?.length || 0) ? `${checked?.length || 0} / ${checkedTotal}` : checkedTotal} · {t('checkedSecHint')}
                  </span>
                </div>
                {checked?.length ? checked.map((tw) => renderTweet(tw, 'random'))
                  : <p className="hint" style={{ margin: '4px 0 10px' }}>∅</p>}
                {checked?.length >= PAGE || checkedTotal > (checked?.length || 0) ? (
                  <div style={{ textAlign: 'center', padding: '4px 0' }}>
                    {checkedNoMore ? <span className="hint">{t('poolNoMore')}</span>
                      : <button className="mini" disabled={checkedBusy} onClick={() => loadMorePool('random')}>{checkedBusy ? '…' : t('loadMoreTweets')}</button>}
                  </div>
                ) : null}
              </div>
            )}
            {role.parody_enabled && (
              <div style={{ marginBottom: 14 }}>
                <div className="bar" style={{ padding: '4px 0' }}>
                  <b style={{ fontSize: 13 }}>🎬 {t('bgPoolSec')}</b>
                  <span className="hint">
                    {bgTotal > (bgPool?.length || 0) ? `${bgPool?.length || 0} / ${bgTotal}` : bgTotal} · {t('bgPoolHint')}
                  </span>
                </div>
                {bgPool?.length ? bgPool.map((tw) => renderTweet(tw, 'parody'))
                  : <p className="hint" style={{ margin: '4px 0 10px' }}>∅</p>}
                {bgPool?.length >= PAGE || bgTotal > (bgPool?.length || 0) ? (
                  <div style={{ textAlign: 'center', padding: '4px 0' }}>
                    {bgNoMore ? <span className="hint">{t('poolNoMore')}</span>
                      : <button className="mini" disabled={bgBusy} onClick={() => loadMorePool('parody')}>{bgBusy ? '…' : t('loadMoreTweets')}</button>}
                  </div>
                ) : null}
              </div>
            )}
            {tweets.length ? tweets.map((tw) => renderTweet(tw))
              : <p className="empty"><span className="icon">🐦</span>{t('noTweets')}</p>}
          </>
        )}
      {tweets?.length ? (
        <div style={{ textAlign: 'center', padding: '10px 0 4px' }}>
          {noMore
            ? <span className="hint">{t('noMoreTweets')}</span>
            : <button className="mini" disabled={loadingMore} onClick={loadMore}>{loadingMore ? '…' : t('loadMoreTweets')}</button>}
        </div>
      ) : null}
    </>
  );
}
