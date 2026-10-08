import { timeUntil } from '../../utils/time';

const now = Date.parse('2026-10-08T06:12:10Z');

describe('timeUntil', () => {
  it('describes future times', () => {
    expect(timeUntil('2026-10-09T02:19:00Z', now)).toEqual({ key: 'dbaas.time.inHours', args: { count: 20 } });
    expect(timeUntil('2026-10-08T06:40:00Z', now)).toEqual({ key: 'dbaas.time.inMinutes', args: { count: 28 } });
  });

  it('returns null for past or invalid times', () => {
    expect(timeUntil('2026-10-07T00:00:00Z', now)).toBeNull();
    expect(timeUntil('nonsense', now)).toBeNull();
  });
});
