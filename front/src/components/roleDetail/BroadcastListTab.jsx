import { t } from '../../i18n.js';
import { api } from '../../api.js';
import { useToastErr, useToastOk } from '../../toast.jsx';
import { notifyMessageQueued } from '../../messageQueue.js';
import PoolPicker from './PoolPicker.jsx';

// 左侧 BroadcastList：可添加的播报池（勾选 / 搜索挂入），与 Feed List 全量列表分开。
export default function BroadcastListTab({ role }) {
  const toastOk = useToastOk();
  const toastErr = useToastErr();
  const broadcastRandom = async () => {
    try {
      const res = await api(`/api/roles/${role.id}/broadcast`, { method: 'POST', body: '{}' });
      if (res && res.error) toastErr(res.error);
      else { toastOk(t('bqQueued').replace('%s', res.queued || 0)); notifyMessageQueued(); }
    } catch (e) { toastErr(e.message); }
  };
  return (
    <>
      <p className="hint" style={{ marginTop: 0 }}>{t('broadcastListHint')}</p>
      <div className="bar">
        <button className="mini" onClick={broadcastRandom}>{t('broadcastRandom')}</button>
      </div>
      <PoolPicker role={role} pool="random" />
    </>
  );
}
