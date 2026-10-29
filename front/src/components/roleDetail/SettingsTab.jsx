import { useEffect, useState } from 'react';
import { api } from '../../api.js';
import { t } from '../../i18n.js';
import { useToastErr, useToastOk } from '../../toast.jsx';
import { useNav } from '../../App.jsx';
import { putRole, KNOWN_PLATFORMS, normPlatform, uiPlatform, liveHasUserLevel } from './shared.js';
import AccountLookup from '../common/AccountLookup.jsx';
import { locAdapter, locChannelName } from '../common/adapters.js';

// 角色设置分三段：
//   1. Feed 上游配置（目标 + 渠道 + 监听）
//   2. Live 上游配置（目标 + 渠道 + 弹幕）
//   3. 通用播报设置（TTS/Chat、语言、重复、过滤）
// 凭据/渠道本身统一在系统设置 → Feed / Live 管理；这里只选「用哪个渠道」。
export default function SettingsTab({ role, machines, onSaved, llmReady: llmReadyProp }) {
  const toastOk = useToastOk();
  const toastErr = useToastErr();
  const nav = useNav();
  const [f, setF] = useState({
    platform: uiPlatform(role.platform),
    username: role.username || '',
    feed_target: role.feed_target || role.username || '',
    feed_channel_id: role.feed_channel_id || '',
    monitor_way: role.monitor_way || 'poll',
    interval: role.interval || 600,
    enabled: role.enabled ? '1' : '0',
    upstream_id: role.upstream_id || '',
    vol: role.tts_volume || 0,
    language: role.language || 'en',
    speak_prompt: role.speak_prompt || '',
    speak_rewrite: role.speak_rewrite ? '1' : '0',
    speak_mode: role.speak_mode || 'chat',
    // 播报模板按模式拆两个字段（后端 speak_text_template_chat / speak_text_template_tts）：
    // 切换 Playback mode 时各展示、各编辑、各保存，互不覆盖。旧版单一 speak_template
    // 已由后端启动时迁入 chat 字段，这里只读新字段。
    speak_template_chat: role.speak_text_template_chat || '',
    speak_template_tts: role.speak_text_template_tts || '',
    speak_regex: role.speak_regex || '',
    model_profile: role.model_profile || '',
    repeat_count: role.repeat_count || 1,
    repeat_gap: role.repeat_gap || 30,
    filter_words: role.filter_words || '',
    require_words: role.require_words || '',
    fetch_count: role.fetch_count || 20,
    auto_random_interval: role.auto_random_interval || 0,
    allow_empty: role.allow_empty ? '1' : '0',
    live_url: role.live_url || '',
    live_channel_id: role.live_channel_id || '',
    live_enabled: role.live_enabled ? '1' : '0',
    live_interval: role.live_interval > 0 ? role.live_interval : 1,
    live_filter_words: role.live_filter_words || '',
    live_require_words: role.live_require_words || '',
    live_only_sc: !!role.live_only_sc,
    live_no_sc: !!role.live_no_sc,
    live_block_users: role.live_block_users || '',
    live_min_level: role.live_min_level || 0,
    live_repeat_count: role.live_repeat_count > 0 ? role.live_repeat_count : 1,
    live_repeat_gap: role.live_repeat_gap || 0,
  });
  const [channels, setChannels] = useState({ feed: [], live: [] });
  const [setup, setSetup] = useState(null);
  const [section, setSection] = useState('feed');

  // LLM 是否配置齐全：决定了播报口语化开关显不显示（父组件没传时自己拉）
  const [llmReadyLocal, setLlmReadyLocal] = useState(false);
  const llmReady = llmReadyProp !== undefined ? llmReadyProp : llmReadyLocal;
  // PC 模式（打包期注入）：无公网回调地址，callback 监控收不到推送，下拉里不出现
  const [pcMode, setPcMode] = useState(false);

  useEffect(() => {
    if (llmReadyProp !== undefined) return;
    api('/api/settings')
      .then((s) => setLlmReadyLocal(!!(s && s.llm_base_url && s.llm_api_key && s.llm_model)))
      .catch(() => {});
  }, [llmReadyProp]);

  useEffect(() => {
    api('/api/setup/status').then((st) => {
      setPcMode(!!st.pc_mode);
      setSetup(st);
    }).catch(() => {});
  }, []);

  // 真实渠道列表：Feed / Live 各自一个域；不再使用旧的 /api/upstreams（新模型下为空）。
  useEffect(() => {
    Promise.all([
      api('/api/channels?domain=feed').catch(() => ({ channels: [] })),
      api('/api/channels?domain=live').catch(() => ({ channels: [] })),
    ]).then(([feed, live]) => setChannels({
      feed: feed?.channels || [],
      live: live?.channels || [],
    }));
  }, []);

  const set = (k) => (e) => setF((cur) => ({ ...cur, [k]: e.target.value }));

  const platformMatches = (ch, p) => {
    const ps = ch?.adapter?.platforms || [];
    if (!p || !ps.length) return true;
    const want = normPlatform(p);
    return ps.some((x) => normPlatform(x) === want);
  };
  const channelOptions = (list, platform, current) => {
    const matching = (list || []).filter((ch) => platformMatches(ch, platform));
    const cur = (list || []).find((ch) => ch.id === current);
    return cur && !matching.some((ch) => ch.id === cur.id) ? [cur, ...matching] : matching;
  };

  const onPlatformChange = (e) => {
    const platform = e.target.value;
    setF((cur) => {
      const keep = (channels.feed || []).some((ch) => ch.id === cur.feed_channel_id && platformMatches(ch, platform));
      return { ...cur, platform, feed_channel_id: keep ? cur.feed_channel_id : '' };
    });
  };

  // Chat 通道需要固件 ≥1.5.3：绑定的机器有任何一台 ≥1.5.3 即可选 Chat
  const verOK = (v) => {
    if (!v) return true;
    const m = String(v).match(/(\d+)\.(\d+)(\.(\d+))?/);
    if (!m) return true;
    return (+m[1]) * 10000 + (+m[2]) * 100 + (+m[4] || 0) >= 10503;
  };
  const boundMachines = (machines || []).filter((mm) => (role.bindings || []).some((b) => b.machine_id === mm.id));
  const chatAvailable = boundMachines.length === 0 || boundMachines.some((mm) => verOK(mm.firmware));

  const save = async () => {
    try {
      await putRole(role, {
        platform: uiPlatform(f.platform), username: f.username.trim().replace(/^@/, ''),
        feed_target: f.feed_target.trim(),
        feed_channel_id: f.feed_channel_id,
        live_url: f.live_url.trim(),
        live_channel_id: f.live_channel_id,
        live_enabled: f.live_enabled === '1',
        live_interval: +f.live_interval || 1,
        live_filter_words: f.live_filter_words,
        live_require_words: f.live_require_words,
        live_only_sc: !!f.live_only_sc,
        live_no_sc: !!f.live_no_sc,
        live_block_users: f.live_block_users,
        live_min_level: +f.live_min_level || 0,
        live_repeat_count: +f.live_repeat_count || 1,
        live_repeat_gap: +f.live_repeat_gap || 0,
        // PC 模式下拉里没有 callback 选项；历史角色若存的是 callback，保存时兜底回 poll
        monitor_way: pcMode ? 'poll' : f.monitor_way, interval: +f.interval || 600, enabled: f.enabled === '1',
        upstream_id: f.upstream_id, tts_volume: +f.vol || 0, language: f.language,
        speak_prompt: f.speak_prompt,
        speak_rewrite: f.speak_rewrite === '1',
        model_profile: f.model_profile, model_profiles: role.model_profiles || [],
        repeat_count: +f.repeat_count || 1, repeat_gap: +f.repeat_gap || 0,
        filter_words: f.filter_words,
        require_words: f.require_words,
        fetch_count: +f.fetch_count || 20,
        auto_random_interval: +f.auto_random_interval || 0,
        allow_empty: f.allow_empty === '1',
        speak_mode: f.speak_mode === 'tts' ? 'tts' : 'chat',
        speak_text_template_chat: f.speak_template_chat,
        speak_text_template_tts: f.speak_template_tts,
        speak_regex: f.speak_regex,
      });
      toastOk(t('saved'));
      onSaved && onSaved();
    } catch (e) { toastErr(e.message); }
  };

  const feedChannels = channelOptions(channels.feed, f.platform, f.feed_channel_id);
  const liveChannels = channelOptions(channels.live, '', f.live_channel_id);
  const sharedReady = !!setup?.data_source_ready;
  // 用户等级过滤只在带等级的直播源（B 站链路）显示；选「自动」时任一渠道是 B 站就显示
  const levelSupported = (() => {
    if (f.live_channel_id) {
      const ch = liveChannels.find((c) => c.id === f.live_channel_id);
      return liveHasUserLevel(ch && (ch.adapter_id || ch.adapter?.id));
    }
    return liveChannels.some((c) => liveHasUserLevel(c.adapter_id || c.adapter?.id));
  })();

  return (
    <>
      {/* 通用上游：凭据/渠道在系统设置里统一管理，Feed/Live 共用 */}
      <div className="card" style={{ maxWidth: 860 }}>
        <div className="bound-source">
          <b>{t('sharedUpstreamTitle')}</b>
          <span className={'badge ' + (sharedReady ? 'ok' : 'warn')}>
            {sharedReady ? t('channelReady') : t('upstreamNotConfigured')}
          </span>
        </div>
        <p className="hint" style={{ margin: '4px 0 8px' }}>{t('sharedUpstreamHint')}</p>
        <div className="hint" style={{ display: 'flex', gap: 12, flexWrap: 'wrap' }}>
          <span>{t('feed')}: {channels.feed.length}</span>
          <span>{t('navLive')}: {channels.live.length}</span>
          {setup ? <span>{t('upstreamBrowserLoggedIn')}: {setup.browser_logged_in || 0}</span> : null}
          {setup?.socialdata_ready ? <span className="badge ok">socialdata ✓</span> : null}
        </div>
        <div className="account-actions">
          <button className="mini ghost" type="button" onClick={() => nav('/settings#feed')}>{t('goFeedSettings')}</button>
          <button className="mini ghost" type="button" onClick={() => nav('/settings#live')}>{t('goLiveSettings')}</button>
        </div>
      </div>

      <nav className="subtabs subtabs-line" aria-label={t('tabSettings')} style={{ marginTop: 12, maxWidth: 860 }}>
        {[
          ['feed', 'feedUpstreamConfig'],
          ['live', 'liveUpstreamConfig'],
          ['broadcast', 'broadcastConfig'],
        ].map(([key, label]) => (
          <button key={key} type="button" className={'tab mini' + (section === key ? ' active' : '')}
            onClick={() => setSection(key)}>
            {t(label)}
          </button>
        ))}
      </nav>

      {/* 1. Feed 上游配置 */}
      {section === 'feed' && <div className="card" style={{ maxWidth: 860, marginTop: 12 }}>
        <div className="bound-source">
          <b>{t('feedUpstreamConfig')}</b>
          <span className="badge mut">{t('feedSectionHint')}</span>
        </div>
        <div className="form-grid" style={{ marginTop: 10 }}>
          <label className="field">{t('platform')}
            <select value={f.platform} onChange={onPlatformChange}>
              {!KNOWN_PLATFORMS.includes(f.platform) ? <option value={f.platform}>{f.platform}</option> : null}
              <option value="x">{t('platformX')}</option>
              <option value="weibo">{t('platformWeibo')}</option>
              <option value="xiaohongshu">{t('platformXhs')}</option>
              <option value="instagram">{t('platformInstagram')}</option>
              <option value="truth">{t('platformTruth')}</option>
              <option value="bluesky">{t('platformBluesky')}</option>
              <option value="bilibili">{t('platformBili')}</option>
              <option value="rss">{t('platformRss')}</option>
            </select>
          </label>
          <label className="field">{t('feedChannel')}
            <select value={f.feed_channel_id} onChange={set('feed_channel_id')}>
              <option value="">{t('channelAuto')}</option>
              {feedChannels.map((ch) => (
                <option key={ch.id} value={ch.id}>
                  {locChannelName(ch)} · {locAdapter(ch.adapter)?.name || ch.adapter_id}
                  {ch.enabled === false ? ` · ${t('disabled')}` : ''}
                  {ch.ready ? '' : ` ⚠ ${t('channelNotReady')}`}
                </option>
              ))}
            </select>
            <span className="hint">
              {feedChannels.length === 0 ? t('channelNoneForPlatform') : t('channelSelectHint')}
            </span>
          </label>
          <div className="field span-2">
            <label htmlFor="role-feed-target">{t('feedTarget')}</label>
            <AccountLookup
              inputId="role-feed-target"
              domain="feed"
              value={f.feed_target}
              onChange={(v) => setF((cur) => ({ ...cur, feed_target: v }))}
              platform={f.platform}
              channelId={f.feed_channel_id}
              expectedPlatform={f.platform}
              placeholder={normPlatform(f.platform) === 'rss' ? t('usernamePhRss') : t('usernamePh')}
            />
          </div>
          <label className="field">{t('monitorWay')}
            <select value={f.monitor_way} onChange={set('monitor_way')}>
              <option value="poll">{t('monitorWayPoll')}</option>
              {!pcMode && normPlatform(f.platform) !== 'rss' && <option value="callback">{t('monitorWayCallback')}</option>}
            </select>
          </label>
          <label className="field">{t('pollInterval')}<input type="number" value={f.interval} onChange={set('interval')} /></label>
          <label className="field">{t('monitor')}
            <select value={f.enabled} onChange={set('enabled')}>
              <option value="1">{t('enabled')}</option>
              <option value="0">{t('disabled')}</option>
            </select>
            <span className="hint">{t('monitorEnableHint')}</span>
          </label>
          <label className="field">{t('fetchCount')}<input type="number" min="1" max="100" value={f.fetch_count} onChange={set('fetch_count')} />
            <span className="hint">{t('fetchCountHint')}</span>
          </label>
        </div>
        {f.monitor_way === 'callback' && <p className="hint">ℹ️ {t('monitorWayCallbackHint')}</p>}
      </div>}

      {/* 2. Live 上游配置 */}
      {section === 'live' && <div className="card" style={{ maxWidth: 860, marginTop: 12 }}>
        <div className="bound-source">
          <b>{t('liveUpstreamConfig')}</b>
          <span className="badge mut">{t('liveSectionHint')}</span>
        </div>
        <div className="form-grid" style={{ marginTop: 10 }}>
          <div className="field span-2">
            <label htmlFor="role-live-url">{t('liveUrl')}</label>
            <AccountLookup
              inputId="role-live-url"
              domain="live"
              value={f.live_url}
              onChange={(v) => setF((cur) => ({ ...cur, live_url: v }))}
              channelId={f.live_channel_id}
              placeholder="https://www.youtube.com/live/… / https://live.bilibili.com/… / https://www.twitch.tv/…"
            />
          </div>
          <label className="field">{t('liveChannel')}
            <select value={f.live_channel_id} onChange={set('live_channel_id')}>
              <option value="">{t('channelAuto')}</option>
              {liveChannels.map((ch) => (
                <option key={ch.id} value={ch.id}>
                  {locChannelName(ch)} · {locAdapter(ch.adapter)?.name || ch.adapter_id}
                  {ch.enabled === false ? ` · ${t('disabled')}` : ''}
                  {ch.ready ? '' : ` ⚠ ${t('channelNotReady')}`}
                </option>
              ))}
            </select>
            <span className="hint">
              {liveChannels.length === 0 ? t('channelNoneForPlatform') : t('channelSelectHint')}
            </span>
          </label>
          <label className="field">{t('modeEnabled')}
            <select value={f.live_enabled} onChange={set('live_enabled')}>
              <option value="1">{t('enabled')}</option>
              <option value="0">{t('disabled')}</option>
            </select>
            <span className="hint">{t('streamEnableHint')}</span>
          </label>
          <label className="field">{t('pollInterval')}
            <input type="number" min="1" value={f.live_interval} onChange={set('live_interval')} />
            <span className="hint">{t('streamIntervalHint')}</span>
          </label>
          <label className="field" style={{ flexDirection: 'row', alignItems: 'center', gap: 8 }}>
            <input type="checkbox" style={{ width: 'auto' }}
              checked={!!f.live_only_sc}
              onChange={(e) => setF((cur) => ({ ...cur, live_only_sc: e.target.checked, ...(e.target.checked ? { live_no_sc: false } : {}) }))} />
            {t('streamOnlySC')}
          </label>
          <label className="field" style={{ flexDirection: 'row', alignItems: 'center', gap: 8 }}>
            <input type="checkbox" style={{ width: 'auto' }}
              checked={!!f.live_no_sc}
              onChange={(e) => setF((cur) => ({ ...cur, live_no_sc: e.target.checked, ...(e.target.checked ? { live_only_sc: false } : {}) }))} />
            {t('liveNoSC')}
          </label>
          {levelSupported && (
            <label className="field">{t('liveMinLevel')}
              <input type="number" min="0" max="200" value={f.live_min_level} onChange={set('live_min_level')} />
              <span className="hint">{t('liveMinLevelTip')}</span>
            </label>
          )}
          <label className="field span-2">{t('liveBlockUsers')}
            <textarea rows="2" placeholder={t('liveBlockUsersPh')} value={f.live_block_users} onChange={set('live_block_users')} />
            <span className="hint">{t('liveBlockUsersHint')}</span>
          </label>
          <label className="field">{t('repeatCount')}
            <input type="number" min="1" max="10" value={f.live_repeat_count} onChange={set('live_repeat_count')} />
          </label>
          <label className="field">{t('repeatGap')}
            <input type="number" min="0" value={f.live_repeat_gap} onChange={set('live_repeat_gap')} />
          </label>
          <label className="field span-2">{t('filterWords')}
            <textarea rows="2" placeholder={t('filterWordsPh')} value={f.live_filter_words} onChange={set('live_filter_words')} />
          </label>
          <label className="field span-2">{t('requireWords')}
            <textarea rows="2" placeholder={t('requireWordsPh')} value={f.live_require_words} onChange={set('live_require_words')} />
          </label>
        </div>
      </div>}

      {/* 3. 通用播报设置 */}
      {section === 'broadcast' && <div className="card" style={{ maxWidth: 860, marginTop: 12 }}>
        <div className="bound-source">
          <b>{t('broadcastConfig')}</b>
          <span className="badge mut">{t('broadcastSectionHint')}</span>
        </div>
        <div className="form-grid" style={{ marginTop: 10 }}>
          <label className="field">{t('ttsVolume')} <span className="hint">{f.vol}</span>
            <input type="range" min="0" max="100" value={f.vol} onChange={set('vol')} style={{ width: '100%', marginTop: 8, accentColor: 'var(--acc)' }} />
          </label>
          <label className="field">{t('language')}<input value={f.language} onChange={set('language')} /></label>
          <label className="field">{t('repeatCount')}<input type="number" min="1" max="10" value={f.repeat_count} onChange={set('repeat_count')} /></label>
          <label className="field">{t('repeatGap')}<input type="number" min="0" value={f.repeat_gap} onChange={set('repeat_gap')} /></label>
          <label className="field">{t('allowEmpty')}
            <select value={f.allow_empty} onChange={set('allow_empty')}>
              <option value="0">{t('allowEmptyNo')}</option>
              <option value="1">{t('allowEmptyYes')}</option>
            </select>
            <span className="hint">{t('allowEmptyHint')}</span>
          </label>
          <label className="field">{t('speakMode')}
            <select value={f.speak_mode} onChange={set('speak_mode')}>
              <option value="chat" disabled={!chatAvailable}>{chatAvailable ? t('speakModeChat') : t('speakModeChatDis')}</option>
              <option value="tts">{t('speakModeTTS')}</option>
            </select>
            <span className="hint">{t('speakModeHint')}</span>
          </label>
          {llmReady && f.speak_mode === 'tts' ? (
            <>
              <label className="field">{t('speakRewrite')}
                <select value={f.speak_rewrite === '1' ? 'llm' : (f.speak_template_tts ? 'template' : 'none')}
                  onChange={(e) => {
                    const v = e.target.value;
                    if (v === 'llm') setF({ ...f, speak_rewrite: '1' });
                    // tts 模板的默认包装就是 {{TWEET}}（原文直读），与 chat 的长指令模板不同
                    else if (v === 'template') setF({ ...f, speak_rewrite: '0', speak_template_tts: f.speak_template_tts || '{{TWEET}}' });
                    else setF({ ...f, speak_rewrite: '0', speak_template_tts: '' });
                  }}>
                  <option value="none">{t('speakRewriteNone')}</option>
                  <option value="template">{t('speakRewriteTemplate')}</option>
                  <option value="llm" disabled={!llmReady}>{t('speakRewriteLLM')}</option>
                </select>
                <span className="hint">{t('speakRewriteHint')}</span>
              </label>
              {f.speak_rewrite !== '1' && f.speak_template_tts && (<>
                <label className="field span-2">{t('speakTemplate')}
                  <textarea rows="3" placeholder={t('speakTemplatePh')} value={f.speak_template_tts} onChange={set('speak_template_tts')} />
                  <span className="hint">{t('speakTemplateHint')}</span>
                </label>
                <label className="field span-2">{t('speakRegex')}
                  <textarea rows="3" placeholder={t('speakRegexPh')} value={f.speak_regex} onChange={set('speak_regex')} />
                  <span className="hint">{t('speakRegexHint')}</span>
                </label>
              </>)}
              {f.speak_rewrite === '1' && (
                <label className="field span-2">{t('speakPrompt')}
                  <textarea rows="4" placeholder={t('speakPromptPh')} value={f.speak_prompt} onChange={set('speak_prompt')} />
                  <span className="hint">{t('speakPromptHint')}</span>
                </label>
              )}
            </>
          ) : f.speak_mode === 'tts' ? (
            <p className="hint span-2" style={{ margin: 0 }}>ℹ️ {t('speakRewriteNoLLM')}</p>
          ) : null}
          {f.speak_mode === 'chat' && (<>
            <label className="field span-2">{t('speakTemplate')}
              <textarea rows="3" placeholder={t('speakTemplatePh')} value={f.speak_template_chat} onChange={set('speak_template_chat')} />
              <span className="hint">{t('speakTemplateHint')}</span>
            </label>
            <label className="field span-2">{t('speakRegex')}
              <textarea rows="3" placeholder={t('speakRegexPh')} value={f.speak_regex} onChange={set('speak_regex')} />
              <span className="hint">{t('speakRegexHint')}</span>
            </label>
          </>)}
          <label className="field span-2">{t('filterWords')}
            <textarea rows="2" placeholder={t('filterWordsPh')} value={f.filter_words} onChange={set('filter_words')} />
          </label>
          <label className="field span-2">{t('requireWords')}
            <textarea rows="2" placeholder={t('requireWordsPh')} value={f.require_words} onChange={set('require_words')} />
          </label>
        </div>
        <p className="hint">{t('roleEnableHint')}</p>
      </div>}

      <div className="card-actions" style={{ maxWidth: 860 }}>
        <button type="button" onClick={save}>{t('save')}</button>
      </div>
    </>
  );
}
