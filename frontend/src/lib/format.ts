export function formatBytes(n: number): string {
  if (!n || n < 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.min(units.length - 1, Math.floor(Math.log(n) / Math.log(1024)))
  return `${(n / Math.pow(1024, i)).toFixed(i === 0 ? 0 : 1)} ${units[i]}`
}

export function formatSpeed(n: number): string {
  return n > 0 ? `${formatBytes(n)}/s` : ''
}

export function formatEta(seconds: number): string {
  if (!isFinite(seconds) || seconds <= 0) return ''
  const s = Math.round(seconds)
  if (s < 60) return `~${s}s`
  const m = Math.floor(s / 60)
  return `~${m}m ${s % 60}s`
}
