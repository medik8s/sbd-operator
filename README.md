# SBD Operator

The SBD (STONITH Block Device) operator provides watchdog-based fencing for Kubernetes clusters to ensure high availability through automatic node remediation.

## Documentation

### User Documentation
- **[SBDConfig User Guide](docs/sbdconfig-user-guide.md)** - Complete configuration reference and examples
- **[Quick Reference](docs/sbdconfig-quick-reference.md)** - Essential commands and common configurations
- **[Sample Configuration](config/samples/medik8s_v1alpha1_sbdconfig.yaml)** - Annotated configuration examples

### Technical Documentation
- **[Coordination Strategies](docs/sbd-coordination-strategies.md)** - File locking and coordination mechanisms
- **[Design Documentation](docs/design.md)** - Architecture and design decisions
- **[Concurrent Writes Analysis](docs/concurrent-writes-analysis.md)** - Storage coordination analysis

## Installation

### Standard Kubernetes Installation

```bash
# Build and install the operator
make build-installer
kubectl apply -f dist/install.yaml
```

### OpenShift Installation

For OpenShift clusters, use the OpenShift-specific installer that includes the required SecurityContextConstraints:

```bash
# Build and install the operator with OpenShift support
make build-openshift-installer
kubectl apply -f dist/install-openshift.yaml
```

The OpenShift installer includes:
- All standard operator resources (CRDs, RBAC, deployment)
- SecurityContextConstraints for SBD Agent privileged access
- Proper service account bindings for OpenShift security model

For more details on OpenShift-specific configuration, see [config/openshift/README.md](config/openshift/README.md).

## Building and Pushing Images

### Build Images Locally

To build both images locally (without pushing):

```bash
# Build both images with Quay tags
make quay-build

# Build with a specific version
make quay-build VERSION=v1.2.3
```

### Build and Push to Quay Registry

**First, authenticate with the registry:**

```bash
# Login to Quay
docker login quay.io
```

Then build and push:

```bash
# Build and push with default settings (latest tag)
make quay-build-push

# Build and push with a specific version
make quay-build-push VERSION=v1.2.3

# Build and push to a different registry/organization
make quay-build-push QUAY_REGISTRY=your-registry.com QUAY_ORG=your-org VERSION=v1.2.3

# Build and push multi-platform images (linux/amd64, linux/arm64, etc.)
make quay-buildx VERSION=v1.2.3
```

### Authentication

For pushing images, you need to authenticate with the container registry:

```bash
# For Quay.io
docker login quay.io

# For other registries
docker login your-registry.com
```

### Configuration Variables

The following variables can be customized:

- `QUAY_REGISTRY`: Registry URL (default: `quay.io`)
- `QUAY_ORG`: Organization name (default: `medik8s`)
- `VERSION`: Image tag version (default: `latest`)
- `PLATFORMS`: Target platforms for multi-arch builds (default: `linux/arm64,linux/amd64,linux/s390x,linux/ppc64le`)

### Individual Image Building

You can also build images individually:

```bash
# Build operator image only
make docker-build

# Build SBD agent image only
make docker-build-agent

# Build both binaries locally
make build build-agent
```

## Running End-to-End Tests

### Prerequisites for CRC (OpenShift)

The e2e tests use **CRC (CodeReady Containers)** with OpenShift and **recreate the environment from scratch** for each test run. This ensures clean, consistent testing. You need:

1. **Install CRC**: Download from [Red Hat Developers](https://developers.redhat.com/products/codeready-containers/download)
2. **Setup CRC**: Run `crc setup` once to configure CRC with appropriate resources
3. **Available disk space**: Ensure sufficient disk space as CRC will be stopped/started

### Running E2E Tests

The Makefile handles all setup automatically:

```bash
# Run complete e2e test suite (recreates CRC environment)
make test-e2e

# Skip cleanup after tests (useful for debugging)
E2E_CLEANUP_SKIP=true make test-e2e

# Run with custom image settings
QUAY_REGISTRY=my-registry.io QUAY_ORG=myorg VERSION=dev make test-e2e
```

### What the E2E Setup Does

The `make test-e2e` command automatically:

1. **Stops any existing CRC cluster**
2. **Starts a fresh CRC cluster**
3. **Builds operator and agent container images**
4. **Loads images into CRC's container runtime**
5. **Builds OpenShift installer with SecurityContextConstraints**
6. **Deploys the operator with OpenShift support**
7. **Waits for operator readiness**
8. **Runs the Go test suite**
9. **Cleans up the environment** (unless `E2E_CLEANUP_SKIP=true`)

### Manual Setup (Advanced)

For development, you can run individual steps:

```bash
# Setup CRC environment and deploy operator
make setup-test-e2e

# Run tests only (assumes setup is done)
go test ./test/e2e/ -v -ginkgo.v

# Clean up everything
make cleanup-test-e2e
```

### Environment Variables

- `E2E_CLEANUP_SKIP=true`: Skip cleanup after tests (useful for debugging)
- `CERT_MANAGER_INSTALL_SKIP=true`: Skip CertManager installation
- `QUAY_REGISTRY`: Registry URL (default: `quay.io`)
- `QUAY_ORG`: Organization/namespace (default: `medik8s`)
- `VERSION`: Image version tag (default: `latest`)

### OpenShift vs Kubernetes Differences

When running on OpenShift (CRC), the tests automatically handle:

- **Security Context Constraints (SCC)** deployed via OpenShift installer
- **Privileged pod permissions** for hardware watchdog access
- **OpenShift Routes** for ingress (if applicable)
- **OpenShift Registry** for container images
- **oc** command instead of kubectl where needed

The tests use the OpenShift-specific installer (`build-openshift-installer`) which includes all necessary SecurityContextConstraints for SBD Agent pods to run with required privileges.

The tests are backward compatible with Kind/Kubernetes for development environments.