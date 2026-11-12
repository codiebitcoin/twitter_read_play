package browser

// Provider 内置浏览器支持的登录平台。
type Provider string

const (
	ProviderX         Provider = "x"
	ProviderWeibo     Provider = "weibo"
	ProviderXHS       Provider = "xiaohongshu"
	ProviderInstagram Provider = "instagram"
	ProviderTruth     Provider = "truth"
	ProviderYouTube   Provider = "youtube"
	ProviderBilibili  Provider = "bilibili"
	ProviderTwitch    Provider = "twitch"
	ProviderBluesky   Provider = "bluesky"
)

// AllProviders 全部平台（前端登录状态区用）。
func AllProviders() []Provider {
	return []Provider{
		ProviderX, ProviderWeibo, ProviderXHS, ProviderInstagram, ProviderTruth,
		ProviderBluesky, ProviderYouTube, ProviderBilibili, ProviderTwitch,
	}
}

type providerConfig struct {
	Name        string
	LoginURL    string
	HomeURL     string
	LogoutURL   string
	ProbeJS     string
	Hosts       []string
	CaptureRe   string
	CookieKeys  []string
	StorageKeys []string
	// DisableWebAuthn 登录窗里屏蔽 WebAuthn：Google 在 WKWebView 里会弹
	// 「请确保蓝牙处于开启状态」的通行密钥提示，屏蔽后回落到密码/TOTP。
	DisableWebAuthn bool
	// UserAgent 登录/工作窗的自定义 UA。空 = 用 WebView 默认。
	// Google 对非浏览器 UA 会拒绝登录，X 也会给老 UA 降级页面，所以统一伪装成 Chrome。
	UserAgent string
	// ReloadProbe 探针前重新加载 HomeURL：登录态只体现在服务端渲染的页面
	// 状态里（小红书 __INITIAL_STATE__），工作窗停在登录前的旧页面会误判。
	ReloadProbe bool
	// LogoutJS 没有服务端登出 URL 的平台（Bluesky 把会话存在 localStorage）
	// 在销毁工作窗前执行：否则下次探测又会把旧会话读回来。
	LogoutJS string
	// LoginHintKey 登录窗顶栏提示的 i18n 词条 key（文案在 internal/i18n，en 兜底）。
	LoginHintKey string
	// LoginOptional 未登录也能抓公开数据（微博/Truth Social）。前端据此
	// 把「登录后可用」换成「游客态也可用」，不误导用户去登录。
	LoginOptional bool
}

// desktopChromeUA 伪装成 macOS 上的 Chrome：WKWebView 默认 UA 里带 App 名，
// 部分站点（X）会因此拒绝或降级页面。
const desktopChromeUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"

// safariUA 与 WKWebView 的真实引擎一致。Google 会按 UA 走不同登录分支，
// 把 Safari 内核伪装成 Chrome 容易触发它只给 Chrome 用的接口，因此 YouTube 用这个。
const safariUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 " +
	"(KHTML, like Gecko) Version/18.0 Safari/605.1.15"

var providerConfigs = map[Provider]providerConfig{
	ProviderX: {
		Name:      "X",
		LoginURL:  "https://x.com/i/flow/login",
		HomeURL:   "https://x.com/home",
		LogoutURL: "https://x.com/logout",
		ProbeJS: `(async () => {
  const readCookie = (name) => {
    const m = (document.cookie || '').match(new RegExp('(?:^|; )' + name + '=([^;]*)'));
    return m ? decodeURIComponent(m[1]) : '';
  };
  // 工作窗导航失败时停在空白/错误页，document.cookie 必然为空 —— 那不是「未登录」。
  const onSite = /(^|\.)x\.com$/.test(location.hostname);
  const twid = readCookie('twid'); // 登录后由服务端写入（u=<user id>），游客没有
  try {
    const r = await fetch('https://x.com/i/api/1.1/account/settings.json', { credentials: 'include' });
    if (r.status === 200) {
      const j = await r.json();
      return { checked: true, connected: true, account: j.screen_name ? ('@' + j.screen_name) : '' };
    }
    if (r.status === 401 || r.status === 403) {
      // 403 也可能只是缺 csrf 头；twid 存在说明会话仍在
      return { checked: true, connected: !!twid, account: '' };
    }
    return { checked: !!twid, connected: !!twid, account: '' };
  } catch (e) {
    // 网络不通（国内常见）：twid 是可读的登录凭据，能据此下结论。
    if (twid) return { checked: false, connected: true, account: '' };
    if (!onSite) return { checked: false, connected: false, error: 'x.com 页面未加载（当前 ' + location.href + '）' };
    return { checked: true, connected: false, account: '' };
  }
})()`,
		Hosts:      []string{"x.com"},
		CaptureRe:  `/i/api/graphql/.*/(UserByScreenName|UserTweets|UserOriginalsTimeline|UserWithRepliesTimeline|UserMediaTimeline)`,
		CookieKeys: []string{"ct0", "twid", "guest_id"},
	},
	ProviderWeibo: {
		Name:      "微博",
		LoginURL:  "https://passport.weibo.com/sso/signin?entry=miniblog&source=miniblog",
		HomeURL:   "https://m.weibo.cn/",
		LogoutURL: "https://passport.weibo.com/signout",
		ProbeJS: `(async () => {
  try {
    const r = await fetch('https://m.weibo.cn/api/config', { credentials: 'include' });
    if (!r.ok) return { checked: false, connected: false, error: 'HTTP ' + r.status };
    const j = await r.json();
    const d = j && j.data ? j.data : {};
    return { checked: true, connected: !!d.login, account: d.uid ? String(d.uid) : '' };
  } catch (e) { return { checked: false, connected: false, error: String(e) }; }
})()`,
		Hosts:      []string{"m.weibo.cn", "weibo.com"},
		CaptureRe:  `/api/container/getIndex`,
		CookieKeys: []string{"SUBP", "ALF", "SSOLoginState"},
		// 游客态也能抓公开时间线，登录只是拿得更全。
		LoginOptional: true,
	},
	ProviderXHS: {
		Name:      "小红书",
		LoginURL:  "https://www.xiaohongshu.com/explore",
		HomeURL:   "https://www.xiaohongshu.com/explore",
		LogoutURL: "https://www.xiaohongshu.com/explore",
		// 小红书没有可用的 selfinfo/me 探针（无签名头直接 406/500），登录态只体现在
		// 页面服务端渲染的 __INITIAL_STATE__.user 里，因此配合 ReloadProbe 重新加载首页。
		ProbeJS: `(async () => {
  const read = () => {
    const s = window.__INITIAL_STATE__ || {};
    const u = s.user || {};
    const info = (u.userInfo && (u.userInfo.nickname || u.userInfo.redId) ? u.userInfo : null)
      || (u.userPageData && u.userPageData.basicInfo) || {};
    return {
      has: !!window.__INITIAL_STATE__,
      logged: !!u.loggedIn,
      account: info.nickname || info.nickName || info.redId || '',
    };
  };
  let st = read();
  if (st.logged) return { checked: true, connected: true, account: st.account };
  // SSR 可能晚于 readyState：等一轮再读
  for (let i = 0; i < 10 && !st.logged; i++) {
    await new Promise((r) => setTimeout(r, 300));
    st = read();
  }
  // 页面有 SSR 状态才代表拿到了明确答复；否则保持上次状态。
  return { checked: st.has, connected: st.logged, account: st.account };
})()`,
		Hosts:       []string{"www.xiaohongshu.com", "edith.xiaohongshu.com"},
		CaptureRe:   `/api/sns/web/v\d/(user_posted|user/otherinfo|search/notes|search/users)`,
		CookieKeys:  []string{"a1", "webId", "gid", "web_session"},
		ReloadProbe: true,
	},
	ProviderInstagram: {
		Name:      "Instagram",
		LoginURL:  "https://www.instagram.com/accounts/login/",
		HomeURL:   "https://www.instagram.com/",
		LogoutURL: "https://www.instagram.com/accounts/logout/",
		ProbeJS: `(async () => {
  const readCookie = (name) => {
    const m = (document.cookie || '').match(new RegExp('(?:^|; )' + name + '=([^;]*)'));
    return m ? decodeURIComponent(m[1]) : '';
  };
  // 页面没加载成功时 cookie 一定是空的，不能据此判定未登录。
  const onSite = /(^|\.)instagram\.com$/.test(location.hostname);
  const uid = readCookie('ds_user_id'); // 登录后写入，游客没有
  try {
    // current_user 需要 App ID 头；未登录时返回 401/重定向。
    const r = await fetch('https://www.instagram.com/api/v1/accounts/current_user/?edit=true', {
      credentials: 'include',
      headers: {
        'X-IG-App-ID': '936619743392459',
        // 页面自身请求都带这两个头；缺了容易被判成非浏览器流量直接 401。
        'X-ASBD-ID': '129477',
        'X-IG-WWW-Claim': '0',
      },
    });
    if (r.ok) {
      const j = await r.json();
      const u = (j && (j.user || j)) || {};
      if (u.username) return { checked: true, connected: true, account: '@' + u.username };
      if (uid) return { checked: true, connected: true, account: '' };
      // 200 但没有 user（例如 {"message":"login_required"}）= 明确未登录
      return { checked: true, connected: false, account: '' };
    }
    // 401 = 服务端明确说这个会话无效。ds_user_id 可能是上次登录残留的
    // 半截状态（只有 ds_user_id、没有 HttpOnly 的 sessionid），不能当凭据。
    // 有 ds_user_id 却 401：登录大多停在验证码/手机确认这一步，把原因说清楚。
    if (r.status === 401) {
      return {
        checked: true,
        connected: false,
        account: '',
        error: uid ? '登录未完成：只有账号记录、没有有效会话。请重新登录，完成验证码/手机确认，看到首页信息流后再关闭窗口' : '',
      };
    }
    // 403 通常是风控/缺 App-ID，不能据此下结论，退回 cookie 判断。
    if (r.status === 403) return { checked: true, connected: !!uid, account: '' };
  } catch (e) {
    // 网络不通时 ds_user_id 是可读的登录凭据。
    if (uid) return { checked: false, connected: true, account: '' };
    if (!onSite) return { checked: false, connected: false, error: 'instagram.com 页面未加载（当前 ' + location.href + '）' };
    return { checked: true, connected: false, account: '' };
  }
  return { checked: !!uid, connected: !!uid, account: '' };
})()`,
		Hosts:      []string{"www.instagram.com", "i.instagram.com", "graph.instagram.com"},
		CaptureRe:  `(/api/v1/(feed|users)/|/graphql/query)`,
		CookieKeys: []string{"ds_user_id", "sessionid", "csrftoken", "mid"},
		// Instagram 也会用通行密钥；WKWebView 没有平台认证器，屏蔽后回落到密码 + 验证码。
		DisableWebAuthn: true,
		// 只输入密码不算登录成功：Instagram 在验证码/手机确认之后才写 sessionid。
		LoginHintKey: "login.hint.x",
	},
	ProviderTruth: {
		Name:      "Truth Social",
		LoginURL:  "https://truthsocial.com/auth/sign_in",
		HomeURL:   "https://truthsocial.com/",
		LogoutURL: "https://truthsocial.com/auth/sign_out",
		ProbeJS: `(async () => {
  let last = '';
  // Cloudflare 挑战页刚过时接口还会短暂失败，重试几次再下结论。
  for (let i = 0; i < 4; i++) {
    try {
      // Mastodon 风格：登录态用 verify_credentials 判断；未登录 401。
      const r = await fetch('https://truthsocial.com/api/v1/accounts/verify_credentials', {
        credentials: 'include', headers: { Accept: 'application/json' },
      });
      if (r.status === 200) {
        const j = await r.json().catch(() => null);
        return { checked: true, connected: true, account: j && j.username ? ('@' + j.username) : '' };
      }
      if (r.status === 401 || r.status === 403) return { checked: true, connected: false, account: '' };
      last = 'HTTP ' + r.status;
    } catch (e) {
      last = String(e);
    }
    await new Promise((r) => setTimeout(r, 800));
  }
  return { checked: false, connected: false, error: last };
})()`,
		Hosts:      []string{"truthsocial.com", "www.truthsocial.com"},
		CaptureRe:  `/api/v1/accounts/`,
		CookieKeys: []string{"_session_id", "cf_clearance"},
		// 公开账号的时间线游客也能读；登录只影响「关注流/私信」等。
		LoginOptional: true,
	},
	ProviderBluesky: {
		Name: "Bluesky",
		// bsky.app 没有独立的登录页：未登录进首页直接是登录/注册界面，
		// 已登录则是信息流，所以登录/首页用同一个地址。
		LoginURL: "https://bsky.app/",
		HomeURL:  "https://bsky.app/",
		// 没有服务端登出地址，退出靠 LogoutJS 清 localStorage。
		LogoutURL: "",
		// 网页端把整个持久化状态（含 session.accounts[]）序列化进
		// localStorage['BSKY_STORAGE']，登录态只能从这里读，没有 cookie/探针接口。
		ProbeJS: `(() => {
  // 只有页面真的停在 bsky.app 时 localStorage 才是这个源的；空白页/报错页保持无结论。
  if (!/(^|\.)bsky\.app$/.test(location.hostname)) {
    return { checked: false, connected: false, error: 'bsky.app 页面未加载（当前 ' + location.href + '）' };
  }
  const read = () => {
    let raw = '';
    try { raw = localStorage.getItem('BSKY_STORAGE') || ''; } catch (e) { return null; }
    if (!raw) return { connected: false, account: '' };
    let st = null;
    try { st = JSON.parse(raw); } catch (e) { return null; }
    const s = (st && st.session) || {};
    const accounts = Array.isArray(s.accounts) ? s.accounts : [];
    const cur = s.currentAccount || {};
    // 登出后 accounts 里可能残留资料但 token 已被清空，只有带 token 才算登录。
    const live = accounts.filter((a) => a && (a.accessJwt || a.refreshJwt));
    if (!live.length) return { connected: false, account: '' };
    const me = live.find((a) => a.did && a.did === cur.did) || live[0];
    return {
      connected: true,
      account: me.handle ? '@' + String(me.handle).replace(/^@/, '') : '',
    };
  };
  // localStorage 是同步读的，且所有窗口共享同一个默认数据源：
  // 登录窗写完这里就能立刻看到，不需要轮询（轮询只会让「未登录」多等几秒）。
  const st = read();
  if (!st) return { checked: false, connected: false, error: 'BSKY_STORAGE 解析失败' };
  return { checked: true, connected: st.connected, account: st.account };
})()`,
		// 没有服务端登出接口：清掉本地会话即可（工作窗与登录窗共享 profile）。
		LogoutJS: `(() => {
  try { localStorage.removeItem('BSKY_STORAGE'); } catch (e) {}
  return { ok: true };
})()`,
		Hosts:       []string{"bsky.app", "public.api.bsky.app", "api.bsky.app", "bsky.social"},
		CaptureRe:   `/xrpc/app\.bsky\.(feed|actor)\.`,
		StorageKeys: []string{"BSKY_STORAGE"},
		// 公开 AppView 不需要登录即可读主页/时间线；登录只影响关注流、私信等。
		LoginOptional: true,
	},
	ProviderYouTube: {
		Name:      "YouTube",
		LoginURL:  "https://accounts.google.com/ServiceLogin?service=youtube",
		HomeURL:   "https://www.youtube.com/",
		LogoutURL: "https://accounts.google.com/Logout",
		ProbeJS: `(async () => {
  // ytcfg 在页面脚本执行后才挂上；某些地区首页还会先给同意页。
  // 同源拉一次首页 HTML，从序列化的 ytcfg 里读 LOGGED_IN 最稳。
  for (let i = 0; i < 8; i++) {
    const c = window.ytcfg;
    if (c && c.get) return { checked: true, connected: !!c.get('LOGGED_IN'), account: '' };
    await new Promise((r) => setTimeout(r, 250));
  }
  try {
    const r = await fetch('https://www.youtube.com/', { credentials: 'include' });
    const html = await r.text();
    const m = html.match(/"LOGGED_IN":(true|false)/);
    if (m) return { checked: true, connected: m[1] === 'true', account: '' };
    return { checked: false, connected: false, error: '首页未返回登录态' };
  } catch (e) {
    return { checked: false, connected: false, error: String(e) };
  }
})()`,
		Hosts:       []string{"www.youtube.com"},
		CaptureRe:   `/youtubei/v1/(browse|live_chat|next)`,
		CookieKeys:  []string{"VISITOR_INFO1_LIVE", "PREF", "SID", "HSID", "SSID", "APISID", "SAPISID"},
		StorageKeys: []string{"yt-player-volume"},
		// Google 在 WKWebView 里没有蓝牙/平台认证器，通行密钥流程会卡在
		// 「请确保蓝牙处于开启状态」；屏蔽 WebAuthn 后登录窗回落到密码 + TOTP。
		DisableWebAuthn: true,
		// Google 按 UA 分支：伪装成 Chrome 会走到只给 Chrome 的登录接口，
		// 保持 Safari UA 与 WKWebView 引擎一致最稳。
		UserAgent: safariUA,
		LoginHintKey: "login.hint.instagram",
	},
	ProviderBilibili: {
		Name:      "Bilibili",
		LoginURL:  "https://passport.bilibili.com/login",
		HomeURL:   "https://www.bilibili.com/",
		LogoutURL: "https://passport.bilibili.com/logout",
		ProbeJS: `(async () => {
  const readCookie = (name) => {
    const m = (document.cookie || '').match(new RegExp('(?:^|; )' + name + '=([^;]*)'));
    return m ? decodeURIComponent(m[1]) : '';
  };
  const dede = readCookie('DedeUserID'); // 登录后写入，游客没有
  try {
    const r = await fetch('https://api.bilibili.com/x/web-interface/nav', { credentials: 'include' });
    if (!r.ok) return { checked: !!dede, connected: !!dede, account: '' };
    const j = await r.json();
    const d = (j && j.data) || {};
    if (d.isLogin) return { checked: true, connected: true, account: d.uname || '' };
    return { checked: true, connected: false, account: '' };
  } catch (e) {
    return { checked: !!dede, connected: !!dede, account: '' };
  }
})()`,
		Hosts:      []string{"api.bilibili.com", "api.live.bilibili.com", "live.bilibili.com"},
		CaptureRe:  `(api\.bilibili\.com|api\.live\.bilibili\.com)/`,
		CookieKeys: []string{"bili_jct", "DedeUserID", "buvid3", "buvid4"},
	},
	ProviderTwitch: {
		Name:      "Twitch",
		LoginURL:  "https://www.twitch.tv/login",
		HomeURL:   "https://www.twitch.tv/",
		LogoutURL: "https://www.twitch.tv/logout",
		ProbeJS: `(async () => {
  const readCookie = (name) => {
    const m = (document.cookie || '').match(new RegExp('(?:^|; )' + name + '=([^;]*)'));
    return m ? decodeURIComponent(m[1]) : '';
  };
  // 登录凭据可能在 localStorage，也可能只写进 cookie：auth-token cookie 是
  // 令牌本体，twilight-user 是含 authToken/login 的 JSON。
  const readLocalUser = () => {
    try { const u = JSON.parse(readCookie('twilight-user') || '{}'); return u.login || u.displayName || ''; } catch (e) { return ''; }
  };
  const readAuth = () => {
    try { const v = localStorage.getItem('auth-token'); if (v) return v; } catch (e) {}
    const t = readCookie('auth-token');
    if (t) return t;
    try { const u = JSON.parse(readCookie('twilight-user') || '{}'); return u.authToken || ''; } catch (e) { return ''; }
  };
  try {
    const r = await fetch('https://gql.twitch.tv/gql', {
      method: 'POST', credentials: 'include',
      headers: { 'Client-ID': 'kimne78kx3ncx6brgo4mv6wki5h1ko', 'Content-Type': 'application/json' },
      body: JSON.stringify([{ operationName: 'CurrentUser', variables: {}, query: 'query CurrentUser { currentUser { login displayName } }' }]),
    });
    const j = await r.json();
    const u = j && j[0] && j[0].data ? j[0].data.currentUser : null;
    if (u) return { checked: true, connected: true, account: u.login || u.displayName || '' };
    if ((r.status === 401 || r.status === 403) && readAuth()) {
      // 国内直连 gql 常被拦/限流，但本地凭据仍在，按已登录处理。
      return { checked: false, connected: true, account: readLocalUser() };
    }
    if (r.status === 401 || r.status === 403) return { checked: true, connected: false, error: 'HTTP ' + r.status };
    if (!r.ok) return { checked: false, connected: false, error: 'HTTP ' + r.status };
    if (readAuth()) return { checked: false, connected: true, account: readLocalUser() };
    return { checked: true, connected: false };
  } catch (e) {
    // 国内直连 gql 经常 Load failed；本地凭据是可靠的登录依据。
    if (readAuth()) return { checked: false, connected: true, account: readLocalUser() };
    return { checked: false, connected: false, error: String(e) };
  }
})()`,
		Hosts:       []string{"gql.twitch.tv", "www.twitch.tv", "api.twitch.tv"},
		CaptureRe:   `(gql\.twitch\.tv|/gql)`,
		CookieKeys:  []string{"persistent_user_id", "auth-token", "twilight-user"},
		StorageKeys: []string{"auth-token"},
	},
}

func configFor(p Provider) (providerConfig, bool) {
	c, ok := providerConfigs[p]
	return c, ok
}

// providerUA 该平台使用的 UA：配置里没写就统一用桌面 Chrome UA。
func providerUA(cfg providerConfig) string {
	if cfg.UserAgent != "" {
		return cfg.UserAgent
	}
	return desktopChromeUA
}

// ValidProvider provider 是否受支持。
func ValidProvider(p Provider) bool {
	_, ok := providerConfigs[p]
	return ok
}

// HomeURLFor 平台首页（选用户窗在角色还没配目标时的起点）。未知平台回 X 首页。
func HomeURLFor(p Provider) string {
	if c, ok := configFor(p); ok && c.HomeURL != "" {
		return c.HomeURL
	}
	return "https://x.com/home"
}

func (c providerConfig) hostAllowed(host string) bool {
	for _, h := range c.Hosts {
		if h == host {
			return true
		}
	}
	return false
}
