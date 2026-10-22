# Plana ARM64 build

This branch builds upstream v0.7.21 (dc0e3d790559581024a043a78ccb7c62eaee0e96), the version requested by our CLIProxyAPI installer. Two Plana changes sit on top: native coding-history import (PR #1), and the plugin registers schema 5 so CLIProxyAPI stops sending the full request body with every stream chunk. Schema 2 made the plugin decode each request once per chunk and kept the CPA host at a full CPU core. CLIProxyAPI v7.3.16 and later accept schema 5.

The release workflow uses Depot's native ARM64 runner and Debian Bookworm with Go 1.24. It runs Go vet, tests, race tests, migration tests, a native plugin ABI call, and a collector SQLite health check before publishing.

Push a tag matching `v*-plana.*` to publish. Release `v0.7.21-plana.2` contains `cpa-session-archive_0.7.21-plana.2_linux_arm64.zip`, the standalone collector, the importer, and checksum files with bare asset filenames.

Install from `Plana-Solutions/cpa-session-archive`, rather than the upstream store entry. The collector needs a separate private service and persistent database; keep its API private. Building these assets does not install the plugin or deploy the collector.

In CPAMP, add `https://raw.githubusercontent.com/Plana-Solutions/cpa-session-archive/plana-arm64/registry.json` under Plugins → Plugin Store → Manage. Refresh and install the entry named **CPA Session Archive (Plana ARM64)**. Confirm `Plana-Solutions/cpa-session-archive` when prompted.
