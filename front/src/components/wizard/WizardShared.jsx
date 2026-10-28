// SetupWizard 各步骤共用的小工具：常量、纯函数与 SummaryRow。
// 从原先的单文件 SetupWizard.jsx 拆出，代码保持原样。
import { uiPlatform } from '../roleDetail/shared.js';

export const BASE_STEPS = ['wizStepMachine', 'wizStepRole', 'wizStepBind', 'wizStepSync', 'wizStepDone'];
export const SLOTS = [1, 2, 3, 4, 5];

export const normUsername = (v) => String(v || '').trim().replace(/^@/, '');
export const editFromRole = (r) => ({
  name: r.name || '',
  description: r.description || '',
  prompt: r.prompt || '',
  platform: uiPlatform(r.platform),
  username: r.username || '',
  feed_target: r.feed_target || '',
  live_url: r.live_url || '',
});
export const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

export function hasVoice(role) {
  return !!(role && (role.local_voice || (role.bindings || []).some((b) => b.voice_id)));
}

// 原 slot 模式（默认）沿用设备已有的形象与声音，向导不把它算作「缺资源」。
export function slotVoiceVrm(role) {
  return !!role && role.voice_vrm_mode !== 'custom';
}

export function SummaryRow({ label, value, extra }) {
  return (
    <div className="wiz-summary-row">
      <span className="hint">{label}</span>
      <b>{value}</b>
      {extra ? <span className="badge mut">{extra}</span> : null}
    </div>
  );
}
