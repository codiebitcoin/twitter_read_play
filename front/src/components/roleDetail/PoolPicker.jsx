import { useEffect, useState } from 'react';
import { api } from '../../api.js';
import { t } from '../../i18n.js';
import { useToastErr } from '../../toast.jsx';
import { cmpIDDesc } from './shared.js';
import TweetCard from './TweetCard.jsx';

// PoolPicker 勾选池选择器（random 池 / parody 背景池共用,数据隔离由 pool 参数保证）:
//   上半:当前池里的推文列表（与「原推文」页同一张 TweetCard 卡片,可摘出）
//   下半:tweet 筛选框(关键字走后端 kw 过滤) → 挂入池,出现在上面的 list 里
//
// 两个列表都按游标分页（before = 本页最后一条的 tid），不再把搜索结果硬截断在 50 条:
// 每页 PAGE 条 + 「加载更早的推文」按钮,可以一直往回翻本地缓存（不产生上游计费）。
// 总数由后端 count=1 单独 COUNT 一次,标题显示「已加载 x / 共 N 条」。
const PAGE = 50;

export default function PoolPicker({ role, pool }) {
  const toastErr = useToastErr();
  const [poolList, setPoolList] = useState(null);
  const [poolTotal, setPoolTotal] = useState(0);
  const [poolFetched, setPoolFetched] = useState(0); // 已按页取回的条数（摘出后翻页按钮不该消失）
  const [poolNoMore, setPoolNoMore] = useState(false);
  const [poolBusy, setPoolBusy] = useState(false);
  const [results, setResults] = useState(null); // 搜索结果(null=未搜索)
  const [resTotal, setResTotal] = useState(0);
  const [resNoMore, setResNoMore] = useState(false);
  const [resBusy, setResBusy] = useState(false);
  const [kw, setKw] = useState('');               // 输入框内容
  const [appliedKw, setAppliedKw] = useState(''); // 当前结果对应的关键字（输入框改动不立刻生效）

  const roleID = encodeURIComponent(role.id);
  const hookField = pool === 'parody' ? 'selected_parody' : 'selected';

  // poolURL/searchURL 统一拼查询串：before/limit 游标分页，count=1 只取总数。
  const poolURL = ({ before = '', limit = 0, count = false } = {}) => {
    const p = new URLSearchParams({ checked: '1', pool });
    if (before) p.set('before', before);
    if (limit) p.set('limit', String(limit));
    if (count) p.set('count', '1');
    return `/api/roles/${roleID}/tweets?${p}`;
  };
  const searchURL = ({ q = '', before = '', limit = 0, count = false } = {}) => {
    const p = new URLSearchParams();
    if (q) p.set('kw', q);
    if (before) p.set('before', before);
    if (limit) p.set('limit', String(limit));
    if (count) p.set('count', '1');
    return `/api/roles/${roleID}/tweets?${p}`;
  };

  // 第一页行数据 + 总数并发拉取（都只读 SQLite 缓存）。
  const loadPool = async () => {
    setPoolBusy(true);
    try {
      const [rows, cnt] = await Promise.all([api(poolURL({ limit: PAGE })), api(poolURL({ count: true }))]);
      const total = (cnt && cnt.total) || 0;
      setPoolList(rows);
      setPoolTotal(total || rows.length);
      setPoolFetched(rows.length);
      setPoolNoMore(rows.length >= (total || rows.length));
    } catch (e) { toastErr(e.message); setPoolList([]); }
    setPoolBusy(false);
  };
  useEffect(() => { loadPool(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [role.id, pool]);

  const loadMorePool = async () => {
    if (!poolList?.length || poolBusy || poolNoMore) return;
    setPoolBusy(true);
    try {
      const more = await api(poolURL({ before: poolList[poolList.length - 1].id, limit: PAGE }));
      setPoolList((cs) => [...cs, ...more]);
      setPoolFetched((n) => n + more.length);
      if (more.length < PAGE || poolList.length + more.length >= poolTotal) setPoolNoMore(true);
    } catch (e) { toastErr(e.message); }
    setPoolBusy(false);
  };

  const search = async () => {
    const q = kw.trim();
    if (!q) { setResults(null); setAppliedKw(''); return; }
    setResBusy(true);
    try {
      const [rows, cnt] = await Promise.all([api(searchURL({ q, limit: PAGE })), api(searchURL({ q, count: true }))]);
      const total = (cnt && cnt.total) || 0;
      setResults(rows);
      setResTotal(total || rows.length);
      setResNoMore(rows.length >= (total || rows.length));
      setAppliedKw(q);
    } catch (e) { toastErr(e.message); }
    setResBusy(false);
  };
  const loadMoreResults = async () => {
    if (!results?.length || resBusy || resNoMore) return;
    setResBusy(true);
    try {
      const more = await api(searchURL({ q: appliedKw, before: results[results.length - 1].id, limit: PAGE }));
      setResults((rs) => [...rs, ...more]);
      if (more.length < PAGE || results.length + more.length >= resTotal) setResNoMore(true);
    } catch (e) { toastErr(e.message); }
    setResBusy(false);
  };
  const clearSearch = () => { setKw(''); setAppliedKw(''); setResults(null); };

  const hookedIDs = new Set((poolList || []).map((x) => x.id));

  // 挂/摘:调 select API(带 pool),同步两边列表
  const toggle = async (tw, sel) => {
    const wasHooked = hookedIDs.has(tw.id);
    try {
      await api(`/api/roles/${roleID}/tweets/select`, { method: 'POST', body: JSON.stringify({ id: tw.id, selected: sel, pool }) });
      setPoolList((cs) => {
        const cur = cs || [];
        if (!sel) return cur.filter((x) => x.id !== tw.id);
        if (cur.some((x) => x.id === tw.id)) return cur;
        return [...cur, { ...tw, [hookField]: true }].sort(cmpIDDesc);
      });
      // 计数跟着 ±1:挂进来的旧推文可能落在已加载页之外,列表看不到但总数要准
      if (sel !== wasHooked) setPoolTotal((n) => Math.max(0, n + (sel ? 1 : -1)));
      setResults((rs) => (rs ? rs.map((x) => (x.id === tw.id ? { ...x, [hookField]: sel } : x)) : rs));
    } catch (e) { toastErr(e.message); }
  };

  // loadedOf 分页进度文案:「已加载 x / 共 N 条」
  const loadedOf = (a, b) => t('poolLoadedOf').replace('{a}', a).replace('{b}', b);

  return (
    <div className="card" style={{ margin: '10px 0 0', padding: 10 }}>
      {/* 当前池 list：与「原推文」页同一张卡片布局（头像 / 昵称@handle / 正文 / 引用 / 时间） */}
      <div className="bar" style={{ padding: '2px 0' }}>
        <b style={{ fontSize: 13 }}>✅ {t('poolCurrent')}</b>
        <span className="hint">
          {!poolList ? '' : (poolTotal > poolList.length ? loadedOf(poolList.length, poolTotal) : String(poolTotal))}
        </span>
      </div>
      {!poolList ? <p className="loading">{t('loading')}</p> : (
        poolList.length ? (
          <>
            <div style={{ maxHeight: 320, overflow: 'auto', display: 'flex', flexDirection: 'column', gap: 8, marginBottom: 10 }}>
              {poolList.map((tw) => (
                <TweetCard key={tw.id} tw={tw} role={role} dense
                  footer={<button className="mini ghost danger" onClick={() => toggle(tw, false)}>✕ {t('poolRemove')}</button>} />
              ))}
            </div>
            {/* 池分页：翻过至少一页（或有下一页）才显示翻页行,避免小池挂个「没有更多了」 */}
            {poolFetched >= PAGE || !poolNoMore ? (
              <div style={{ textAlign: 'center', marginBottom: 10 }}>
                {poolNoMore ? <span className="hint">{t('poolNoMore')}</span>
                  : <button className="mini" disabled={poolBusy} onClick={loadMorePool}>{poolBusy ? '…' : t('loadMoreTweets')}</button>}
              </div>
            ) : null}
          </>
        ) : <p className="hint" style={{ margin: '4px 0 10px' }}>∅ {t('poolEmpty')}</p>
      )}

      {/* tweet 筛选框 */}
      <div className="bar" style={{ padding: '2px 0' }}>
        <input className="mini" style={{ maxWidth: 240 }} value={kw}
          onChange={(e) => setKw(e.target.value)}
          onKeyDown={(e) => { if (e.key === 'Enter') search(); }}
          placeholder={t('poolSearchPh')} />
        <button className="mini" disabled={resBusy || !kw.trim()} onClick={search}>{resBusy ? '…' : t('searchTweets')}</button>
        {results && <button className="mini ghost" onClick={clearSearch}>{t('searchClear')}</button>}
        {results && <span className="hint">🔍 {appliedKw} · {loadedOf(results.length, resTotal)}</span>}
      </div>
      {results && (
        results.length ? (
          <>
            <div style={{ maxHeight: 360, overflow: 'auto', display: 'flex', flexDirection: 'column', gap: 8 }}>
              {results.map((tw) => {
                const hooked = hookedIDs.has(tw.id) || tw[hookField];
                return (
                  <TweetCard key={tw.id} tw={tw} role={role} dense
                    footer={hooked
                      ? <span className="badge ok">✓ {t('poolHooked')}</span>
                      : <button className="mini" onClick={() => toggle(tw, true)}>+ {t('poolAdd')}</button>} />
                );
              })}
            </div>
            {results.length >= PAGE ? (
              <div style={{ textAlign: 'center', marginTop: 8 }}>
                {resNoMore ? <span className="hint">{t('poolNoMore')}</span>
                  : <button className="mini" disabled={resBusy} onClick={loadMoreResults}>{resBusy ? '…' : t('loadMoreTweets')}</button>}
              </div>
            ) : null}
          </>
        ) : <p className="hint" style={{ margin: '6px 0 0' }}>∅ {t('noTweets')}</p>
      )}
    </div>
  );
}
