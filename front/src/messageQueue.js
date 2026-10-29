// 消息队列通知总线：入队/下发操作成功后由发起方 notify 一下，全局 MessageQueue
// 面板立刻拉一次，这样空闲时可以把轮询降到 30s 心跳，不用常驻 2s。
// 覆盖两类消息：播报（broadcast）与设备操作（PATCH设置 / Check&fix / VRM上传 / voice_clone）。
const listeners = new Set();

export function onMessageQueued(fn) {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

export function notifyMessageQueued() {
  for (const fn of listeners) {
    try { fn(); } catch { /* 单个订阅者出错不影响其他 */ }
  }
}