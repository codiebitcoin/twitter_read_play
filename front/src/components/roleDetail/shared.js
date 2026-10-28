import { api } from '../../api.js';
import { t } from '../../i18n.js';

// ---- 共用的角色 PUT（spread 保留非表单字段） ----
// 全量 PUT 前先拉服务端最新快照做 base：调用方手里的 role 是「打开页面那一刻」的
// 快照，直接 spread 回写会把别处（另一台设备/另一个面板）刚改过的配置静默写回旧值
// （实测：设置里切了 TTS 模式/模板，Stream 面板随后一次拉黑/暂停就把它写回去了）。
export async function putRole(role, extra) {
  const url = '/api/roles/' + encodeURIComponent(role.id);
  const cur = await api(url).catch(() => null);
  return api(url, { method: 'PUT', body: JSON.stringify({ ...(cur || role), ...extra }) });
}

// 角色平台：界面只露出 x。存量 twitter 与空值都归到 x；上游匹配仍把 x 视为 twitter。
export const KNOWN_PLATFORMS = ['twitter', 'x', 'weibo', 'xiaohongshu', 'instagram', 'truth', 'bluesky', 'bilibili', 'rss'];

// Voice / VRM 来源模式：'slot'（默认，沿用设备原槽位形象与声音）| 'custom'（角色自带）。
// 空值/未知按 slot 处理，与后端 Role.UsesCustomVoiceVrm 一致。
export const usesCustomVoiceVrm = (role) => !!role && role.voice_vrm_mode === 'custom';

// 弹幕过滤分词：逗号（中英文）/分号/空白分隔，去空（与后端 livechat.ParseWords 同规则）。
export const parseFilterWords = (s) =>
  String(s || '').split(/[，,；;\s]+/).map((w) => w.trim()).filter(Boolean);

// 弹幕平台能力：只有 B 站链路（blivedm / 内置浏览器）透传用户等级。
// 不支持的平台（Twitch/YouTube）前端就不显示「最低用户等级」过滤项，与后端 CapabilitiesFor 同规则。
export const liveHasUserLevel = (kindOrAdapter) =>
  String(kindOrAdapter || '').toLowerCase().includes('bilibili');
export const normPlatform = (p) => (!p || p === 'x' ? 'twitter' : p);
export const uiPlatform = (p) => {
  const v = String(p || '').toLowerCase();
  if (!v || v === 'twitter' || v === 'x') return 'x';
  return v;
};
export function platformLabel(p) {
  const v = (p || 'x').toLowerCase();
  if (v === 'weibo') return t('platformWeibo');
  if (v === 'xiaohongshu') return t('platformXhs');
  if (v === 'instagram') return t('platformInstagram');
  if (v === 'truth') return t('platformTruth');
  if (v === 'bluesky') return t('platformBluesky');
  if (v === 'bilibili') return t('platformBili');
  if (v === 'rss') return t('platformRss');
  if (v === 'twitter' || v === 'x') return t('platformX');
  return v;
}
export function needsSocialdataKey(platform) {
  const v = normPlatform(platform);
  return v === 'twitter';
}

// cmpIDDesc 与后端 gtID 同口径的排序（tid 是雪花 id 字符串，长的高=新）：新→旧。
// 池列表按页往回翻时，中途挂入一条旧推文要用它插到正确位置，否则列表顺序会乱。
export const cmpIDDesc = (a, b) => {
  const x = String(a.id || ''); const y = String(b.id || '');
  if (x.length !== y.length) return y.length - x.length;
  return x < y ? 1 : x > y ? -1 : 0;
};
