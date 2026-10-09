# DBaaS

Database as a Service for Harvester. Self-service managed databases on dedicated virtual machines, provisioned and operated from Rancher through the WSO2 DBaaS operator (`dbaas.opencloud.wso2.com`).

## Features

- **DBaaS** entry in the left navigation, listing the Harvester clusters that have the DBaaS operator installed.
- Database instances: create, edit, start, stop and delete; view connection details and events.
- Snapshots (manual and automated) and restores into new instances.
- OS updates (repave) when a newer database image is available.
- Database Images (admins only): upload and manage the VM images the operator builds instances from.

## Requirements

- Rancher 2.15 or later.
- A Harvester cluster with the DBaaS operator 0.1.0 or later (API `dbaas.opencloud.wso2.com/v1alpha1`) installed. Clusters without the operator do not appear in the list.
