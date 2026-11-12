// 设置页「日志」模块：Debug 日志开关 + 日志 zip 一键下载。
// 排障流程：开 Debug → 复现问题（比如登录一次 X）→ 下载 zip 发给开发者。
// zip 里是桌面模式的 desktop.log（含轮转的 .1），敏感参数（desktop-start 凭据、
// OAuth 回调码）后端下载时已统一打码。
import { useEffect, useState } from 'react';
import { api } from '../../api.js';
import { t } from '../../i18n.js';
import { useToastErr, useToastOk } from '../../toast.jsx';

export function LogsCard({ st, onSaved }) {
  const toastOk = useToastOk();
  const toastErr = useToastErr();
  const [on, setOn] = useState(!!st.debug);
  const [busy, setBusy] = useState(false);

  useEffect(() => { setOn(!!st.debug); }, [st.debug]);

  const toggle = async () => {
    const next = !on;
    setOn(next); // 乐观更新，失败回滚
    try {
      await api('/api/settings', { method: 'PUT', body: JSON.stringify({ debug: next ? '1' : '' }) });
      toastOk(t('saved'));
      onSaved && onSaved();
    } catch (e) {
      setOn(!next);
      toastErr(e.message);
    }
  };

  // 下载走裸 fetch（api() 只吃 JSON），拿 blob 后用 <a download> 触发保存。
  const download = async () => {
    setBusy(true);
    try {
      const res = await fetch('/api/logs/download');
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const blob = await res.blob();
      const cd = res.headers.get('Content-Disposition') || '';
      const a = document.createElement('a');
      a.href = URL.createObjectURL(blob);
      a.download = (cd.match(/filename="([^"]+)"/) || [])[1] || 'trp-logs.zip';
      a.click();
      URL.revokeObjectURL(a.href);
    } catch (e) { toastErr(e.message); }
    finally { setBusy(false); }
  };

  return (
    <div className="card" style={{ maxWidth: 520, marginTop: 16 }} id="logs">
      <b>{t('logsTitle')}</b>
      <p className="hint" style={{ marginTop: 8 }}>{t('logsHint')}</p>
      <div style={{ display: 'flex', gap: 12, alignItems: 'center', flexWrap: 'wrap', marginTop: 4 }}>
        <label style={{ display: 'flex', gap: 6, alignItems: 'center', cursor: 'pointer' }}>
          <input type="checkbox" checked={on} onChange={toggle} />
          {t('logsDebug')}
        </label>
        <button className="mini" disabled={busy} onClick={download}>{busy ? '…' : t('logsDownload')}</button>
      </div>
      <p className="hint">{on ? t('logsDebugOn') : t('logsDebugOff')}</p>
    </div>
  );
}
