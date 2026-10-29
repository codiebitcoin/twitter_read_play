import { useState } from 'react';
import { t } from '../../i18n.js';
import BroadcastFeed from './FeedTab.jsx';
import RandomPanel from './RandomPanel.jsx';
import ParodyPanel from './ParodyPanel.jsx';
import WatchPanel from './WatchPanel.jsx';

// Feed 页：社媒/RSS 帖子。Stream / Book 已提升到角色详情同级，不再挂在这里。
//   feedlist  —— 全量列表（刷新/轮询/逐条播报）；勾选池在左侧 BroadcastList
//   random    —— 按规则从缓存随机播
//   parody    —— LLM 仿写
//   watch     —— 监听（拉新）总开关、间隔、Feed 关键字；与 Stream 配置互不影响
const MODES = ['feedlist', 'random', 'parody', 'watch'];
const LABEL = { feedlist: 'modeFeedList', random: 'modeRandom', parody: 'modeParody', watch: 'modeWatch' };

export default function FeedTabs({ role, onSaved, goTab }) {
  const [mode, setMode] = useState('feedlist');
  return (
    <>
      <nav className="subtabs subtabs-line" aria-label="Feed">
        {MODES.map((m) => (
          <button key={m} type="button" className={'tab mini' + (m === mode ? ' active' : '')} onClick={() => setMode(m)}>
            {t(LABEL[m])}
          </button>
        ))}
      </nav>
      {mode === 'feedlist' && <BroadcastFeed role={role} />}
      {mode === 'random' && <RandomPanel role={role} onSaved={onSaved} />}
      {mode === 'parody' && <ParodyPanel role={role} onSaved={onSaved} />}
      {mode === 'watch' && <WatchPanel role={role} onSaved={onSaved} goTab={goTab} />}
    </>
  );
}
