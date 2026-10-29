import { t } from '../../i18n.js';
import { platformLabel as locPlatformLabel } from '../common/adapters.js';

// parseTweetDate 宽口径时间解析：各上游 created_at 格式不一（后端 parseTweetTime 的前端对应）。
// 支持 ISO/RFC3339、RFC1123、Twitter 经典格式（V8 可解析）、纯数字时间戳（秒/毫秒）、
// 微博中文格式（刚刚 / N分钟前 / 今天 08:00 / 昨天 22:15 / 10月15日 09:54）。
// 解析不出来返回 null（调用方原样显示 created_at）。
export function parseTweetDate(s) {
  if (!s) return null;
  const v = String(s).trim();
  if (!v) return null;
  // 纯数字时间戳（10 位秒 / 13 位毫秒）：先判，避免被 Date 当成年份
  if (/^\d{10}$/.test(v)) return new Date(Number(v) * 1000);
  if (/^\d{13}$/.test(v)) return new Date(Number(v));
  const now = new Date();
  if (v === '刚刚' || v === '刚才') return now;
  let m = v.match(/^(\d+)\s*(秒|分钟|小时|天)前$/);
  if (m) {
    const unit = { 秒: 1000, 分钟: 60000, 小时: 3600000, 天: 86400000 }[m[2]];
    return new Date(now.getTime() - Number(m[1]) * unit);
  }
  m = v.match(/^(今天|昨天|前天)\s*(\d{1,2}):(\d{2})(?::(\d{2}))?$/);
  if (m) {
    const d = new Date(now);
    if (m[1] === '昨天') d.setDate(d.getDate() - 1);
    if (m[1] === '前天') d.setDate(d.getDate() - 2);
    d.setHours(Number(m[2]), Number(m[3]), Number(m[4] || 0), 0);
    return d;
  }
  m = v.match(/^(?:(\d{4})年)?(\d{1,2})月(\d{1,2})日(?:\s*(\d{1,2}):(\d{2})(?::(\d{2}))?)?$/);
  if (m) {
    const year = m[1] ? Number(m[1]) : now.getFullYear();
    const d = new Date(year, Number(m[2]) - 1, Number(m[3]), Number(m[4] || 0), Number(m[5] || 0), Number(m[6] || 0));
    if (isNaN(d)) return null;
    // 微博只写月日：算出来比现在晚一天以上 → 其实是去年的
    if (!m[1] && d.getTime() - now.getTime() > 86400000) d.setFullYear(year - 1);
    return d;
  }
  const d = new Date(v);
  return isNaN(d) ? null : d;
}

// fmtTweetTimeAbs 发帖时间到分钟：M/D HH:mm（跨年带年份）；relative 作 tooltip
export function fmtTweetTimeAbs(iso) {
  const d = parseTweetDate(iso);
  if (!d) return iso;
  const p = (n) => String(n).padStart(2, '0');
  const now = new Date();
  const md = `${d.getMonth() + 1}/${d.getDate()} ${p(d.getHours())}:${p(d.getMinutes())}`;
  return d.getFullYear() === now.getFullYear() ? md : `${d.getFullYear()}/${md}`;
}

// fmtTweetTime X 风格相对时间（<24h 用 s/m/h，否则 M/D 或 yyyy/M/D）
export function fmtTweetTime(iso) {
  const d = parseTweetDate(iso);
  if (!d) return iso;
  const diff = (Date.now() - d.getTime()) / 1000;
  if (diff < 60) return Math.max(1, Math.floor(diff)) + 's';
  if (diff < 3600) return Math.floor(diff / 60) + 'm';
  if (diff < 86400) return Math.floor(diff / 3600) + 'h';
  const now = new Date();
  const sameYear = d.getFullYear() === now.getFullYear();
  return sameYear ? `${d.getMonth() + 1}/${d.getDate()}` : `${d.getFullYear()}/${d.getMonth() + 1}/${d.getDate()}`;
}

// fmtMetric 大数缩写：1234 → 1.2k（点赞/评论/转发用）
export function fmtMetric(n) {
  const v = Number(n) || 0;
  if (v < 1000) return String(v);
  if (v < 1000000) return (v / 1000).toFixed(v < 10000 ? 1 : 0) + 'k';
  return (v / 1000000).toFixed(1) + 'M';
}

// TweetCard 单条推文卡片（X/Twitter 布局）：头像 + 昵称/@handle + 正文 + 引用/转推 + 时间/互动。
// FeedTab（原推文列表）与 PoolPicker（勾选池）共用同一份展示，避免两处布局走样。
//
// props:
//   tw            推文行（后端 TweetRow：id/user/text/edited/media/retweet_*/quoted_*/translation/created_at）
//   role          角色（取 name 作为昵称，user 为空时用 username 兜底）
//   left          左侧插槽（复选框等）
//   footer        底部右侧插槽（编辑/播报/挂摘等操作按钮）
//   editing       是否处于编辑态 + editText/onEditText/onSaveEdit/onCancelEdit/onResetEdit
//   dense         true 时更紧凑（池区列表里用）
export default function TweetCard({
  tw, role, left = null, footer = null, dense = false,
  editing = false, editText = '', onEditText, onSaveEdit, onCancelEdit, onResetEdit,
}) {
  const name = tw.user || role?.username || '';
  const time = tw.created_at ? fmtTweetTimeAbs(tw.created_at) : '';
  const timeTip = tw.created_at ? `${fmtTweetTimeAbs(tw.created_at)}（${fmtTweetTime(tw.created_at)}）` : '';
  const images = Array.isArray(tw.images) ? tw.images.filter(Boolean) : [];
  // 真实互动数据（social_read 的微博/小红书给，twitter 老数据没有 → 保持原来的 id 尾号展示）
  const metrics = [
    tw.likes ? { icon: '♥', n: tw.likes, title: t('likesCount') } : null,
    tw.comment_count ? { icon: '💬', n: tw.comment_count, title: t('commentsCount') } : null,
    tw.share_count ? { icon: '🔁', n: tw.share_count, title: t('sharesCount') } : null,
    tw.collect_count ? { icon: '⭐', n: tw.collect_count, title: t('collectCount') } : null,
  ].filter(Boolean);

  return (
    <div className="card tweet" style={{ border: '1px solid var(--line-soft)', borderRadius: 14, padding: dense ? 10 : undefined }}>
      <div style={{ display: 'flex', gap: dense ? 8 : 10, alignItems: 'flex-start' }}>
        {left}
        <div style={{
          width: dense ? 30 : 36, height: dense ? 30 : 36, borderRadius: '50%', background: 'var(--bg-soft)',
          display: 'flex', alignItems: 'center', justifyContent: 'center', flexShrink: 0, fontWeight: 600,
          fontSize: dense ? 12 : undefined,
        }}>
          {(name[0] || '?').toUpperCase()}
        </div>
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ display: 'flex', gap: 6, alignItems: 'baseline', flexWrap: 'wrap' }}>
            <b style={{ fontSize: 14 }}>{role?.name || name || '—'}</b>
            <span className="hint">@{name}</span>
            {/* 上游平台徽标（后端接口已带 platform：weibo / xiaohongshu / twitter） */}
            {tw.platform ? (
              <span className="badge" title={'platform: ' + tw.platform} style={{ fontSize: 11 }}>
                {locPlatformLabel(tw.platform)}
              </span>
            ) : null}
          </div>

          {editing ? (
            <div style={{ marginTop: 4 }}>
              <textarea rows="4" style={{ width: '100%', fontSize: 13.5 }} value={editText} onChange={(e) => onEditText && onEditText(e.target.value)} />
              {tw.edited && tw.edited !== tw.text ? <p className="hint" style={{ margin: '4px 0 0' }}>📄 {t('originalText')}：{tw.text}</p> : null}
              <div style={{ display: 'flex', gap: 6, marginTop: 6 }}>
                <button className="mini" onClick={onSaveEdit}>{t('save')}</button>
                <button className="mini ghost" onClick={onCancelEdit}>{t('cancel')}</button>
                {tw.edited ? <button className="mini ghost danger" onClick={onResetEdit}>{t('editReset')}</button> : null}
              </div>
            </div>
          ) : (
            <p style={{ margin: '4px 0 0', whiteSpace: 'pre-wrap', wordBreak: 'break-word', fontSize: dense ? 13.5 : 14.5, lineHeight: 1.45 }}>
              {tw.edited ? <span className="badge ok" style={{ marginRight: 6 }} title={t('editedBadge')}>📝 {t('editedBadge')}</span> : null}
              {/* 纯媒体推文（视频/图片，正文只有 t.co 占位链接）：给徽标说明，避免看起来是空推文 */}
              {!tw.edited && !tw.text && tw.media ? (
                <span className="badge" style={{ marginRight: 6 }} title="media only">
                  {tw.media === 'photo' || tw.media === 'image' ? '🖼' : '🎬'} {tw.media}
                </span>
              ) : null}
              {tw.edited || tw.text}
            </p>
          )}

          {(tw.retweet_text || tw.quoted_text) ? (
            <p style={{ margin: '6px 0 0', padding: '6px 8px', background: 'var(--bg-soft)', borderRadius: 8, fontSize: dense ? 12.5 : 13, lineHeight: 1.4, whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>
              <span className="hint">{tw.retweet_text ? `🔁 @${tw.retweet_user}: ` : `💬 引用 @${tw.quoted_user}: `}</span>
              {tw.retweet_text || tw.quoted_text}
            </p>
          ) : null}

          {/* 配图缩略图（微博/小红书）：点击在新窗口打开帖子原链接，没有链接就打开原图 */}
          {images.length > 0 ? (
            <div style={{ display: 'flex', gap: 6, marginTop: 8, flexWrap: 'wrap' }}>
              {images.slice(0, 9).map((src, i) => (
                <a key={`${src}-${i}`} href={tw.link || src} target="_blank" rel="noreferrer noopener" title={src}>
                  <img src={src} alt="" loading="lazy"
                    style={{
                      width: dense ? 64 : 92, height: dense ? 64 : 92, objectFit: 'cover', borderRadius: 8,
                      border: '1px solid var(--line-soft)', background: 'var(--bg-soft)', display: 'block',
                    }} />
                </a>
              ))}
            </div>
          ) : null}

          {tw.translation ? (
            <p className="hint" style={{ whiteSpace: 'pre-wrap', margin: '8px 0 0', paddingLeft: 8, borderLeft: '2px solid var(--line-soft)' }}>🌐 {tw.translation}</p>
          ) : null}

          <div className="hint" style={{ marginTop: 8, fontSize: 12, display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
            {time && <span title={timeTip}>🕘 {time}</span>}
            {metrics.length > 0
              ? metrics.map((m) => <span key={m.title} title={m.title}>{m.icon} {fmtMetric(m.n)}</span>)
              : (tw.id ? <span>· ♥ {String(tw.id).slice(-4)}</span> : null)}
            <span style={{ flex: 1 }}></span>
            {footer}
          </div>
        </div>
      </div>
    </div>
  );
}
