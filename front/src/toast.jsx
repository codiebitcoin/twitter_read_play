import { createContext, useContext, useState, useCallback, useRef } from 'react';
const ToastCtx = createContext(() => {});

export function ToastProvider({ children }) {
  const [toasts, setToasts] = useState([]);
  const idRef = useRef(0);
  const toast = useCallback((msg, type = '') => {
    const id = ++idRef.current;
    setToasts((ts) => [...ts, { id, msg, type }]);
    setTimeout(() => {
      // 标记 out，350ms 后移除（与原 CSS 动画一致）
      setToasts((ts) => ts.map((x) => (x.id === id ? { ...x, out: true } : x)));
      setTimeout(() => setToasts((ts) => ts.filter((x) => x.id !== id)), 350);
    }, 2600);
  }, []);
  return (
    <ToastCtx.Provider value={toast}>
      {children}
      <div id="toasts" aria-live="polite">
        {toasts.map((x) => (
          <div key={x.id} className={'toast' + (x.type ? ' ' + x.type : '') + (x.out ? ' out' : '')}>{x.msg}</div>
        ))}
      </div>
    </ToastCtx.Provider>
  );
}

export const useToast = () => useContext(ToastCtx);
// 返回的回调必须引用稳定（useCallback）：否则作为 useEffect/useCallback 依赖时
// 会造成「渲染→请求→再渲染」死循环（/api/machines 被无限调用的事故根源）。
export const useToastOk = () => { const t = useToast(); return useCallback((m) => t(m, 'ok'), [t]); };
export const useToastErr = () => { const t = useToast(); return useCallback((m) => t(m, 'bad'), [t]); };
