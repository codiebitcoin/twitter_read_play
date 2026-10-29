// macOS「本地网络」授权提示 + 一键打开系统设置。
//
// macOS 15 起，应用首次访问局域网（设备扫描 / 配对）会弹「本地网络」授权弹窗；
// 用户点「拒绝」后扫描与配对会静默失败，只能去系统设置手动打开授权。
// 配对入口（向导 guide / 机器详情配对弹窗）只在真正连不上对端时显示这段提示与按钮，
// 按钮调用 POST /api/system/open/local-network 直接打开授权配置页。

import { useEffect, useState } from 'react';
import { api } from '../api.js';
import { t } from '../i18n.js';
import { useToastOk, useToastErr } from '../toast.jsx';

// 连不上对端的网络层错误。只有这类失败才可能是「本地网络」授权被拒：
// 配对码错误、鉴权失败、设备端 500 等与权限无关，提示用户去改系统设置只会误导。
const LAN_FAILURE_PATTERNS = [
  'connection refused',
  'connection timed out',
  'i/o timeout',
  'no route to host',
  'network is unreachable',
  'host is down',
  'connection reset by peer',
];

export function isLanFailure(err) {
  if (!err) return false;
  const msg = String((err && err.message) || err).toLowerCase();
  return LAN_FAILURE_PATTERNS.some((p) => msg.includes(p));
}

export default function MacLanTip({ show = false, style }) {
  const [isMac, setIsMac] = useState(false);
  const [opening, setOpening] = useState(false);
  const toastOk = useToastOk();
  const toastErr = useToastErr();

  useEffect(() => {
    if (!show) { setIsMac(false); return undefined; }
    let dead = false;
    api('/api/setup/status')
      .then((st) => { if (!dead) setIsMac(st && st.host_os === 'darwin'); })
      .catch(() => { /* 拿不到就当非 Mac，不阻塞配对流程 */ });
    return () => { dead = true; };
  }, [show]);

  if (!show || !isMac) return null;

  const open = async () => {
    setOpening(true);
    try {
      await api('/api/system/open/local-network', { method: 'POST' });
      toastOk(t('macLanOpened'));
    } catch (e) { toastErr(e.message); } finally { setOpening(false); }
  };

  return (
    <p className="hint" style={{ margin: '0 0 8px', ...(style || {}) }}>
      ℹ️ {t('macLanTip')}{' '}
      <button type="button" className="mini ghost" disabled={opening} onClick={open}>
        {t('macLanOpenBtn')}
      </button>
    </p>
  );
}
