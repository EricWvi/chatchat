const pad = (n: number) => String(n).padStart(2, "0");

function sameDay(a: Date, b: Date) {
  return a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate();
}

/** "14:05" today, "昨天 14:05", "10月24日 14:05" earlier, with the year when it differs. */
export function formatTime(iso: string, now = new Date()) {
  const t = new Date(iso);
  const hm = `${pad(t.getHours())}:${pad(t.getMinutes())}`;
  if (sameDay(t, now)) return hm;
  const yesterday = new Date(now);
  yesterday.setDate(now.getDate() - 1);
  if (sameDay(t, yesterday)) return `昨天 ${hm}`;
  const tomorrow = new Date(now);
  tomorrow.setDate(now.getDate() + 1);
  if (sameDay(t, tomorrow)) return `明天 ${hm}`;
  const md = `${t.getMonth() + 1}月${t.getDate()}日 ${hm}`;
  return t.getFullYear() === now.getFullYear() ? md : `${t.getFullYear()}年${md}`;
}

/** "刚刚", "3 分钟前", "2 小时前", "昨天"… for last-used times. */
export function formatAgo(iso: string | null, now = new Date()) {
  if (!iso) return "从未";
  const s = (now.getTime() - new Date(iso).getTime()) / 1000;
  if (s < 60) return "刚刚";
  if (s < 3600) return `${Math.floor(s / 60)} 分钟前`;
  if (s < 86400) return `${Math.floor(s / 3600)} 小时前`;
  return formatTime(iso, now);
}

/** "18 分钟后", at least one minute. */
export function minutesUntil(iso: string, now = new Date()) {
  return Math.max(1, Math.ceil((new Date(iso).getTime() - now.getTime()) / 60000));
}
