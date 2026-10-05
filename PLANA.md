# Plana ARM64 build

This branch builds upstream v0.7.21 (dc0e3d790559581024a043a78ccb7c62eaee0e96), the version requested by our CLIProxyAPI installer. Plugin application code is unchanged.

The release workflow uses Depot's native ARM64 runner and Debian Bookworm with Go 1.24. It runs Go vet, tests, race tests, migration tests, a native plugin ABI call, and a collector SQLite health check before publishing.

Push a tag matching `v*-plana.*` to publish. Release `v0.7.21-plana.1` contains `cpa-session-archive_0.7.21-plana.1_linux_arm64.zip`, the standalone collector, and checksum files with bare asset filenames.

Install from `Plana-Solutions/cpa-session-archive`, rather than the upstream store entry. The collector needs a separate private service and persistent database; keep its API private. Building these assets does not install the plugin or deploy the collector.
