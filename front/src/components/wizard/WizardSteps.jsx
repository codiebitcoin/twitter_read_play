// SetupWizard 的四个步骤组件（KeyStep / MachineStep / RoleStep / TweetPickStep）
// 与两个步骤内嵌组件（InlinePair / PageImport）。
// 从原先的单文件 SetupWizard.jsx 拆出，代码保持原样。
import { useEffect, useState } from 'react';
import { api } from '../../api.js';
import { t } from '../../i18n.js';
import { useToastErr, useToastOk } from '../../toast.jsx';
import { notifyMessageQueued } from '../../messageQueue.js';
import MacLanTip, { isLanFailure } from '../MacLanTip.jsx';
import { SHOW_LIVE } from '../../features.js';
import { platformLabel, uiPlatform } from '../roleDetail/shared.js';
import { hasVoice, normUsername, slotVoiceVrm, SummaryRow } from './WizardShared.jsx';

// ---- 数据源步骤：socialdata key 没配置时就是向导第一步（在设备之前）----
//
// 两种取数方式二选一：
//   - API Key：socialdata.tools（填 key，云端拉取，需要公网）
//   - 内置浏览器登录：桌面端直接登 X/微博/小红书…（免 key，走页面自身接口）
// guide 按钮按所选方式给不同入口，不再默认跳 socialdata 申请页。

export function KeyStep({ sdKey, setSdKey, mode, setMode, browser, onOpenSettings }) {
  const providers = (browser && browser.providers) || [];
  const logged = providers.filter((p) => p.connected);
  return (
    <>
      <h4 className="wiz-title">🔑 {t('wizSdTitle')}</h4>
      <p className="wiz-sub">{t('wizSdSub')}</p>

      <div className="wiz-choices" style={{ display: 'flex', gap: 8, marginBottom: 12, flexWrap: 'wrap' }}>
        <button type="button" className={'wiz-choice wiz-mode' + (mode === 'key' ? ' active' : '')}
          style={{ flex: 1, minWidth: 200 }}
          onClick={() => setMode('key')}>
          <b>{t('wizSdModeKey')}</b>
          <span className="hint">{t('wizSdModeKeyHint')}</span>
        </button>
        <button type="button" className={'wiz-choice wiz-mode' + (mode === 'browser' ? ' active' : '')}
          style={{ flex: 1, minWidth: 200 }}
          disabled={!browser || !browser.available}
          onClick={() => setMode('browser')}>
          <b>{t('wizSdModeBrowser')}</b>
          <span className="hint">
            {browser && browser.available ? t('wizSdModeBrowserHint') : t('wizSdModeBrowserOff')}
          </span>
        </button>
      </div>

      {mode === 'key' ? (
        <>
          <label className="field">{t('socialdataKey')}
            <input autoFocus value={sdKey} placeholder="socialdata.tools API Key"
              onChange={(e) => setSdKey(e.target.value)} />
          </label>
          <div className="wiz-actions">
            <span className="hint" style={{ flex: 1 }}>
              {t('wizSdNoKey')}{' '}
              <a className="mini secondary" style={{ textDecoration: 'none', display: 'inline-block' }}
                href="https://socialdata.tools/app/dashboard" target="_blank" rel="noopener">
                {t('wizSdGuide')} ↗
              </a>
            </span>
          </div>
        </>
      ) : (
        <>
          <p className="hint">{t('wizSdBrowserHint')}</p>
          {/* 只列已确认登录的平台：未检测/未登录的在这里没有信息量 */}
          {logged.length ? (
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
              {logged.map((p) => (
                <span key={p.provider} className="badge ok">
                  {p.provider}{p.account ? ' ' + p.account : ''} ✓
                </span>
              ))}
            </div>
          ) : null}
          <div className="wiz-actions" style={{ marginTop: 10 }}>
            <button type="button" className="mini secondary" onClick={onOpenSettings}>
              {t('wizSdGoSettings')} ↗
            </button>
            <span className="hint" style={{ flex: 1 }}>
              {logged.length
                ? t('wizSdBrowserLogged').replace('%s', logged.map((p) => p.provider).join(', '))
                : t('wizSdBrowserNone')}
            </span>
          </div>
        </>
      )}
    </>
  );
}

// ---- Step 1：设备 ----

export function MachineStep({ machines, machineId, setMachineId, adding, setAdding, onCreated, onRefresh }) {
  const toastErr = useToastErr();
  const [form, setForm] = useState({ name: '', addr: '', auth_scope: 'permanent' });
  const [pairing, setPairing] = useState('');
  const [scan, setScan] = useState(null);
  const [scanning, setScanning] = useState(false);
  const [lanErr, setLanErr] = useState('');

  // 换一台设备重新配对时清掉上一台留下的「连不上」提示，避免误导
  useEffect(() => { setLanErr(''); }, [pairing]);

  const scanLan = async () => {
    setScanning(true);
    setScan(null);
    setLanErr('');
    try {
      setScan(await api('/api/machines/discover'));
    } catch (e) {
      if (isLanFailure(e)) setLanErr(e.message);
      toastErr(e.message);
    } finally {
      setScanning(false);
    }
  };

  const create = async () => {
    try {
      const mc = await api('/api/machines', {
        method: 'POST',
        body: JSON.stringify({ name: form.name.trim(), addr: form.addr.trim(), auth_scope: form.auth_scope }),
      });
      onCreated(mc);
      setAdding(false);
      setPairing(mc.id); // 新设备顺手进入配对
    } catch (e) { toastErr(e.message); }
  };

  const selected = machines.find((m) => m.id === machineId);

  return (
    <>
      <h4 className="wiz-title">{t('wizMachinePick')}</h4>
      <p className="wiz-sub">{t('wizMachineSub')}</p>
      {/* 只在扫描/配对真的连不上对端时才提示「本地网络」授权 */}
      <MacLanTip show={!!lanErr} />
      <div className="wiz-list">
        {machines.map((m) => (
          <label key={m.id} className={'wiz-choice' + (machineId === m.id && !adding ? ' active' : '')}>
            <input type="radio" name="wiz-machine" checked={machineId === m.id && !adding}
              onChange={() => { setMachineId(m.id); setAdding(false); }} />
            <div style={{ flex: 1, minWidth: 0 }}>
              <b>{m.name || m.addr}</b>
              <div className="account-tags" style={{ marginTop: 3 }}>
                <span className="badge mut mono">{m.addr}</span>
                {m.creds ? <span className="badge ok">{t('pairedAt')}</span> : <span className="badge warn">{t('notPairedBold')}</span>}
              </div>
            </div>
          </label>
        ))}

        <label className={'wiz-choice' + (adding ? ' active' : '')}>
          <input type="radio" name="wiz-machine" checked={adding} onChange={() => setAdding(true)} />
          <b>{t('wizMachineNew')}</b>
        </label>
      </div>

      {!adding && selected && !selected.creds && (
        <div className="card" style={{ marginTop: 10, padding: 12 }}>
          <p className="hint" style={{ margin: '0 0 8px' }}>⚠ {t('wizMachineUnpairedWarn')}</p>
          {pairing === selected.id
            ? <InlinePair machineId={selected.id} onRefresh={onRefresh} onDone={() => setPairing('')} onLanIssue={(e) => setLanErr(e ? e.message : '')} />
            : <button className="mini" onClick={() => setPairing(selected.id)}>{t('pair')}</button>}
        </div>
      )}

      {adding && (
        <div className="card" style={{ marginTop: 10 }}>
          <div className="form-grid">
            <label className="field">{t('name')}
              <input autoFocus value={form.name} placeholder={t('machineNamePh')}
                onChange={(e) => setForm({ ...form, name: e.target.value })} />
            </label>
            <label className="field">{t('authScope')}
              <select value={form.auth_scope} onChange={(e) => setForm({ ...form, auth_scope: e.target.value })}>
                <option value="permanent">permanent（长期授权）</option>
                <option value="session">session（会话授权）</option>
              </select>
            </label>
            <label className="field span-2">{t('address')}
              <input value={form.addr} placeholder="192.168.1.10（只填 IP，端口默认 8081）"
                onChange={(e) => setForm({ ...form, addr: e.target.value })} />
            </label>
          </div>

          <div className="wiz-scan-row">
            <button className="mini secondary" onClick={scanLan} disabled={scanning}>
              {scanning ? t('wizScanning') : `📡 ${t('wizScanLan')}`}
            </button>
            {scan ? (
              <span className="hint">
                {t('wizLocalIP')}: {(scan.local_ips || []).join(', ') || '-'}
                {' · '}{t('wizSubnets')}: {(scan.subnets || []).join(', ') || '-'}
                {' · '}{t('wizScanned')}: {scan.scanned || 0}
                {typeof scan.elapsed_ms === 'number' ? ` · ${scan.elapsed_ms} ms` : ''}
              </span>
            ) : <span className="hint">{t('wizScanHint')}</span>}
          </div>
          {scan && (
            <div style={{ margin: '4px 0 10px' }}>
              {(scan.devices || []).length ? (
                <div className="wiz-scan-results">
                  {scan.devices.map((d) => (
                    <button key={d.ip} className="mini secondary"
                      title={d.rtt_ms ? `${d.rtt_ms} ms` : ''}
                      onClick={() => setForm((f) => ({ ...f, addr: d.addr, name: f.name || d.ip }))}>
                      {d.addr}
                    </button>
                  ))}
                </div>
              ) : (
                <p className="hint">🔍 {t('wizScanNone')}</p>
              )}
            </div>
          )}

          <div className="wiz-actions">
            <button onClick={create} disabled={!form.addr.trim()}>{`＋ ${t('addMachine')}`}</button>
          </div>
        </div>
      )}
    </>
  );
}

// 内联配对：start → 设备弹码 → confirm。卸载时 cancel，避免设备端停在配对态。
function InlinePair({ machineId, onRefresh, onDone, onLanIssue }) {
  const [status, setStatus] = useState(t('pairRequesting'));
  const [code, setCode] = useState('');
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let dead = false;
    (async () => {
      try {
        await api(`/api/machines/${encodeURIComponent(machineId)}/pair/start`, { method: 'POST' });
        if (dead) return;
        setStatus(t('pairEnterCode'));
        onLanIssue && onLanIssue(null);
      } catch (e) {
        if (dead) return;
        setStatus(t('pairStartFailed') + e.message);
        if (isLanFailure(e)) onLanIssue && onLanIssue(e);
      }
    })();
    return () => {
      dead = true;
      api(`/api/machines/${encodeURIComponent(machineId)}/pair/cancel`, { method: 'POST' }).catch(() => {});
    };
  }, [machineId]);

  const confirm = async () => {
    setBusy(true);
    try {
      await api(`/api/machines/${encodeURIComponent(machineId)}/pair/confirm`, {
        method: 'POST',
        body: JSON.stringify({ code: code.trim() }),
      });
      if (onRefresh) await onRefresh();
      onDone && onDone();
    } catch (e) {
      setStatus(t('pairFailed') + e.message);
      if (isLanFailure(e)) onLanIssue && onLanIssue(e);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div>
      <p className="hint" style={{ margin: '0 0 6px' }}>{status}</p>
      <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
        <input maxLength={6} value={code} placeholder={t('pairCodePlaceholder')}
          onChange={(e) => setCode(e.target.value)}
          style={{ maxWidth: 160, fontSize: 18, letterSpacing: 4 }} />
        <button className="mini" onClick={confirm} disabled={busy || code.trim().length < 4}>
          {t('pairConfirm')}
        </button>
      </div>
    </div>
  );
}

// ---- Step 2：角色 ----

// 导入要观察的页面：静态解析 + 详情识别，成功后把平台/账号/target 默认填进下面的表单。
function PageImport({ edit, setEdit }) {
  const [raw, setRaw] = useState('');
  const [busy, setBusy] = useState(false);
  const [res, setRes] = useState(null);
  const [err, setErr] = useState('');

  const detect = async () => {
    const v = raw.trim();
    if (!v || busy) return;
    setBusy(true);
    setErr('');
    setRes(null);
    let data = null;
    // 先按 Feed 解析；SHOW_LIVE 时失败再按 Live 解析（直播 URL 在 Feed 域通常是非法的）。
    for (const domain of ['feed', ...(SHOW_LIVE ? ['live'] : [])]) {
      try {
        data = await api('/api/source/inspect', {
          method: 'POST',
          body: JSON.stringify({ domain, raw: v }),
        });
        break;
      } catch (e) {
        if (domain === 'live') setErr(e.message);
      }
    }
    setBusy(false);
    if (!data) return;
    setRes(data);
    const tgt = data.target || {};
    const live = tgt.domain === 'live';
    setEdit({
      ...edit,
      platform: uiPlatform(tgt.platform),
      username: live ? '' : normUsername(tgt.id),
      feed_target: live ? '' : (tgt.raw || ''),
      live_url: live ? (tgt.raw || '') : '',
    });
  };

  return (
    <div className="card" style={{ marginTop: 10, padding: 12 }}>
      <div className="bound-source">
        <b>🔎 {t('wizPageImport')}</b>
        <span className="badge mut">{SHOW_LIVE ? 'Feed / Live' : 'Feed'}</span>
      </div>
      <p className="hint" style={{ margin: '4px 0 8px' }}>{t('wizPageImportHint')}</p>
      <div className="account-input">
        <input value={raw} onChange={(e) => setRaw(e.target.value)}
          onKeyDown={(e) => { if (e.key === 'Enter') { e.preventDefault(); detect(); } }}
          placeholder={t('wizPagePlaceholder')} />
        <button type="button" className="mini" onClick={detect} disabled={!raw.trim() || busy}>
          {busy ? t('accountInspecting') : t('accountInspect')}
        </button>
      </div>
      {err ? <p className="hint account-err">⚠ {err}</p> : null}
      {res ? (
        <div className="account-card">
          <div className="account-head">
            {res.account?.avatar_url
              ? <img className="account-avatar" src={res.account.avatar_url} alt="" referrerPolicy="no-referrer" />
              : <div className="account-avatar placeholder">@</div>}
            <div className="account-meta">
              <div className="account-name">
                {res.account?.display_name || res.account?.handle || res.target?.display || res.target?.id}
              </div>
              <div className="hint">
                {res.target?.platform} · {res.target?.kind} · {res.target?.id}
                {res.account?.followers ? ` · ${t(res.target?.kind === 'live_room' || res.target?.kind === 'live_video' ? 'accountOnline' : 'accountFollowers')} ${Number(res.account.followers).toLocaleString()}` : ''}
              </div>
              {res.inspect && !res.inspect.ok ? <div className="hint">ℹ️ {res.inspect.error}</div> : null}
            </div>
          </div>
          <p className="hint" style={{ margin: '6px 0 0' }}>✓ {t('wizPageFilled')}</p>
        </div>
      ) : null}
    </div>
  );
}

export function RoleStep({
  roleList, roleChoice, setRoleChoice, roleDetail, roleLoading,
  roleEdit, setRoleEdit, newRole, setNewRole, platformLabel,
  sdMode, setSdMode, sdKey, setSdKey, saveSdKey, browser, browserLogged, saving,
  onOpenBrowserSettings,
}) {
  const edit = roleChoice === 'new' ? newRole : roleEdit;
  const setEdit = roleChoice === 'new' ? setNewRole : setRoleEdit;
  const set = (key) => (e) => setEdit({ ...edit, [key]: e.target.value });

  return (
    <>
      <h4 className="wiz-title">{t('wizRolePick')}</h4>
      <p className="wiz-sub">{t('wizRoleSub')}</p>

      <PageImport edit={edit} setEdit={setEdit} />

      <div className="card" style={{ marginTop: 10, padding: 12 }}>
        <div className="bound-source">
          <b>🔑 {t('wizSdTitle')}</b>
          <span className="badge mut">{sdMode === 'browser' ? t('wizSdModeBrowser') : t('wizSdModeKey')}</span>
        </div>
        <p className="hint" style={{ margin: '4px 0 8px' }}>{t('wizSdSub')}</p>
        <div className="wiz-choices" style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
          <button type="button" className={'wiz-choice wiz-mode' + (sdMode === 'key' ? ' active' : '')}
            style={{ flex: 1, minWidth: 190 }} onClick={() => setSdMode('key')}>
            <b>{t('wizSdModeKey')}</b>
            <span className="hint">{t('wizSdModeKeyHint')}</span>
          </button>
          <button type="button" className={'wiz-choice wiz-mode' + (sdMode === 'browser' ? ' active' : '')}
            style={{ flex: 1, minWidth: 190 }} disabled={!browser || !browser.available}
            onClick={() => setSdMode('browser')}>
            <b>{t('wizSdModeBrowser')}</b>
            <span className="hint">{browser && browser.available ? t('wizSdModeBrowserHint') : t('wizSdModeBrowserOff')}</span>
          </button>
        </div>
        {sdMode === 'key' ? (
          <div className="account-input" style={{ marginTop: 8 }}>
            <input type="password" autoComplete="off" value={sdKey}
              onChange={(e) => setSdKey(e.target.value)} placeholder="socialdata.tools API Key" />
            <button type="button" className="mini" onClick={saveSdKey} disabled={!sdKey.trim() || saving}>
              {saving ? t('wizSaving') : t('save')}
            </button>
          </div>
        ) : (
          <div className="account-actions" style={{ marginTop: 8 }}>
            <button type="button" className="mini secondary" onClick={onOpenBrowserSettings}>
              {t('wizSdGoSettings')} ↗
            </button>
            <span className="hint">
              {browserLogged
                ? t('wizSdBrowserLogged').replace('%s', (browser?.providers || []).filter((p) => p.connected).map((p) => p.provider).join(', '))
                : t('wizSdBrowserNone')}
            </span>
          </div>
        )}
      </div>

      <div className="wiz-list">
        {roleList.map((r) => (
          <label key={r.id} className={'wiz-choice' + (roleChoice === r.id ? ' active' : '')}>
            <input type="radio" name="wiz-role" checked={roleChoice === r.id}
              onChange={() => setRoleChoice(r.id)} />
            <div style={{ flex: 1, minWidth: 0 }}>
              <b>{r.name || r.id}</b>
              <div className="account-tags" style={{ marginTop: 3 }}>
                <span className={'badge ' + (r.enabled ? 'ok' : 'warn')}>{r.enabled ? t('enabled') : t('disabled')}</span>
                <span className="badge mut">{platformLabel(r.platform)}{r.username ? '/@' + r.username : ''}</span>
                <span className="badge mut">{(r.bindings || []).length} binding{(r.bindings || []).length === 1 ? '' : 's'}</span>
                {slotVoiceVrm(r)
                  ? <span className="badge mut">🎛 {t('assetSourceSlotBadge')}</span>
                  : <>
                      {hasVoice(r) ? <span className="badge ok">🔊</span> : <span className="badge mut">🔇</span>}
                      {r.local_vrm ? <span className="badge ok">🧍</span> : <span className="badge mut">🧍</span>}
                    </>}
              </div>
            </div>
          </label>
        ))}

        <label className={'wiz-choice' + (roleChoice === 'new' ? ' active' : '')}>
          <input type="radio" name="wiz-role" checked={roleChoice === 'new'}
            onChange={() => setRoleChoice('new')} />
          <b>{t('wizRoleNew')}</b>
        </label>
      </div>

      {roleChoice !== 'new' && roleLoading && <p className="hint">⏳ {t('wizRoleLoading')}</p>}

      <div className="card" style={{ marginTop: 10 }}>
        <div className="form-grid">
          <label className="field">{t('name')}
            <input value={edit.name} autoFocus={roleChoice === 'new'}
              placeholder={t('wizRoleNamePh')} onChange={set('name')} />
          </label>
          <label className="field">{t('platform')}
            <select value={edit.platform} onChange={set('platform')}>
              {/* 角色存了非常用平台时保留原值：否则浏览器回落到第一项，保存会把平台写坏 */}
              {!['twitter', 'x', 'weibo', 'xiaohongshu', 'instagram', 'truth', 'bluesky', 'bilibili', 'rss'].includes(edit.platform)
                ? <option value={edit.platform}>{platformLabel(edit.platform)}</option> : null}
              <option value="x">{t('platformX')}</option>
              <option value="weibo">{t('platformWeibo')}</option>
              <option value="xiaohongshu">{t('platformXhs')}</option>
              <option value="instagram">{t('platformInstagram')}</option>
              <option value="truth">{t('platformTruth')}</option>
              <option value="bluesky">{t('platformBluesky')}</option>
              <option value="bilibili">{t('platformBili')}</option>
              <option value="rss">{t('platformRss')}</option>
            </select>
          </label>
          <label className="field">{edit.platform === 'rss' ? t('rssFeed') : t('username')}
            <input value={edit.username} placeholder={edit.platform === 'rss' ? t('usernamePhRss') : t('usernamePh')} onChange={set('username')} />
          </label>
          {edit.live_url ? (
            <label className="field span-2">{t('liveUrl')}
              <input value={edit.live_url} readOnly />
              <span className="hint">{t('wizPageLiveFilled')}</span>
            </label>
          ) : null}
          <label className="field span-2">{t('roleDesc')}
            <input value={edit.description} placeholder={t('roleDescPh')} onChange={set('description')} />
          </label>
          <label className="field span-2">{t('wizPromptLabel')}
            <textarea rows={3} value={edit.prompt} placeholder={t('wizPromptPh')}
              onChange={set('prompt')} />
          </label>
        </div>
        <p className="hint" style={{ margin: 0 }}>{t('wizRoleInfoHint')}</p>
      </div>

      {/* 完整度清单：缺什么列什么，逐项补全；音色/形象要在角色详情页处理，新窗口打开不打断向导 */}
      {(() => {
        if (roleChoice === 'new') return null; // 新角色还没落库，音色/形象创建后在角色页补
        const cur = roleDetail && roleDetail.id === roleChoice ? roleDetail : roleList.find((r) => r.id === roleChoice);
        if (!cur) return null;
        const items = [];
        // 原 slot 模式沿用设备形象/声音，不算缺失（也就没有要去补的资源）
        if (!slotVoiceVrm(cur)) {
          if (!hasVoice(cur)) items.push({ icon: '🔊', text: t('wizMissVoice'), href: '/roles/' + encodeURIComponent(roleChoice) + '/setup' });
          if (!cur.local_vrm) items.push({ icon: '🧍', text: t('wizMissVrm'), href: '/roles/' + encodeURIComponent(roleChoice) + '/setup' });
        }
        if (!items.length) return <p className="wiz-ok" style={{ marginTop: 10 }}>✓ {t('wizAllFilled')}</p>;
        return (
          <div className="card" style={{ marginTop: 10, padding: 12 }}>
            <p className="hint" style={{ margin: '0 0 8px' }}>📋 {t('wizChecklist')}</p>
            {items.map((it) => (
              <div key={it.text} style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '3px 0' }}>
                <span style={{ flex: 1, fontSize: 13 }}>{it.icon} {it.text}</span>
                <a className="mini secondary" style={{ textDecoration: 'none', display: 'inline-block' }}
                  href={it.href} target="_blank" rel="noopener">{t('wizFixGo')} ↗</a>
              </div>
            ))}
            <p className="hint" style={{ margin: '6px 0 0' }}>{t('wizFixLater')}</p>
          </div>
        );
      })()}

      {roleChoice !== 'new' && roleDetail && roleDetail.id === roleChoice && (
        <div className="wiz-summary" style={{ marginTop: 10 }}>
          <SummaryRow label={t('wizRoleBindings')} value={
            (roleDetail.bindings || []).length
              ? (roleDetail.bindings || []).map((b) => `${b.machine_id} · slot ${b.slot}`).join(' / ')
              : t('wizRoleNoBinding')
          } />
          <SummaryRow label={t('wizResource')}
            value={slotVoiceVrm(roleDetail)
              ? t('assetSourceSlotBadge')
              : `${hasVoice(roleDetail) ? t('homeHasVoice') : t('homeNoVoice')} · ${roleDetail.local_vrm ? t('homeHasVrm') : t('homeNoVrm')}`} />
        </div>
      )}
    </>
  );
}

// ---- Step 5：试播 —— 不再是干巴巴的 Finish，拉推文列表选一条点播放 ----

export function TweetPickStep({ role, machine, slot, sdReady, onFinish, onOpenRole, syncInfo }) {
  const toastOk = useToastOk();
  const toastErr = useToastErr();
  const [tweets, setTweets] = useState(null);
  const [loading, setLoading] = useState(false);
  const [playingId, setPlayingId] = useState('');
  const [playedId, setPlayedId] = useState('');

  const load = async (force = false) => {
    if (!role) return;
    setLoading(true);
    try {
      const rows = await api(`/api/roles/${encodeURIComponent(role.id)}/tweets?limit=8${force ? '&force=1' : ''}`);
      setTweets(rows || []);
    } catch (e) { toastErr(e.message); } finally { setLoading(false); }
  };
  useEffect(() => { load(false); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [role?.id]);

  const play = async (tw) => {
    if (!machine) return;
    setPlayingId(tw.id);
    try {
      await api(`/api/roles/${encodeURIComponent(role.id)}/broadcast`, {
        method: 'POST',
        body: JSON.stringify({ tweet_id: tw.id, machine_id: machine.id, slot: +slot }),
      });
      notifyMessageQueued(); // MessageQueue 面板立刻刷新
      setPlayedId(tw.id);
      toastOk(t('wizPlayQueued'));
    } catch (e) { toastErr(e.message); } finally { setPlayingId(''); }
  };

  return (
    <>
      <h4 className="wiz-title">▶ {t('wizPickTitle')}</h4>
      <p className="wiz-sub">{t('wizPickSub')}</p>
      <div className="wiz-summary">
        <SummaryRow label={t('wizTarget')} value={`${machine ? (machine.name || machine.addr) : '-'} · slot ${slot}`} />
        <SummaryRow label={t('roles')} value={role ? (role.name || role.id) : '-'} />
        <SummaryRow label={t('wizSyncState')} value={syncInfo} />
      </div>

      {!sdReady && (
        <p className="hint" style={{ margin: '10px 0 0' }}>🔑 {t('wizSdNeedHint')}</p>
      )}

      <div style={{ marginTop: 10 }}>
        {loading && <p className="hint">⏳ {t('loading')}</p>}
        {!loading && tweets && tweets.length === 0 && (
          <div className="card" style={{ padding: 14, textAlign: 'center' }}>
            <p className="hint" style={{ margin: '0 0 10px' }}>{t('wizNoTweets')}</p>
            <button className="mini" disabled={!sdReady} onClick={() => load(true)}>{t('refreshTweets')}</button>
          </div>
        )}
        {!loading && tweets && tweets.length > 0 && tweets.map((tw) => (
          <div key={tw.id} className={'wiz-choice' + (playedId === tw.id ? ' active' : '')}
            style={{ alignItems: 'flex-start', marginBottom: 6 }}>
            <div style={{ flex: 1, minWidth: 0 }}>
              <div style={{ fontSize: 13, display: '-webkit-box', WebkitLineClamp: 2, WebkitBoxOrient: 'vertical', overflow: 'hidden' }}>
                {tw.text || tw.content || '(no text)'}
              </div>
              <div className="hint" style={{ marginTop: 3 }}>
                {tw.created_at || tw.time ? new Date(tw.created_at || tw.time).toLocaleString() : ''}
              </div>
            </div>
            {playedId === tw.id
              ? <span className="badge ok">✓ {t('wizPlayQueued')}</span>
              : <button className="mini" disabled={!!playingId} onClick={() => play(tw)}>
                  {playingId === tw.id ? t('wizSyncRunning') : `▶ ${t('wizPlay')}`}
                </button>}
          </div>
        ))}
      </div>

      <p className="hint" style={{ marginTop: 10 }}>{t('wizPickHint')}</p>
      <div className="wiz-actions" style={{ justifyContent: 'center', marginTop: 14 }}>
        <button className="secondary" disabled={loading} onClick={() => load(true)}>{t('refreshTweets')}</button>
        <button className="ghost" onClick={onOpenRole}>{t('wizOpenRole')}</button>
        <button onClick={onFinish}>{t('wizFinish')}</button>
      </div>
    </>
  );
}
