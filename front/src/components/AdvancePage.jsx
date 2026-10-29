import { useState } from 'react';
import { t } from '../i18n.js';
import ChatRoomTab from './advance/ChatRoomTab.jsx';

// Lab 页：实验性/进阶功能的入口页。
// 左侧分组导航沿用角色详情页的 role-rail 交互；新增实验功能时往 TAB_GROUPS 里加一项即可。
const TAB_GROUPS = [
  { sec: 'railWorkspace', tabs: ['chatroom'] },
];
const TAB_LABEL = { chatroom: 'tabChatRoom' };

export default function AdvancePage() {
  const [tab, setTab] = useState('chatroom');
  return (
    <div className="role-shell">
      <div className="actionbar">
        <h2>{t('advance')}</h2>
      </div>
      <div className="role-layout">
        <nav className="role-rail" aria-label="Lab">
          {TAB_GROUPS.map((group) => (
            <div key={group.sec} className="rail-group">
              <p className="role-rail-sec">{t(group.sec)}</p>
              {group.tabs.map((tb) => (
                <button
                  key={tb}
                  type="button"
                  className={'rail-item' + (tb === tab ? ' active' : '')}
                  aria-current={tb === tab ? 'page' : undefined}
                  onClick={() => setTab(tb)}
                >{t(TAB_LABEL[tb])}</button>
              ))}
            </div>
          ))}
        </nav>
        <div className="role-main">
          {tab === 'chatroom' && <ChatRoomTab />}
        </div>
      </div>
    </div>
  );
}
