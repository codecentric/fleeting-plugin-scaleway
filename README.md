# fleeting-plugin-scaleway

GitLab Runner fleeting plugin for [Scaleway Apple Silicon](https://www.scaleway.com/en/developers/api/apple-silicon/) (Mac mini M1/M2/M2-Pro dedicated servers).

## Overview

This plugin implements the [GitLab fleeting](https://docs.gitlab.com/runner/executors/docker_autoscaler.html) `InstanceGroup` interface, allowing the GitLab Runner docker-autoscaler executor to dynamically provision and deprovision Scaleway Apple Silicon servers.

> **Note:** Scaleway Apple Silicon servers have a **minimum 24-hour lease**. The plugin will provision servers on demand, but billing starts immediately and the server cannot be deleted before the lease expires.

## Plugin Configuration

| Parameter | Type | Required | Description |
|---|---|---|---|
| `secret_key` | string | No* | Scaleway API secret key. Defaults to `SCW_SECRET_KEY` env var. |
| `project_id` | string | No* | Scaleway project ID. Defaults to `SCW_DEFAULT_PROJECT_ID` env var. |
| `zone` | string | No | Availability zone. Defaults to `fr-par-3` (the only Apple Silicon zone). |
| `server_type` | string | **Yes** | Server type to provision, e.g. `M2-M`, `M2-L`, `M1-M`. |
| `os_id` | string | No | OS UUID to install. When omitted the default OS for the server type is used. |
| `name` | string | **Yes** | Logical name for this instance group. Used to tag/identify servers. |

\* Can be provided via environment variable instead.

### Default connector config

| Parameter | Default |
|---|---|
| `os` | `darwin` |
| `protocol` | `ssh` |
| `arch` | `arm64` |

## Authentication

Provide credentials in one of two ways:

1. **Plugin config** — set `secret_key` and `project_id` in `plugin_config`.
2. **Environment variables** — set `SCW_SECRET_KEY` and `SCW_DEFAULT_PROJECT_ID` on the runner process.

## Example runner config

```toml
concurrent = 4
check_interval = 0
shutdown_timeout = 0
log_level = "info"

[[runners]]
  name = "scaleway-mac-runner"
  url = "https://gitlab.com"
  token = "<GITLAB_RUNNER_TOKEN>"
  executor = "docker-autoscaler"
  shell = "bash"

  [runners.autoscaler]
    capacity_per_instance = 1
    max_use_count = 10
    max_instances = 4
    plugin = "fleeting-plugin-scaleway"

    [runners.autoscaler.plugin_config]
      name         = "mac-runners"
      server_type  = "M2-M"
      zone         = "fr-par-3"
      # secret_key and project_id can also be set via SCW_SECRET_KEY /
      # SCW_DEFAULT_PROJECT_ID environment variables.
      secret_key   = "<SCW_SECRET_KEY>"
      project_id   = "<SCW_PROJECT_ID>"

    [runners.autoscaler.connector_config]
      username               = "m1"
      key_path               = "/etc/gitlab-runner/id_ed25519"
      use_static_credentials = true
      keepalive              = "30s"
      timeout                = "10m"

    [[runners.autoscaler.policy]]
      idle_count = 1
      idle_time  = "30m"
```

## Installation

### OCI registry (recommended)

The plugin is distributed as an OCI artifact on GHCR. Installation is a two-step process:

**1. Set the `plugin` field in `config.toml`:**

```toml
[runners.autoscaler]
  plugin = "ghcr.io/codecentric/fleeting-plugin-scaleway:latest"
```

Use a version constraint to pin a specific release:

```toml
  plugin = "ghcr.io/codecentric/fleeting-plugin-scaleway:0"     # latest 0.x.x
  plugin = "ghcr.io/codecentric/fleeting-plugin-scaleway:0.1"   # latest 0.1.x
  plugin = "ghcr.io/codecentric/fleeting-plugin-scaleway:0.1.0" # exact version
```

**2. Install the plugin binary onto the runner host:**

```bash
gitlab-runner fleeting install
```

This downloads the correct binary for the host OS and architecture and installs it to `~/.config/fleeting/plugins/` (or `%APPDATA%\fleeting\plugins\` on Windows). It only needs to be run once, or again when you want to update the plugin version.

After installation, `gitlab-runner run` will use the locally installed binary — it does **not** pull from the OCI registry at runtime.

### Manual binary installation

Download a binary from the [Releases](https://github.com/codecentric/fleeting-plugin-scaleway/releases) page, place it somewhere on `$PATH`, and name it `fleeting-plugin-scaleway`. Then reference it by that name:

```toml
[runners.autoscaler]
  plugin = "fleeting-plugin-scaleway"
```

## Building from source

```bash
go build ./cmd/fleeting-plugin-scaleway
```

Cross-compile all platforms (output goes to `dist/`):

```bash
make all
```

## Caveats

- The **24-hour minimum lease** means autoscaling costs are dominated by the lease duration, not actual usage. Configure `idle_time` and `max_use_count` accordingly.
- Only **`fr-par-3`** is currently available for Apple Silicon.
- Server names are prefixed with `fleeting-<name>-` so the plugin can identify its own servers across API calls.
- SSH credentials must be pre-configured on the base OS image or injected through the Scaleway runner configuration. The plugin itself does not inject SSH keys.
