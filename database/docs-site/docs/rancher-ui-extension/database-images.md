---
title: Database images
sidebar_position: 6
---

# Database images (administrators)

Databases are built from images stored in Harvester. Administrators manage them under **Database Images** in the cluster's side menu. Other users don't see this entry.

- **Upload Image:** from a URL, or by uploading a file. Use the exact version name as the display name, because the operator finds its image by that name.
- **The list** shows each image's state (**Importing**, **Uploading**, **Ready**, **Failed**), OS version and size.
- **Duplicate names:** two images with the same name in the operator's image namespace make new databases fail. Delete the extra one.

See [Prerequisites](/installation/prerequisites) for the images the operator needs.
