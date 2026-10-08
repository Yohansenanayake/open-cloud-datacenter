import { parseEngineEOL, parseOSUpdate, shortDriftMessage } from '../../utils/image-drift';

// Exact message formats from the operator (internal/ensure/repave.go)
const OS = 'VM is on image revision "ubuntu-2204-postgres-v20260515"; revision "ubuntu-2404-postgres-v20260701" available — annotate with dbaas.opencloud.wso2.com/repave-trigger=now to repave';
const EOL = 'engineVersion "15" is not available in revision "ubuntu-2404-postgres-v20260815" (available: [16 17 18]) — migrate data before repaving';

describe('ImageDrift message parsing', () => {
  it('reads an OS update', () => {
    expect(parseOSUpdate(OS)).toEqual({ current: 'ubuntu-2204-postgres-v20260515', target: 'ubuntu-2404-postgres-v20260701' });
  });

  it('reads an engine EOL', () => {
    expect(parseEngineEOL(EOL)).toEqual({
      engineVersion: '15', target: 'ubuntu-2404-postgres-v20260815', supported: ['16', '17', '18']
    });
  });

  it('does not guess at other wording, and drops the kubectl hint', () => {
    expect(parseOSUpdate('something else')).toBeNull();
    expect(parseEngineEOL('engineVersion "15" changed wording')).toBeNull();
    expect(shortDriftMessage(OS)).toBe('VM is on image revision "ubuntu-2204-postgres-v20260515"; revision "ubuntu-2404-postgres-v20260701" available');
  });
});
