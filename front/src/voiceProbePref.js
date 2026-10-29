// ---- 「试听验证音色」偏好（sync / check 共用，localStorage 持久化，默认开启）----
// 开启时：同步/校验会先让设备试听已记录的克隆（外放一小段），确认真的能播才沿用，
// 播不出来才重克隆——避免为可用的克隆白克隆一次（分钟级且会占用克隆槽）。
// 关闭时：静默校验，不试听；设备未登记克隆槽时会直接走重克隆。
const KEY = 'probeVoice';

export function getProbeVoice() {
  const v = localStorage.getItem(KEY);
  return v === null ? true : v === '1';
}

export function setProbeVoice(on) {
  localStorage.setItem(KEY, on ? '1' : '0');
}
