import { reactive } from 'vue';
import { HARVESTER_IMAGE } from '../types';

// File uploads started from this browser tab, so the baked-images list can show
// their progress after the upload form is left. Keyed by "<namespace>/<name>".
export const imageUploads = reactive({});

/**
 * Send a file to a Harvester VirtualMachineImage created with sourceType
 * "upload" (Harvester's own API action, the same request Harvester's UI makes;
 * the default backingimage backend takes the file as the "chunk" field).
 */
export async function uploadImageFile(store, clusterId, image, file) {
  const key = `${ image.metadata.namespace }/${ image.metadata.name }`;
  const url = `/k8s/clusters/${ encodeURIComponent(clusterId) }/v1/harvester/${ HARVESTER_IMAGE }s/${ encodeURIComponent(image.metadata.namespace) }/${ encodeURIComponent(image.metadata.name) }?action=upload`;
  const data = new FormData();

  data.append('chunk', file);
  imageUploads[key] = { progress: 0, error: '' };

  try {
    await store.dispatch('management/request', {
      url,
      method:               'post',
      data,
      headers:              { 'Content-Type': 'multipart/form-data', 'File-Size': file.size },
      params:               { size: file.size },
      redirectUnauthorized: false,
      onUploadProgress:     (e) => {
        if (e.total) {
          imageUploads[key].progress = Math.round((e.loaded / e.total) * 100);
        }
      },
    });
    delete imageUploads[key];
  } catch (err) {
    imageUploads[key].error = err?.message || err?._statusText || 'Upload failed';
    throw err;
  }
}
