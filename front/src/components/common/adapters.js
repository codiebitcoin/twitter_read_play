// 后端下发的适配器/渠道 name、hint 是中文，前端按 id 本地化；未知 id 原样返回。
import { t } from '../../i18n.js';

const ADAPTER_I18N = {
  twitch_browser: ['adapterTwitchBrowser', 'hintTwitchBrowser'],
  youtube_browser: ['adapterYouTubeBrowser', 'hintYouTubeBrowser'],
  bilibili_blivedm: ['adapterBilibiliBlivedm', 'hintBilibiliBlivedm'],
};

// locAdapter 适配器 Info 的 name/hint 本地化（返回新对象）。
export function locAdapter(a) {
  if (!a) return a;
  const m = ADAPTER_I18N[a.id];
  return m ? { ...a, name: t(m[0]), hint: t(m[1]) } : a;
}

// locChannelName 渠道名本地化：内置浏览器渠道与适配器同名；blivedm 渠道用独立 key。
export function locChannelName(ch) {
  if (!ch) return '';
  if (ch.id === 'ch-live-bilibili') return t('channelBilibili');
  const m = ADAPTER_I18N[ch.adapter_id || ch.adapter?.id];
  return m ? t(m[0]) : (ch.name || ch.id);
}

// platformLabel 平台显示名（i18n key 已有，收拢各处重复的三元链）。
export function platformLabel(p) {
  const v = (p || 'x').toLowerCase();
  const key = {
    weibo: 'platformWeibo', xiaohongshu: 'platformXhs', instagram: 'platformInstagram',
    truth: 'platformTruth', bluesky: 'platformBluesky', bilibili: 'platformBili',
    rss: 'platformRss', twitter: 'platformX', x: 'platformX',
  }[v];
  return key ? t(key) : v;
}
