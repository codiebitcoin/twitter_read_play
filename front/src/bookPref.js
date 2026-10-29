// ---- book 模式的朗读偏好（localStorage 持久化，刷新/换书后保留）----
// mode:   tts  = 按文字念（设备直读，不过 LLM，长篇推荐）
//         chat = 经设备 LLM（会按角色 prompt 的口吻念，短篇/台词用）
// preface: 是否先念章节标题（"Genesis." / "《第一章 风起》。"）
const MODE_KEY = 'bookMode';
const PREFACE_KEY = 'bookPreface';

export function getBookPrefs() {
  const mode = localStorage.getItem(MODE_KEY);
  const preface = localStorage.getItem(PREFACE_KEY);
  return {
    mode: mode === 'chat' ? 'chat' : 'tts',
    preface: preface === null ? true : preface === '1',
  };
}

export function setBookPrefs(p) {
  localStorage.setItem(MODE_KEY, p.mode === 'chat' ? 'chat' : 'tts');
  localStorage.setItem(PREFACE_KEY, p.preface ? '1' : '0');
}
