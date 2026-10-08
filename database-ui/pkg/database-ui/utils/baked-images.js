import { IMAGE_LABEL } from '../types';

// Helpers for DBaaS baked images (Harvester VirtualMachineImages carrying
// IMAGE_LABEL.BAKED). State is derived from the raw object, the same way
// Harvester's own image list does, so it doesn't depend on which model class
// the store uses for the type.

export const IMAGE_STATE = {
  IMPORTING: 'importing',
  READY:     'ready',
  FAILED:    'failed',
};

export function isBakedImage(image) {
  return image?.metadata?.labels?.[IMAGE_LABEL.BAKED] === 'true';
}

function condition(image, type) {
  return (image?.status?.conditions || []).find((c) => c.type === type);
}

/**
 * @returns {{ state: string, progress: number, message: string }}
 */
export function imageState(image) {
  const initialized = condition(image, 'Initialized');
  const imported = condition(image, 'Imported');
  const retryExceeded = condition(image, 'RetryLimitExceeded');
  const progress = image?.status?.progress || 0;
  const message = initialized?.message || imported?.message || retryExceeded?.message || '';

  if (imported?.status === 'True') {
    return {
      state: IMAGE_STATE.READY, progress: 100, message: ''
    };
  }
  if (retryExceeded?.status === 'True' || initialized?.status === 'False' || imported?.status === 'False') {
    return {
      state: IMAGE_STATE.FAILED, progress, message
    };
  }

  return {
    state: IMAGE_STATE.IMPORTING, progress, message
  };
}

// "<namespace>/<display name>" keys used by more than one baked image. In the
// operator's image namespace such a pair makes image resolution ambiguous.
export function duplicateDisplayNames(images) {
  const counts = {};

  images.forEach((i) => {
    const key = `${ i.metadata?.namespace }/${ i.spec?.displayName }`;

    counts[key] = (counts[key] || 0) + 1;
  });

  return Object.keys(counts).filter((k) => counts[k] > 1).sort();
}

const UNITS = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];

export function formatBytes(bytes) {
  if (!bytes) {
    return '';
  }

  let value = bytes;
  let unit = 0;

  while (value >= 1024 && unit < UNITS.length - 1) {
    value /= 1024;
    unit++;
  }

  // Up to two decimals, without trailing zeros (1.5 GiB, 1 GiB, 100 B)
  return `${ unit ? Number(value.toFixed(2)) : value } ${ UNITS[unit] }`;
}

// Namespace that already holds most baked images (a sensible upload default)
export function mostUsedNamespace(images) {
  const counts = {};

  images.forEach((i) => {
    const ns = i.metadata?.namespace;

    counts[ns] = (counts[ns] || 0) + 1;
  });

  return Object.keys(counts).sort((a, b) => counts[b] - counts[a])[0] || '';
}
