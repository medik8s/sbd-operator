# SBD Operator Debugging Scripts

This directory contains useful scripts for debugging and troubleshooting the SBD Operator and its agents.

## Scripts Overview

### 🔍 `list-agent-pods.sh` - Agent Pod Overview
Lists all SBD agent pods across OpenShift nodes with their status.

**Use this when you want to:**
- Get an overview of SBD agent deployment across the cluster
- Check which nodes have agents running
- See pod status at a glance
- Identify problematic pods before digging into logs

**Example output:**
```bash
$ ./list-agent-pods.sh
[2025-01-24 10:30:15] SBD Agent Pod Listing Tool
[2025-01-24 10:30:15] Auto-detected SBD namespace: sbd-system
[2025-01-24 10:30:15] SBD Agent Pods in namespace: sbd-system
==============================================

POD NAME                                 NODE NAME                      STATUS         
---------------------------------------- ------------------------------ ---------------
sbd-agent-test-config-abc123            worker-1.example.com           Running        
sbd-agent-test-config-def456            worker-2.example.com           Running        
sbd-agent-test-config-ghi789            master-1.example.com           Running        

[2025-01-24 10:30:15] Summary: 3 total pods, 3 running, 0 pending, 0 failed
```

### 📋 `get-agent-logs.sh` - Node-Specific Logs
Retrieves logs from the SBD agent pod running on a specific OpenShift node.

**Use this when you want to:**
- Debug issues on a specific node
- Follow real-time logs from an agent
- Get previous logs after a pod restart
- Troubleshoot node-specific SBD problems

**Example output:**
```bash
$ ./get-agent-logs.sh worker-1.example.com --tail 50
[2025-01-24 10:31:22] SBD Agent Log Retrieval Tool
[2025-01-24 10:31:22] Node: worker-1.example.com
[2025-01-24 10:31:22] Auto-detected SBD namespace: sbd-system
[2025-01-24 10:31:22] Found SBD agent pod: sbd-agent-test-config-abc123 (status: Running)
[2025-01-24 10:31:22] Retrieving logs from SBD agent pod 'sbd-agent-test-config-abc123'...
==============================================
2025-01-24T10:31:20.123456Z INFO    Starting SBD Agent
2025-01-24T10:31:20.234567Z INFO    Successfully opened SBD device
2025-01-24T10:31:20.345678Z INFO    Watchdog pet successful
...
```

## Quick Start

### 1. Get an overview of all agent pods
```bash
./scripts/list-agent-pods.sh
```

### 2. Look at logs from a specific node
```bash
./scripts/get-agent-logs.sh <node-name>
```

### 3. Follow logs in real-time
```bash
./scripts/get-agent-logs.sh <node-name> --follow
```

## Common Usage Patterns

### Basic Troubleshooting Workflow
1. **Check overall status:** `./list-agent-pods.sh`
2. **Identify problem nodes:** Look for non-Running status
3. **Get detailed logs:** `./get-agent-logs.sh <problematic-node>`
4. **Follow real-time:** `./get-agent-logs.sh <node> --follow --tail 100`

### Debugging Agent Startup Issues
```bash
# Check if agents are starting on all nodes
./list-agent-pods.sh --details

# Get startup logs from a specific node
./get-agent-logs.sh worker-1 --tail 200

# Follow logs during agent restart
oc delete pod sbd-agent-xxx -n sbd-system
./get-agent-logs.sh worker-1 --follow
```

### Investigating Pod Restarts
```bash
# Check restart counts
./list-agent-pods.sh --details

# Get logs from previous container instance
./get-agent-logs.sh worker-1 --previous

# Compare with current logs
./get-agent-logs.sh worker-1 --tail 50
```

### Cluster-Wide Analysis
```bash
# Get all pods in JSON format for analysis
./list-agent-pods.sh --output json > agent-pods.json

# Check specific namespace
./list-agent-pods.sh -n my-sbd-namespace
```

## Script Features

### Auto-Detection
Both scripts automatically detect:
- ✅ **Namespace:** Finds SBD agents in common namespaces
- ✅ **Command:** Uses `oc` if available, falls back to `kubectl`
- ✅ **Cluster:** Validates connection before proceeding

### Environment Variables
Set these for easier usage:
- `SBD_NAMESPACE`: Default namespace for SBD agents
- `KUBECONFIG`: Path to kubeconfig file

### Error Handling
Scripts provide clear error messages for:
- Missing dependencies (`oc`/`kubectl`)
- No cluster connectivity
- Node not found
- No agent pods found
- Permission issues

## Dependencies

Both scripts require:
- ✅ OpenShift CLI (`oc`) or Kubernetes CLI (`kubectl`)
- ✅ Access to OpenShift/Kubernetes cluster
- ✅ Read permissions for pods and logs in SBD namespace

## Troubleshooting the Scripts

### "No SBD agent pods found"
- Check if SBD operator is deployed: `oc get pods -A | grep sbd`
- Verify namespace: `oc get pods -n <sbd-namespace>`
- Check labels: `oc get pods -l app=sbd-agent -A`

### "Node not found"
- List available nodes: `oc get nodes`
- Use exact node name (case-sensitive)
- Check node labels: `oc get node <node-name> --show-labels`

### "Cannot connect to cluster"
- Check kubeconfig: `oc cluster-info`
- Verify login: `oc whoami`
- Test connectivity: `oc get nodes`

## Advanced Usage

### Custom Output Formats
```bash
# Get pod details in wide format
./list-agent-pods.sh --output wide --details

# Export agent status as YAML
./list-agent-pods.sh --output yaml > agent-status.yaml
```

### Log Analysis
```bash
# Get last hour of logs
./get-agent-logs.sh worker-1 --since 1h

# Save logs to file
./get-agent-logs.sh worker-1 > agent-logs.txt

# Monitor logs without timestamps (cleaner output)
./get-agent-logs.sh worker-1 --follow --no-timestamps
```

### Multiple Nodes
```bash
# Get logs from all nodes (example)
for node in $(oc get nodes --no-headers -o custom-columns=NAME:.metadata.name); do
    echo "=== Logs from $node ==="
    ./get-agent-logs.sh "$node" --tail 10
done
```

## Contributing

When adding new debugging scripts:
1. Follow the same patterns for argument parsing and error handling
2. Add comprehensive help messages with examples
3. Support both `oc` and `kubectl` commands
4. Include namespace auto-detection
5. Add documentation to this README

## See Also

- [SBD Operator Documentation](../docs/)
- [OpenShift CLI Reference](https://docs.openshift.com/container-platform/latest/cli_reference/openshift_cli/getting-started-cli.html)
- [Kubernetes Debugging Guide](https://kubernetes.io/docs/tasks/debug-application-cluster/) 