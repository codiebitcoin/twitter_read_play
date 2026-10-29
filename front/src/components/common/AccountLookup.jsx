import { useEffect, useRef, useState } from 'react';
import { api } from '../../api.js';
import { t } from '../../i18n.js';

// Feed 目标输入 + 静态解析 + 显式「自动识别」+「查看账号」。
// 详情识别只读取公开资料；失败时保留静态解析结果，不阻塞保存。

const SOURCE_LABEL = {
  x_browser: 'X 内置浏览器',
  weibo_browser: '微博 内置浏览器',
  xiaohongshu_browser: '小红书 内置浏览器',
  instagram_browser: 'Instagram 内置浏览器',
  truth_browser: 'Truth Social 内置浏览器',
  bluesky: 'Bluesky 公开 API',
  socialdata: 'socialdata.tools',
  socialread: 'twitter-read',
  xtoken: 'X 登录 token',
  twitter_official: 'X 官方 API',
  weibo: '微博 Cookie',
  rss: 'RSS',
  truth: 'Truth Social',
};

const num = (n) => (n === undefined || n === null || n === '' ? '' : Number(n).toLocaleString());

// 平台 → 内置浏览器 provider（与 browser.AllProviders 一致）。
const PROVIDER_FOR_PLATFORM = {
  x: 'x',
  weibo: 'weibo',
  xiaohongshu: 'xiaohongshu',
  bilibili: 'bilibili',
  youtube: 'youtube',
  twitch: 'twitch',
  instagram: 'instagram',
  truth: 'truth',
  bluesky: 'bluesky',
};

function inspectMessage(ie, platform) {
  if (!ie) return '';
  switch (ie.error_kind) {
    case 'need_login':
      return t('accountNeedLogin').replace('%s', platform || '');
    case 'inspect_unsupported':
      return t('accountStaticOnly');
    case 'rate_limited':
      return t('accountRateLimited');
    case 'timeout':
      return t('accountTimeout');
    default:
      return t('accountInspectFailed').replace('%s', ie.error || '');
  }
}

export default function AccountLookup({
  domain = 'feed',
  value = '',
  onChange,
  platform = '',
  channelId = '',
  expectedPlatform = '',
  compact = false,
  showInput = true,
  placeholder = '',
  inputId,
}) {
  const [preview, setPreview] = useState(null);
  const [result, setResult] = useState(null);
  const [busy, setBusy] = useState(false);
  const [waitingLogin, setWaitingLogin] = useState(false);
  const [err, setErr] = useState('');
  const [mode, setMode] = useState('');
  const seq = useRef(0);
  const loginPoll = useRef(null);

  useEffect(() => () => {
    if (loginPoll.current) clearInterval(loginPoll.current);
  }, []);

  // 桌面端才能开内置窗口；Web 端走新标签。
  useEffect(() => {
    api('/api/browser/session')
      .then((s) => setMode(s && s.available ? 'desktop' : 'web'))
      .catch(() => setMode('web'));
  }, []);

  // 静态解析：防抖，不发网络上游请求。
  useEffect(() => {
    const raw = (value || '').trim();
    if (!raw) {
      setPreview(null);
      setResult(null);
      return;
    }
    // 目标变了，上一次的识别结果就不再对应当前输入。
    setResult(null);
    setErr('');
    const my = ++seq.current;
    const timer = setTimeout(() => {
      api('/api/source/parse', {
        method: 'POST',
        body: JSON.stringify({ domain, raw, platform, channel_id: channelId }),
      }).then((r) => {
        if (my === seq.current) setPreview(r);
      }).catch((e) => {
        if (my === seq.current) setPreview({ error: e.message });
      });
    }, 350);
    return () => clearTimeout(timer);
  }, [value, platform, channelId, domain]);

  const inspect = async (refresh = false) => {
    const raw = (value || '').trim();
    if (!raw || busy) return;
    setBusy(true);
    setErr('');
    try {
      const r = await api('/api/source/inspect', {
        method: 'POST',
        body: JSON.stringify({
          domain, raw, platform,
          channel_id: channelId,
          expected_platform: expectedPlatform,
          refresh,
        }),
      });
      setResult(r);
    } catch (e) {
      setErr(e.message);
    } finally {
      setBusy(false);
    }
  };

  // 识别失败且原因是未登录：打开内置登录窗，轮询登录态，成功后自动重新识别。
  // 后端在登录窗关闭时也会主动 Probe 一次，所以用户关窗后这里通常 1-2 秒内就能拿到 connected。
  const openLoginAndRetry = async () => {
    const p = PROVIDER_FOR_PLATFORM[target?.platform || platform || result?.account?.platform];
    if (!p) {
      setErr(t('accountLoginUnavailable'));
      return;
    }
    if (loginPoll.current) clearInterval(loginPoll.current);
    setErr('');
    setWaitingLogin(true);
    try {
      await api(`/api/browser/session/${encodeURIComponent(p)}/open`, { method: 'POST' });
    } catch (e) {
      setErr(e.message);
      setWaitingLogin(false);
      return;
    }
    const started = Date.now();
    loginPoll.current = setInterval(async () => {
      try {
        const st = await api(`/api/browser/session/${encodeURIComponent(p)}`);
        if (st && st.connected) {
          clearInterval(loginPoll.current);
          loginPoll.current = null;
          setWaitingLogin(false);
          inspect(true);
          return;
        }
        // 关窗后后端给出明确「未登录」结论：停止等待，让用户看到结果而不是一直转。
        if (st && (st.state === 'disconnected' || st.state === 'expired')) {
          clearInterval(loginPoll.current);
          loginPoll.current = null;
          setWaitingLogin(false);
          setErr(t('accountLoginNotDetected').replace('%s', p));
          return;
        }
      } catch { /* 登录页导航中，忽略 */ }
      if (Date.now() - started > 180000) {
        clearInterval(loginPoll.current);
        loginPoll.current = null;
        setWaitingLogin(false);
      }
    }, 2000);
  };

  const openView = async () => {
    const url = result?.view_url || preview?.view_url;
    if (!url) return;
    if (mode === 'desktop') {
      try {
        const r = await api('/api/source/view', {
          method: 'POST',
          body: JSON.stringify({ domain, raw: value, platform }),
        });
        if (r && r.opened) return;
        if (r && r.error) setErr(r.error);
      } catch (e) {
        setErr(e.message);
      }
    }
    // Web 端：这里是点击事件的第一段同步代码，不会被弹窗拦截。
    window.open(url, '_blank', 'noopener,noreferrer');
  };

  const target = result?.target || preview?.target;
  const account = result?.account;
  const liveKind = account?.kind === 'live_room' || account?.kind === 'live_video';
  const viewURL = result?.view_url || preview?.view_url;
  const canonical = account?.canonical_url || viewURL;
  const inspectErr = result && result.inspect && !result.inspect.ok ? result.inspect : null;
  // 识别结果优先：preview 的 warning 往往是一长串候选拒绝原因，识别后再重复展示很吵。
  const warnings = [...new Set(result ? (result.warnings || []) : (preview?.warnings || []))];
  const resolved = result?.resolved || preview?.resolved;

  return (
    <div className={'account-lookup' + (compact ? ' compact' : '')}>
      {showInput ? (
        <div className="account-input">
          <input
            id={inputId}
            value={value}
            onChange={(e) => onChange && onChange(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                e.preventDefault();
                inspect(false);
              }
            }}
            placeholder={placeholder || t('usernamePh')}
          />
          <button className="mini" onClick={() => inspect(false)} disabled={!value.trim() || busy}>
            {busy ? t('accountInspecting') : t('accountInspect')}
          </button>
        </div>
      ) : null}

      {showInput ? <span className="hint">{t(domain === 'live' ? 'liveUrlHint' : 'feedTargetHint')}</span> : null}

      {preview?.error ? <p className="hint account-err">⚠ {preview.error}</p> : null}
      {!preview?.error && target ? (
        <p className="hint account-static">
          ✓ {t('feedTargetParsed')}: {target.platform} · {target.kind} · {target.id}
          {resolved ? ` · ${t('feedTargetResolved')}: ${resolved.channel_id}` : ` · ${t('feedTargetNoChannel')}`}
        </p>
      ) : null}
      {!account && !inspectErr && viewURL ? (
        <div className="account-actions">
          <button className="mini ghost" onClick={openView}>
            {mode === 'desktop' ? t('accountViewBuiltin') : t('accountViewNewTab')}
          </button>
        </div>
      ) : null}

      {account ? (
        <div className="account-card">
          <div className="account-head">
            {account.avatar_url
              ? <img className="account-avatar" src={account.avatar_url} alt="" referrerPolicy="no-referrer" />
              : <div className="account-avatar placeholder">@</div>}
            <div className="account-meta">
              <div className="account-name">
                {account.display_name || account.handle || account.id}
                {account.verified ? <span className="badge ok">✓</span> : null}
              </div>
              <div className="hint">
                {account.handle || ''}
                {account.platform ? ` · ${account.platform}` : ''}
                {account.source ? ` · ${t('accountSource').replace('%s', SOURCE_LABEL[account.source] || account.source)}` : ''}
                {result?.inspect?.cached ? ` · ${t('accountCached')}` : ''}
              </div>
              {(account.followers || account.posts) ? (
                <div className="hint">
                  {account.followers ? `${t(liveKind ? 'accountOnline' : 'accountFollowers')} ${num(account.followers)}` : ''}
                  {account.followers && account.posts ? ' · ' : ''}
                  {account.posts ? `${t('accountPosts')} ${num(account.posts)}` : ''}
                </div>
              ) : null}
            </div>
          </div>
          {account.bio ? <p className="hint account-bio">{account.bio}</p> : null}
          <div className="account-actions">
            <button className="mini" onClick={openView} disabled={!viewURL}>
              {mode === 'desktop' ? t('accountViewBuiltin') : t('accountViewNewTab')}
            </button>
            <button className="mini ghost" onClick={() => inspect(true)} disabled={busy}>
              {busy ? t('accountInspecting') : t('accountRefresh')}
            </button>
            {canonical && canonical !== value ? (
              <button className="mini ghost" onClick={() => onChange && onChange(canonical)}>
                {t('accountUseCanonical')}
              </button>
            ) : null}
          </div>
        </div>
      ) : null}

      {!account && inspectErr ? (
        <div className="account-note">
          <p className="hint">
            {inspectErr.error_kind === 'inspect_unsupported' ? 'ℹ️ ' : '⚠ '}
            {inspectMessage(inspectErr, target?.platform || platform)}
          </p>
          <div className="account-actions">
            {viewURL ? (
              <button className="mini ghost" onClick={openView}>
                {mode === 'desktop' ? t('accountViewBuiltin') : t('accountViewNewTab')}
              </button>
            ) : null}
            {inspectErr.error_kind === 'need_login' && mode === 'desktop' ? (
              <button className="mini" onClick={openLoginAndRetry} disabled={waitingLogin}>
                {waitingLogin
                  ? t('accountWaitingLogin')
                  : t('accountLoginAndRetry').replace('%s', target?.platform || platform || '')}
              </button>
            ) : (
              <button className="mini ghost" onClick={() => inspect(true)} disabled={busy}>
                {busy ? t('accountInspecting') : t('accountRetryInspect')}
              </button>
            )}
          </div>
        </div>
      ) : null}

      {err ? <p className="hint account-err">⚠ {err}</p> : null}
      {warnings.length ? <p className="hint account-warn">⚠ {warnings.join(' · ')}</p> : null}
    </div>
  );
}
