[![REUSE status](https://api.reuse.software/badge/github.com/openmcp-project/service-provider-otel-operator)](https://api.reuse.software/info/github.com/openmcp-project/service-provider-otel-operator)

# Service Provider: OTEL Operator

An [OpenMCP](https://github.com/openmcp-project) Service Provider that automates the deployment and lifecycle management of the [OpenTelemetry Operator](https://github.com/open-telemetry/opentelemetry-operator) into Control Planes (CPs) via Helm.

## Overview

This service provider installs the OpenTelemetry Operator for each CP that requests one. It uses the official [opentelemetry-kube-stack Helm chart](https://github.com/open-telemetry/opentelemetry-helm-charts/tree/main/charts/opentelemetry-kube-stack) for both releases: one CRD-only release targets the CP, and one operator-only release targets the workload cluster. Once the operator is running, users can create `OpenTelemetryCollector` custom resources in the CP to configure and manage collector instances.

### Architecture

```
Platform Cluster                  CP (per tenant)
┌─────────────────────┐           ┌──────────────────────────────────────────┐
│  ProviderConfig     │           │  namespace: opentelemetry-operator-system│
│  (cluster-scoped)   │           │                                          │
│  - chartURL         │           │  HelmRelease: <name>-crds                │ ← kube-stack CRDs
│  - pollInterval     │           │    → CRDs (OpenTelemetryCollector, etc.) │
│  - helmValues       │           │                                          │
│  - imagePullSecrets │           │  Workload HelmRelease: <name>-workload   │ ← kube-stack operator
└─────────────────────┘           │                                          │
                                  │  OpenTelemetryCollector CRs              │ ← user creates
Onboarding Cluster                └──────────────────────────────────────────┘
┌──────────────────────┐
│  OtelOperator        │ ← one per CP
│  (per-CP overrides)  │
└──────────────────────┘
```

### Reconciliation Flow

1. Set status to `Progressing`
2. Prepare CP ServiceAccount auth for the workload operator
3. Sync image pull secrets from the platform cluster to the workload cluster (if configured)
4. Create one `opentelemetry-kube-stack` OCIRepository
5. Install or upgrade the CP CRD-only HelmRelease (`<name>-crds`)
6. Install or upgrade the workload operator-only HelmRelease (`<name>-workload`)
7. Set status to `Ready`

On **deletion**, the service provider uninstalls the managed Helm releases. The CRD release uses orphan deletion propagation so existing CP custom resources are not garbage-collected with the HelmRelease.

### cert-manager

The OpenTelemetry Operator Helm chart supports cert-manager for webhook certificate management. By default, this service provider disables cert-manager and uses auto-generated self-signed certificates instead (`admissionWebhooks.autoGenerateCert.enabled: true`). This makes the operator self-contained without requiring cert-manager in the CP.

If cert-manager is available in your CPs, you can enable it via the ProviderConfig's `helmValues`:

```yaml
spec:
  helmValues:
    opentelemetry-operator:
      admissionWebhooks:
        certManager:
          enabled: true
        autoGenerateCert:
          enabled: false
```

## API

### OtelOperator (onboarding cluster)

Created per CP to request an OpenTelemetry Operator installation. All fields are optional overrides — defaults come from the ProviderConfig.

```yaml
apiVersion: oteloperator.services.openmcp.cloud/v1alpha1
kind: OtelOperator
metadata:
  name: my-cp
spec:
  # opentelemetry-kube-stack chart version
  version: "0.20.1"
```

### ProviderConfig (platform cluster)

Cluster-scoped resource that provides default values for all CPs.

```yaml
apiVersion: oteloperator.services.openmcp.cloud/v1alpha1
kind: ProviderConfig
metadata:
  name: oteloperator
spec:
  pollInterval: 1m
  chartURL: "oci://ghcr.io/open-telemetry/opentelemetry-helm-charts/opentelemetry-kube-stack"
  chartPullSecret: my-registry-secret
  helmValues:
    opentelemetry-operator:
      admissionWebhooks:
        certManager:
          enabled: false
        autoGenerateCert:
          enabled: true
```

## Project Structure

```
├── api/
│   ├── v1alpha1/                    # API types (OtelOperator, ProviderConfig)
│   └── crds/                        # Embedded CRD manifests
├── cmd/
│   └── service-provider-otel-operator/  # Entrypoint (init + run commands)
├── internal/
│   ├── controller/                  # Reconciler (CreateOrUpdate / Delete)
│   ├── helm/                        # Helm SDK integration (install/upgrade/uninstall)
│   └── resources/                   # Kubernetes resource helpers
│       ├── constants.go             # Labels
│       ├── namespace.go             # Namespace reconciliation
│       └── imagepullsecret.go       # Image pull secret sync (platform → CP)
├── pkg/
│   └── spruntime/                   # Generic SP/PC reconciler framework
├── test/
│   └── e2e/                         # End-to-end tests
└── hack/                            # Build tooling
```

## Development

### Prerequisites

- Go 1.25+
- [Task](https://taskfile.dev/) (build system)
- Docker (for building images and running e2e tests)
- Access to an OpenMCP environment (for e2e tests)
- On macOS: GNU `realpath` is required for e2e tests (`brew install coreutils`)

### Build

```bash
go build ./...
```

### Docker

```bash
# Build image for current platform (used by e2e tests)
task build:img:build-test
```

### Run Tests

```bash
# Unit tests
task test

# End-to-end tests (requires Docker for kind clusters)
task test-e2e
```

### Generate CRDs and DeepCopy

```bash
task generate
```

### Validate (lint + formatting)

```bash
task validate
```

### CLI Flags

The service provider binary accepts a command (`init` or `run`) as its first argument, followed by flags:

| Flag | Default | Description |
|------|---------|-------------|
| `--environment` | `""` | Name of the environment |
| `--provider-name` | `""` | Name of the provider resource |
| `--metrics-bind-address` | `0` | Address for the metrics endpoint (`:8443` for HTTPS, `:8080` for HTTP, `0` to disable) |
| `--health-probe-bind-address` | `:8081` | Address for health probe endpoint |
| `--leader-elect` | `false` | Enable leader election |
| `--metrics-secure` | `true` | Serve metrics via HTTPS |
| `--enable-http2` | `false` | Enable HTTP/2 for metrics and webhook servers |
| `--verbosity` | | Logging verbosity level |

## Support, Feedback, Contributing

This project is open to feature requests/suggestions, bug reports etc. via [GitHub issues](https://github.com/openmcp-project/service-provider-otel-operator/issues). Contribution and feedback are encouraged and always welcome. For more information about how to contribute, the project structure, as well as additional contribution information, see our [Contribution Guidelines](CONTRIBUTING.md).

## Security / Disclosure

If you find any bug that may be a security problem, please follow our instructions at [in our security policy](https://github.com/openmcp-project/service-provider-otel-operator/security/policy) on how to report it. Please do not create GitHub issues for security-related doubts or problems.

## Code of Conduct

We as members, contributors, and leaders pledge to make participation in our community a harassment-free experience for everyone. By participating in this project, you agree to abide by its [Code of Conduct](https://github.com/SAP/.github/blob/main/CODE_OF_CONDUCT.md) at all times.

## Licensing

Copyright 2025 SAP SE or an SAP affiliate company and service-provider-otel-operator contributors. Please see our [LICENSE](LICENSE) for copyright and license information. Detailed information including third-party components and their licensing/copyright information is available [via the REUSE tool](https://api.reuse.software/info/github.com/openmcp-project/service-provider-otel-operator).
