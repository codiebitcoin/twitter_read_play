import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { api } from '../../api.js';
import { t } from '../../i18n.js';
import { useToastErr, useToastOk } from '../../toast.jsx';
import { notifyMessageQueued } from '../../messageQueue.js';
import { getBookPrefs, setBookPrefs } from '../../bookPref.js';

// ---- Book 页（与 Feed / Stream 同级）：上传图书 → 自动拆章节 → 点章 / 点段 / 随机播放 ----
//
// 书归属当前角色、不跨角色共享，所以所有图书操作都走角色路由。
// 数据流：
//   GET  /api/books/state?role_id=              本角色书库 + 进度 + 本角色播放状态 + 未归属旧书（轮询）
//   POST /api/roles/:id/books (multipart)       上传（后端解码 + 拆分 + 落章文件，自动归属）
//   GET  /api/roles/:id/books/:book/chapters/:i 章节正文（段落数组，点段播放用）
//   POST /api/roles/:id/books/claim             认领一本未归属的旧书
//   POST /api/roles/:id/book/play               {book_id, chapter, paragraph?} 章 / 段
//   POST /api/roles/:id/book/random             随机一段
//   POST /api/roles/:id/book/stop               停止
//   POST /api/roles/:id/book/progress           设置断点（跳转）
const PAGE_CH = 300;         // 章节列表一次渲染多少（圣经 1900 章，不能一次铺满 DOM）
const PLAYING_POLL_MS = 4000;
const IDLE_POLL_MS = 15000;

export default function BookPanel({ role }) {
  const [state, setState] = useState(null); // {books, progress, runs}
  const [bookId, setBookId] = useState('');
  const [chapter, setChapter] = useState(0);
  const [chapterData, setChapterData] = useState(null); // {chapter, paragraphs, total}
  const [chText, setChText] = useState('');
  const [shown, setShown] = useState(PAGE_CH);
  const [busy, setBusy] = useState('');
  const [opts, setOpts] = useState(getBookPrefs);
  const updateOpts = (next) => { setOpts(next); setBookPrefs(next); };
  const [uploading, setUploading] = useState(false);
  const [drag, setDrag] = useState(false);
  const fileRef = useRef(null);
  const toastOk = useToastOk();
  const toastErr = useToastErr();

  const books = state?.books || [];
  const unassigned = state?.unassigned || [];
  const book = useMemo(() => books.find((b) => b.id === bookId) || null, [books, bookId]);
  const prog = book ? state?.progress?.[book.id] : null;
  // 后端已按角色过滤 runs；这里双保险（旧数据/缓存里可能没有 role_id）。
  const runs = state?.runs
    ? Object.values(state.runs).filter((r) => !r.role_id || r.role_id === role.id)
    : [];
  const playing = runs.find((r) => r.playing);

  const loadState = useCallback(async (silent = true) => {
    try {
      const s = await api(`/api/books/state?role_id=${encodeURIComponent(role.id)}`);
      setState(s);
      return s;
    } catch (e) {
      if (!silent) toastErr(e.message);
      return null;
    }
  }, [role.id]);

  // 首次进入：书库；默认选上次读的那本（有断点的），否则第一本
  useEffect(() => {
    (async () => {
      const s = await loadState(false);
      if (!s) return;
      const withProgress = (s.books || []).find((b) => s.progress?.[b.id]?.chapter > 0);
      const pick = withProgress || (s.books || [])[0];
      if (pick) {
        setBookId(pick.id);
        setChapter(clampCh(pick, s.progress?.[pick.id]?.chapter || 0));
      }
    })();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [role.id]);

  // 播放状态轮询：正在播 4s（进度实时）；空闲 15s（别的页/随机也会动进度）
  useEffect(() => {
    const iv = setInterval(() => loadState(), playing ? PLAYING_POLL_MS : IDLE_POLL_MS);
    return () => clearInterval(iv);
  }, [loadState, playing]);

  const openChapter = useCallback(async (bk, i) => {
    if (!bk) return;
    setChapter(i);
    setChapterData(null);
    try {
      setChapterData(await api(`/api/roles/${encodeURIComponent(role.id)}/books/${encodeURIComponent(bk.id)}/chapters/${i}`));
    } catch (e) { toastErr(e.message); }
  }, [toastErr]);

  // 首次拿到书 / 换书时打开断点所在章
  const openedRef = useRef('');
  useEffect(() => {
    if (!book) return;
    // 换书（或第一次拿到书）才重新决定打开哪一章；轮询更新 books 不打断当前阅读位置
    if (openedRef.current === book.id) return;
    openedRef.current = book.id;
    openChapter(book, clampCh(book, chapter));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [book?.id]);

  const pickBook = (b) => {
    openedRef.current = '';
    setBookId(b.id);
    setChapter(clampCh(b, state?.progress?.[b.id]?.chapter || 0));
    setShown(PAGE_CH);
    setChText('');
  };

  // ---- 播放 ----
  const play = async ({ chapter: ci = chapter, paragraph = null, random = false }) => {
    if (!book) return;
    setBusy(random ? 'random' : 'play');
    try {
      const body = JSON.stringify({
        book_id: book.id, chapter: ci, paragraph, mode: opts.mode, preface: opts.preface,
      });
      const res = await api(`/api/roles/${encodeURIComponent(role.id)}/book/${random ? 'random' : 'play'}`,
        { method: 'POST', body });
      toastOk(t('bookPlayStarted'));
      notifyMessageQueued(); // 让全局消息队列弹窗立刻刷新（书朗读不走推文队列，只刷新标题栏）
      if (res?.run) {
        if (res.run.chapter !== chapter) openChapter(book, res.run.chapter);
        setState((s) => (s ? { ...s, runs: { ...s.runs, [res.run.machine_id]: { ...res.run, playing: true } } } : s));
      }
      loadState();
    } catch (e) { toastErr(e.message); }
    setBusy('');
  };
  const stop = async () => {
    try {
      await api(`/api/roles/${encodeURIComponent(role.id)}/book/stop`, { method: 'POST', body: '{}' });
      toastOk(t('bookStopped'));
      loadState();
    } catch (e) { toastErr(e.message); }
  };
  const jump = async (ci) => {
    if (!book) return;
    try {
      await api(`/api/roles/${encodeURIComponent(role.id)}/book/progress`,
        { method: 'POST', body: JSON.stringify({ book_id: book.id, chapter: ci, paragraph: 0 }) });
      openChapter(book, ci);
      loadState();
    } catch (e) { toastErr(e.message); }
  };

  // ---- 上传 / 删除 / 重新解码 ----
  const doUpload = async (file) => {
    if (!file) return;
    setUploading(true);
    try {
      const fd = new FormData();
      fd.append('file', file);
      const b = await api(`/api/roles/${encodeURIComponent(role.id)}/books`, { method: 'POST', body: fd });
      toastOk(`${t('bookUploaded')}: ${b.title} · ${b.chapters.length} ${t('bookChaptersLabel')}`);
      const s = await loadState();
      if (s) pickBook(b);
      else { openedRef.current = ''; setBookId(b.id); setChapter(0); }
    } catch (e) { toastErr(e.message); }
    setUploading(false);
    if (fileRef.current) fileRef.current.value = '';
  };

  const delBook = async () => {
    if (!book || !confirm(t('bookDeleteConfirm'))) return;
    try {
      await api(`/api/roles/${encodeURIComponent(role.id)}/books/${encodeURIComponent(book.id)}`, { method: 'DELETE' });
      const s = await loadState();
      openedRef.current = '';
      setBookId('');
      setChapterData(null);
      if (s?.books?.length) pickBook(s.books[0]);
    } catch (e) { toastErr(e.message); }
  };

  const redecode = async (enc) => {
    if (!book) return;
    setBusy('redecode');
    try {
      const b = await api(`/api/roles/${encodeURIComponent(role.id)}/books/${encodeURIComponent(book.id)}/redecode`,
        { method: 'POST', body: JSON.stringify({ encoding: enc }) });
      toastOk(`${t('bookRedeCoded')} (${b.txt_encoding})`);
      await loadState();
      openChapter(b, 0);
    } catch (e) { toastErr(e.message); }
    setBusy('');
  };

  // 未归属的旧书：认领到当前角色后才会进入本角色的书库。
  const claim = async (b) => {
    setBusy('claim');
    try {
      const owned = await api(`/api/roles/${encodeURIComponent(role.id)}/books/claim`,
        { method: 'POST', body: JSON.stringify({ book_id: b.id }) });
      toastOk(t('bookClaimed'));
      const s = await loadState();
      openedRef.current = '';
      setBookId(owned.id);
      setChapter(clampCh(owned, s?.progress?.[owned.id]?.chapter || 0));
      setShown(PAGE_CH);
      setChText('');
    } catch (e) { toastErr(e.message); }
    setBusy('');
  };

  // 章节筛选 + 分页渲染
  const chList = useMemo(() => {
    if (!book) return [];
    const q = chText.trim().toLowerCase();
    const all = book.chapters.map((c, i) => ({ ...c, i }));
    return q ? all.filter((c) => (c.title || '').toLowerCase().includes(q)) : all;
  }, [book, chText]);
  const nextCh = prog ? Math.min(prog.chapter, (book?.chapters.length || 1) - 1) : 0;

  return (
    <div className="book-layout">
      {/* 左：上传 + 书库 */}
      <aside className="book-side">
        <div
          className={'book-drop' + (drag ? ' over' : '') + (uploading ? ' busy' : '')}
          onClick={() => !uploading && fileRef.current && fileRef.current.click()}
          onDragOver={(e) => { e.preventDefault(); setDrag(true); }}
          onDragLeave={() => setDrag(false)}
          onDrop={(e) => { e.preventDefault(); setDrag(false); doUpload(e.dataTransfer.files?.[0]); }}
        >
          <input ref={fileRef} type="file" accept=".txt,.md,.markdown,.text,.epub,text/plain,application/epub+zip"
            style={{ display: 'none' }}
            onChange={(e) => doUpload(e.target.files?.[0])} />
          <div className="book-drop-icon">{uploading ? '…' : '📚'}</div>
          <b>{uploading ? t('bookUploading') : t('bookUpload')}</b>
          {!uploading && <span className="hint">{t('bookDropHint')}</span>}
          <span className="hint book-drop-tip">{t('bookUploadHint')}</span>
        </div>

        <div className="book-list">
          <div className="bar" style={{ padding: '4px 0', marginBottom: 4 }}>
            <b style={{ fontSize: 13 }}>{t('bookLibrary')} · {role.name}</b>
            <span className="hint">{books.length}</span>
          </div>
          {books.length === 0 && <p className="hint">{t('bookNoBooks')}</p>}
          {books.map((b) => {
            const p = state?.progress?.[b.id];
            const active = b.id === bookId;
            return (
              <button key={b.id} className={'book-item' + (active ? ' active' : '')} onClick={() => pickBook(b)}>
                <span className="book-item-title">{b.title}</span>
                <span className="hint">
                  {b.chapters.length} {t('bookChaptersLabel')}
                  {p && p.chapter > 0 && (
                    <span style={{ marginLeft: 6 }}>· {t('bookResumeAt')} {p.chapter + 1}</span>
                  )}
                </span>
              </button>
            );
          })}
        </div>

        {/* 升级前的旧书没有归属：认领后才进入当前角色的书库，避免角色之间共享 */}
        {unassigned.length > 0 && (
          <div className="book-list" style={{ marginTop: 10 }}>
            <div className="bar" style={{ padding: '4px 0', marginBottom: 4 }}>
              <b style={{ fontSize: 13 }}>{t('bookUnassigned')}</b>
              <span className="hint">{unassigned.length}</span>
            </div>
            <p className="hint" style={{ margin: '0 0 6px' }}>{t('bookUnassignedHint')}</p>
            {unassigned.map((b) => (
              <button key={b.id} className="book-item" disabled={busy === 'claim'} onClick={() => claim(b)}>
                <span className="book-item-title">{b.title}</span>
                <span className="hint">
                  {b.chapters.length} {t('bookChaptersLabel')} · {t('bookAssignTo')} {role.name}
                </span>
              </button>
            ))}
          </div>
        )}
      </aside>

      {/* 右：当前书 */}
      <section className="book-main">
        {!book ? (
          <p className="empty"><span className="icon">📖</span>{t('bookNoBooks')}</p>
        ) : (
          <>
            <div className="bar book-head">
              <div style={{ minWidth: 0 }}>
                <h3 style={{ margin: 0 }}>{book.title}</h3>
                <div className="hint">
                  {book.author && <>{book.author} · </>}
                  {book.chapters.length} {t('bookChaptersLabel')} · {book.format}
                  {book.txt_encoding ? ` / ${book.txt_encoding}` : ''}
                </div>
              </div>
              <span style={{ flex: 1 }} />
              {playing ? (
                <button className="danger" onClick={stop}>■ {t('bookStop')}</button>
              ) : (
                <button onClick={() => play({ chapter: nextCh })} disabled={busy !== ''}>
                  {nextCh > 0 || (prog && prog.paragraph > 0)
                    ? `▶ ${t('bookResumeAt')} ${nextCh + 1}`
                    : `▶ ${t('bookPlayChapter')}`}
                </button>
              )}
              <button className="secondary" onClick={() => play({ random: true })} disabled={busy !== ''}>
                🎲 {t('bookRandom')}
              </button>
              <button className="mini ghost" title={t('bookDeleteConfirm')} onClick={delBook}>✕</button>
            </div>

            {/* 朗读方式 */}
            <div className="book-opts">
              <label className="inline">
                {t('bookMode')}
                <select value={opts.mode} onChange={(e) => updateOpts({ ...opts, mode: e.target.value })}>
                  <option value="tts">{t('bookModeTTS')}</option>
                  <option value="chat">{t('bookModeChat')}</option>
                </select>
              </label>
              <label className="inline">
                <input type="checkbox" checked={opts.preface}
                  onChange={(e) => updateOpts({ ...opts, preface: e.target.checked })} />
                {t('bookPreface')}
              </label>
              {playing && (
                <span className="book-now">
                  ▶ <b>{playing.book_title}</b> · {playing.chapter + 1}
                  {playing.total > 1 && ` · ${playing.pos}/${playing.total}`}
                  <span className="book-now-text">{playing.text}</span>
                </span>
              )}
              {playing && playing.error && <span className="badge bad">{playing.error}</span>}
            </div>

            <div className="book-cols">
              {/* 章节列表 */}
              <div className="book-chapters">
                <input className="mini" value={chText} placeholder={t('bookFilterCh')}
                  onChange={(e) => { setChText(e.target.value); setShown(PAGE_CH); }} />
                <div className="book-chscroll">
                  {chList.slice(0, shown).map((c) => {
                    const isCur = c.i === chapter;
                    const isNext = c.i === nextCh;
                    return (
                      <div key={c.i}
                        className={'book-ch' + (isCur ? ' active' : '') + (isNext ? ' next' : '')}
                        title={c.title}
                        onClick={() => jump(c.i)}>
                        <span className="book-ch-no">{c.i + 1}</span>
                        <span className="book-ch-title">{c.title || '—'}</span>
                        <span className="book-ch-meta">{c.chars}</span>
                        <button className="mini ghost book-ch-play" title={t('bookPlayChapter')}
                          onClick={(e) => { e.stopPropagation(); play({ chapter: c.i }); }}>▶</button>
                      </div>
                    );
                  })}
                  {chList.length === 0 && <p className="hint" style={{ padding: 8 }}>{t('bookNoChMatch')}</p>}
                  {chList.length > shown && (
                    <div style={{ textAlign: 'center', padding: 6 }}>
                      <button className="mini ghost" onClick={() => setShown((n) => n + PAGE_CH)}>
                        … {shown} / {chList.length}
                      </button>
                    </div>
                  )}
                </div>
              </div>

              {/* 章节正文：逐段点播 */}
              <div className="book-body">
                {!chapterData ? <p className="hint">{t('bookLoadingCh')}</p> : (
                  <>
                    <div className="bar" style={{ padding: '2px 0' }}>
                      <b>{chapterData.chapter.title}</b>
                      <span className="hint">{chapterData.total} · {chapterData.chapter.chars} {t('bookCharsLabel')}</span>
                      <span style={{ flex: 1 }} />
                      <button className="mini" onClick={() => play({ chapter })}>▶ {t('bookPlayChapter')}</button>
                    </div>
                    {chapterData.paragraphs.map((p, i) => (
                      <div key={i} className={'book-para' + (chapter < nextCh || (chapter === nextCh && i < (prog?.paragraph || 0)) ? ' read' : '')}>
                        <button className="mini ghost book-para-play" title={t('bookParaPlay')}
                          onClick={() => play({ chapter, paragraph: i })}>▶</button>
                        <div className="book-para-text" onClick={() => play({ chapter, paragraph: i })}>{p}</div>
                      </div>
                    ))}
                  </>
                )}
              </div>
            </div>

            {/* 编码救急：乱码时重新解码 */}
            {book.format !== 'epub' && (
              <details className="book-redecode">
                <summary className="hint">{t('bookRedeCode')}…</summary>
                <p className="hint" style={{ margin: '6px 0' }}>{t('bookRedeCodeHint')}</p>
                <div className="bar" style={{ margin: 0 }}>
                  {['auto', 'utf-8', 'gb18030', 'big5', 'utf-16le'].map((enc) => (
                    <button key={enc} className="mini secondary" disabled={busy === 'redecode'}
                      onClick={() => redecode(enc)}>{enc}</button>
                  ))}
                </div>
              </details>
            )}
          </>
        )}
      </section>
    </div>
  );
}

// clampCh 章节序号夹到书内（断点可能因重新拆分成更多/更少章而越界）。
function clampCh(b, i) {
  const n = b?.chapters?.length || 0;
  if (n === 0) return 0;
  return Math.max(0, Math.min(n - 1, i || 0));
}
