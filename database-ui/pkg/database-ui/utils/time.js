// "in about N <unit>" for a future timestamp (Shell's LiveDate only does "ago").
// Returns an i18n key and args, or null when the time is not in the future.
export function timeUntil(iso, now = Date.now()) {
  const target = new Date(iso).getTime();

  if (!iso || Number.isNaN(target) || target <= now) {
    return null;
  }

  const minutes = Math.round((target - now) / 60000);

  if (minutes < 60) {
    return { key: 'dbaas.time.inMinutes', args: { count: Math.max(minutes, 1) } };
  }
  if (minutes < 48 * 60) {
    return { key: 'dbaas.time.inHours', args: { count: Math.round(minutes / 60) } };
  }

  return { key: 'dbaas.time.inDays', args: { count: Math.round(minutes / 1440) } };
}
