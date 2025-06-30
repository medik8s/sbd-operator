# SBD Operator End-to-End (E2E) Tests

This directory contains comprehensive end-to-end tests for the SBD Operator that simulate real failure scenarios to validate the operator's remediation capabilities.

## Overview

The E2E tests go beyond basic functionality validation to test the operator's behavior under realistic failure conditions. These tests use **real AWS infrastructure disruptions** to simulate the types of failures that would trigger SBD remediation in production environments.

## Test Categories

### 1. Basic Configuration Tests
- SBD operator deployment and configuration
- Agent DaemonSet creation and readiness
- Basic cluster topology discovery

### 2. AWS-Based Disruption Tests
- **Network Communication Failures**: Uses AWS Security Groups to block network traffic
- **Storage Access Interruptions**: Uses AWS EBS volume detachment to simulate storage failures
- **Node Recovery Scenarios**: Tests automatic recovery after disruptions are removed

### 3. Resilience Tests
- SBD agent crash and recovery
- Non-fencing failure handling
- Large cluster coordination

## Prerequisites

### Cluster Requirements
- **AWS-based Kubernetes cluster** (EKS, OpenShift on AWS, or self-managed)
- At least 3 worker nodes for safe disruption testing
- Nodes must have AWS provider IDs (format: `aws:///region/instance-id`)

### AWS Requirements
- AWS credentials configured (via environment variables, IAM roles, or AWS CLI)
- Required AWS IAM permissions (see [AWS Permissions](#aws-permissions) section)
- Cluster must be running on AWS EC2 instances

### Software Requirements
- `kubectl` configured to access the cluster
- `ginkgo` test framework
- Go 1.21+ for building tests

## AWS Permissions

The E2E tests require the following AWS IAM permissions:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "ec2:DescribeInstances",
        "ec2:DescribeVolumes", 
        "ec2:DescribeSecurityGroups",
        "ec2:CreateSecurityGroup",
        "ec2:DeleteSecurityGroup",
        "ec2:ModifyInstanceAttribute",
        "ec2:AttachVolume",
        "ec2:DetachVolume",
        "ec2:RevokeSecurityGroupEgress"
      ],
      "Resource": "*"
    }
  ]
}
```

### IAM Policy Example

Create an IAM policy with the above permissions and attach it to:
- The EC2 instance role (if using instance profiles)
- The user/role running the tests (if using AWS credentials)
- The service account (if using IAM roles for service accounts in EKS)

## Running E2E Tests

### Using the Makefile (Recommended)

```bash
# Run all e2e tests with automatic setup
make test-e2e KUBECONFIG=/path/to/kubeconfig

# Run with specific AWS region (optional)
AWS_REGION=us-west-2 make test-e2e KUBECONFIG=/path/to/kubeconfig
```

### Manual Execution

```bash
# Set environment variables
export KUBECONFIG=/path/to/your/kubeconfig
export AWS_REGION=us-west-2  # Optional - will be auto-detected

# Run tests directly
cd test/e2e
ginkgo -v .
```

## Test Validation Process

### 1. Cluster Validation
The tests automatically validate that:
- The cluster is AWS-based (checks node provider IDs)
- At least 50% of nodes have AWS provider IDs
- Required number of nodes are available for safe testing

### 2. AWS Region Detection
The tests automatically detect the AWS region using:
1. `AWS_REGION` environment variable
2. Node names (e.g., `ip-10-0-1-1.us-west-2.compute.internal`)
3. Node provider IDs (e.g., `aws:///us-west-2a/i-1234567890abcdef0`)

### 3. Permission Validation
Before running disruption tests, the system validates all required AWS permissions by:
- Testing each permission with invalid parameters
- Distinguishing between authorization errors and validation errors
- Failing fast if permissions are insufficient

## Test Scenarios

### Network Disruption Test

**What it does:**
1. Creates a temporary AWS Security Group with no outbound rules
2. Attaches the security group to a target worker node
3. Verifies the node becomes `NotReady` due to network isolation
4. Confirms SBD remediation is triggered
5. Removes the security group and verifies node recovery

**Safety measures:**
- Only affects one worker node at a time
- Preserves existing security groups
- Automatic cleanup even if test fails
- Skips control plane nodes

### Storage Disruption Test

**What it does:**
1. Identifies non-root EBS volumes attached to a target worker node
2. Detaches the volumes to simulate storage failure
3. Verifies the node experiences storage issues
4. Confirms SBD remediation for storage failures
5. Reattaches volumes and monitors recovery

**Safety measures:**
- Never detaches root volumes (avoids complete system failure)
- Only detaches additional EBS volumes
- Graceful detachment attempted first, forced as fallback
- Automatic volume reattachment with proper device mapping

## Test Skipping and Failures

### Automatic Skipping
Tests are automatically skipped when:
- Individual AWS-based tests skip when cluster is not AWS-based or AWS initialization fails
- Insufficient nodes for safe testing
- AWS region cannot be determined
- Required AWS permissions are missing

**Note**: The test suite will run non-AWS tests (like basic configuration and agent crash tests) even when AWS is not available. Only the network and storage disruption tests require AWS.

### Expected Failures
Some test scenarios are designed to trigger failures:
- Node `NotReady` conditions (intentional)
- SBD remediation triggers (expected behavior)
- Temporary resource unavailability (part of test)

## Troubleshooting

### Common Issues

#### 1. "Cluster is not AWS-based" 
```
AWS not available for disruption tests: cluster is not AWS-based, skipping AWS disruption tests
```
**Solution:** This is informational. Non-AWS tests will still run. For AWS disruption tests, ensure you're running on an AWS-based Kubernetes cluster with proper provider IDs.

#### 2. "Failed to detect AWS region"
```
Error: failed to detect AWS region: could not auto-detect AWS region from cluster configuration
```
**Solution:** Set the `AWS_REGION` environment variable or ensure node names contain region information.

#### 3. "AWS permission validation failed"
```
Error: AWS permission validation failed: missing required AWS permissions: ec2:CreateSecurityGroup, ec2:DetachVolume
```
**Solution:** Ensure the IAM role/user has all required permissions listed above.

#### 4. "No suitable non-root volumes found to detach"
```
Skipping storage disruption test: no suitable non-root volumes found to detach
```
**Solution:** This is expected if nodes only have root volumes. The test will skip storage disruption scenarios.

#### 5. Security Group Cleanup Failures
```
Warning: failed to clean up network disruption: failed to delete security group: DependencyViolation
```
**Solution:** The test includes retry logic for this. If manual cleanup is needed:
```bash
# Find and delete the security group manually
aws ec2 describe-security-groups --filters "Name=group-name,Values=sbd-e2e-network-disruptor-*"
aws ec2 delete-security-group --group-id sg-xxxxxxxxx
```

### Debugging

#### 1. Enable Verbose Logging
```bash
ginkgo -v --trace test/e2e
```

#### 2. Check AWS Credentials
```bash
aws sts get-caller-identity
aws ec2 describe-instances --max-items 1
```

#### 3. Verify Node Provider IDs
```bash
kubectl get nodes -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.providerID}{"\n"}{end}'
```

#### 4. Monitor AWS Resources During Tests
```bash
# Monitor security groups
aws ec2 describe-security-groups --filters "Name=group-name,Values=sbd-e2e-*"

# Monitor volumes
aws ec2 describe-volumes --filters "Name=state,Values=available,in-use"
```

## Safety Considerations

### Production Clusters
**⚠️ WARNING:** These tests perform real infrastructure disruptions. While designed to be safe, they should be used with caution on production clusters.

**Recommendations:**
- Test on dedicated test clusters when possible
- Ensure adequate node redundancy (minimum 3 worker nodes)
- Run during maintenance windows
- Have monitoring in place to detect issues

### Resource Cleanup
The tests include comprehensive cleanup logic:
- `defer` statements ensure cleanup even on test failures
- Automatic restoration of original configurations
- Retry logic for AWS resource cleanup
- Graceful handling of partial failures

### Test Isolation
- Each test uses unique resource names with timestamps
- Tests clean up previous runs before starting
- Temporary AWS resources are clearly tagged
- No persistent changes to cluster configuration

## Environment Variables

- `AWS_REGION`: Override AWS region detection (optional)
- `KUBECONFIG`: Path to Kubernetes configuration file
- `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`: AWS credentials (if not using IAM roles)
- `AWS_PROFILE`: AWS profile to use (alternative to access keys)

## Contributing

When adding new disruption tests:

1. **Follow the safety patterns:**
   - Always use `defer` for cleanup
   - Test with invalid parameters to check permissions
   - Include comprehensive error handling

2. **Add appropriate validation:**
   - Check cluster compatibility
   - Validate required permissions
   - Skip gracefully when prerequisites aren't met

3. **Document the test:**
   - Explain what infrastructure changes are made
   - Document safety measures
   - Include troubleshooting guidance

## Related Documentation

- [Smoke Tests](../smoke/README.md) - Basic functionality validation
- [SBD Protocol Documentation](../../docs/) - Understanding SBD behavior
- [AWS IAM Documentation](https://docs.aws.amazon.com/IAM/) - Managing AWS permissions 