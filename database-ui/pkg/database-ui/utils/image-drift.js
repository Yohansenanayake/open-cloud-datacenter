// Reads the details out of the operator's ImageDrift message, which has no
// structured equivalent yet (yohan-docs known-gaps
// repave-target-revision-not-structured.md in the operator repo). Messages
// (internal/ensure/repave.go):
//   OSUpdateAvailable: VM is on image revision "<current>"; revision "<target>" available — annotate with ...
//   EngineVersionEOL:  engineVersion "<v>" is not available in revision "<target>" (available: [15 16 17]) — migrate data before repaving
// Parsing is strict: if the message doesn't have the expected shape, the
// caller shows the message itself (see shortDriftMessage).

const QUOTED = /"([^"]+)"/g;

function quoted(message) {
  return [...(message || '').matchAll(QUOTED)].map((m) => m[1]);
}

/**
 * @returns {{ current: string, target: string } | null}
 */
export function parseOSUpdate(message) {
  const values = quoted(message);

  return values.length === 2 ? { current: values[0], target: values[1] } : null;
}

/**
 * @returns {{ engineVersion: string, target: string, supported: string[] } | null}
 */
export function parseEngineEOL(message) {
  const values = quoted(message);
  const available = (message || '').match(/\(available: \[([^\]]*)\]\)/);

  if (values.length !== 2 || !available) {
    return null;
  }

  return {
    engineVersion: values[0],
    target:        values[1],
    supported:     available[1].split(/[\s,]+/).filter(Boolean),
  };
}

// The operator's message without its kubectl hint ("— annotate with ...")
export function shortDriftMessage(message) {
  return (message || '').split(' — ')[0].trim();
}
