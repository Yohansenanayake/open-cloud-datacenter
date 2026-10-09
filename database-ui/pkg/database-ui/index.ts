import { importTypes } from '@rancher/auto-import';
import { IPlugin } from '@shell/core/types';
import dbaasRoutes from './routing/dbaas-routing';

// Init the package
export default function(plugin: IPlugin): void {
  // Auto-import model, detail, edit from the folders
  importTypes(plugin);

  // Provide plugin metadata from package.json
  plugin.metadata = require('./package.json');

  // DBaaS rail entry and the per-cluster DBaaS pages
  plugin.addProduct(require('./product'));
  plugin.addRoutes(dbaasRoutes);
}
