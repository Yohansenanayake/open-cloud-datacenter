import {
  duplicateDisplayNames, formatBytes, imageState, isBakedImage, mostUsedNamespace
} from '../../utils/baked-images';

const image = (ns, display, conditions = [], extra = {}) => ({
  metadata: { namespace: ns, labels: { 'dbaas.opencloud.wso2.com/baked-image': 'true' } },
  spec:     { displayName: display },
  status:   { conditions, ...extra },
});

describe('database image helpers', () => {
  it('derives state like Harvester', () => {
    expect(imageState(image('d', 'x', [{ type: 'Imported', status: 'True' }])).state).toBe('ready');
    expect(imageState(image('d', 'x', [{ type: 'Imported', status: 'Unknown' }], { progress: 42 }))).toMatchObject({ state: 'importing', progress: 42 });
    expect(imageState(image('d', 'x', [{
      type: 'RetryLimitExceeded', status: 'True', message: 'gave up'
    }]))).toMatchObject({ state: 'failed', message: 'gave up' });
  });

  it('finds duplicate display names within a namespace only', () => {
    expect(duplicateDisplayNames([image('a', 'r1'), image('a', 'r1'), image('b', 'r1')])).toEqual(['a/r1']);
  });

  it('formats sizes and picks the usual namespace', () => {
    expect(formatBytes(100)).toBe('100 B');
    expect(formatBytes(1610612736)).toBe('1.5 GiB');
    expect(mostUsedNamespace([image('a', '1'), image('b', '2'), image('b', '3')])).toBe('b');
    expect(isBakedImage({ metadata: { labels: {} } })).toBe(false);
  });
});
