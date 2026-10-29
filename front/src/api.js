// ---- api helpers（与原 web/app.js 保持一致；401 → /login 服务端登录页）----
function jsonHeaders(opts) {
  const h = { 'Content-Type': 'application/json' };
  if (opts && (opts.body instanceof FormData || opts.body instanceof Blob)) delete h['Content-Type'];
  return h;
}

export const api = async (path, opts = {}) => {
  const res = await fetch(path, { headers: jsonHeaders(opts), ...opts });
  if (res.status === 401) { location.href = '/login'; throw new Error('未登录'); }
  const text = await res.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch { data = undefined; }
  if (data === undefined) throw new Error(`接口返回异常 (HTTP ${res.status}，非 JSON): ` + text.slice(0, 120));
  // 错误体（{error:...}）必须抛出，调用方 catch 后 toast 提示 —— 之前这段检查在 return 之后是死代码，
  // 导致 broadcast 等接口失败时前端拿不到任何反馈（修于 broadcast 静默失败事故）。
  if (!res.ok || (data && data.error)) throw new Error((data && data.error) || res.statusText);
  return data === null ? [] : data;
};

// 同 api()，但 body.error 不视为异常（状态探测等本身携带 error 字段的接口）
export const apiRaw = async (path, opts = {}) => {
  const res = await fetch(path, { headers: jsonHeaders(opts), ...opts });
  if (res.status === 401) { location.href = '/login'; throw new Error('未登录'); }
  return res.json().catch(() => ({}));
};

// ---- helpers ----
export function relTime(ts) {
  const d = typeof ts === 'number' ? new Date(ts * 1000) : new Date(ts);
  const diff = (Date.now() - d.getTime()) / 1000;
  if (diff < 60) return Math.max(1, Math.floor(diff)) + ' 秒前';
  if (diff < 3600) return Math.floor(diff / 60) + ' 分钟前';
  if (diff < 86400) return Math.floor(diff / 3600) + ' 小时前';
  return d.toLocaleString();
}

export function humanInterval(sec) {
  if (sec % 3600 === 0 && sec >= 3600) return (sec / 3600) + ' h';
  if (sec % 60 === 0 && sec >= 60) return (sec / 60) + ' min';
  return sec + ' s';
}
