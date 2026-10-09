---
title: Connect to a database
sidebar_position: 3
---

# Connect to a database

When the database is **Available**, open it. The **Connection** tab is the first thing you see.

![The Connection tab of a database instance](@site/static/img/rancher-ui/create-9-available.svg)

It shows:
- **Endpoint**: host, port, database and **SSL mode**
- A ready-to-use **JDBC URL** and **psql** command (use the copy icon next to each)
- **Admin Credentials**: the username and password (use **Show** and **Copy**)
- The **CA certificate** (**Download ca.crt**), which your client needs to verify the server

The database's **Events** tab lists recent events if something looks wrong.

- Clients must be on, or routed to, the database's network.
- If you can't see the password, you don't have permission to read Secrets in that namespace. Ask your administrator.

For connecting from your own application, see [Connecting from an application](/connecting).
