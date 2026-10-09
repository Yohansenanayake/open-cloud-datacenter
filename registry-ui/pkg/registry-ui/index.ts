import { importTypes } from '@rancher/auto-import';
import { IPlugin } from '@shell/core/types';
import registryRoutes from './routing/registry-routing';

// Init the package
export default function(plugin: IPlugin): void {
  // Auto-import models, detail and edit pages from their folders
  importTypes(plugin);

  // Provide plugin metadata from package.json
  plugin.metadata = require('./package.json');

  // Registries rail entry and the per-cluster Registry pages
  plugin.addProduct(require('./product'));
  plugin.addRoutes(registryRoutes);
}
