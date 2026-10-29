package browser

import (
	"fmt"
	"strconv"
	"strings"
)

// loginPreload 登录窗注入：按平台需要屏蔽 WebAuthn，并在页面顶部挂一条提示。
func loginPreload(cfg providerConfig) string {
	var b strings.Builder
	if cfg.DisableWebAuthn {
		// Google 检测到 WebAuthn 可用就优先走通行密钥，而 WKWebView 里没有平台
		// 认证器，会卡在「请确保蓝牙处于开启状态」。删掉接口后它回落到密码/TOTP。
		b.WriteString(`(() => {
  try { Object.defineProperty(navigator, 'credentials', { get: () => undefined, configurable: true }); } catch (e) {}
  try { delete window.PublicKeyCredential; } catch (e) {}
  try { window.PublicKeyCredential = undefined; } catch (e) {}
  try {
    if (window.PublicKeyCredential && PublicKeyCredential.isUserVerifyingPlatformAuthenticatorAvailable) {
      PublicKeyCredential.isUserVerifyingPlatformAuthenticatorAvailable = async () => false;
    }
  } catch (e) {}
})();`)
	}
	if cfg.LoginHint != "" {
		b.WriteString(fmt.Sprintf(`(() => {
  const tip = %s;
  const show = () => {
    if (document.getElementById('__trp_login_hint')) return;
    const d = document.createElement('div');
    d.id = '__trp_login_hint';
    d.textContent = tip;
    d.style.cssText = 'position:fixed;left:0;right:0;top:0;z-index:2147483647;padding:8px 12px;'
      + 'font:13px/1.5 system-ui,-apple-system,"PingFang SC",sans-serif;color:#fff;background:#2b5fd9;'
      + 'text-align:center;box-shadow:0 2px 8px rgba(0,0,0,.25)';
    (document.body || document.documentElement).appendChild(d);
  };
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', show);
  else show();
})();`, strconv.Quote(cfg.LoginHint)))
	}
	return b.String()
}

// capturePreload 注入工作窗口：劫持 fetch / XMLHttpRequest，把命中的响应
// 存进 window.__trpCapture（有上限），由 Go 侧 Page.Eval 取回。
func capturePreload(p Provider) string {
	cfg, ok := configFor(p)
	re := ""
	if ok {
		re = cfg.CaptureRe
	}
	if re == "" {
		re = `/api/`
	}
	return fmt.Sprintf(`(() => {
  if (window.__trpCaptureInstalled) return;
  window.__trpCaptureInstalled = true;
  window.__trpCapture = [];
  const MAX = 200;
  const MAX_BODY = 4 * 1024 * 1024;
  const match = new RegExp(%s);
  const push = (url, status, text) => {
    if (!text || text.length > MAX_BODY) return;
    if (window.__trpCapture.length >= MAX) window.__trpCapture.shift();
    window.__trpCapture.push({ url: String(url), status: status, body: text, at: Date.now() });
  };
  try {
    const of = window.fetch;
    window.fetch = async function (input, init) {
      const res = await of.apply(this, arguments);
      try {
        const url = typeof input === 'string' ? input : (input && input.url) || '';
        if (match.test(url)) res.clone().text().then((t) => push(url, res.status, t)).catch(() => {});
      } catch (_) {}
      return res;
    };
  } catch (_) {}
  try {
    const OX = window.XMLHttpRequest;
    function XHR() {
      const xhr = new OX();
      xhr.addEventListener('load', () => {
        try {
          if (match.test(xhr.responseURL)) push(xhr.responseURL, xhr.status, xhr.responseText);
        } catch (_) {}
      });
      return xhr;
    }
    XHR.prototype = OX.prototype;
    window.XMLHttpRequest = XHR;
  } catch (_) {}
})();`, strconv.Quote(re))
}
