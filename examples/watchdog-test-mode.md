# Watchdog Test Mode Example

This example demonstrates how to use the watchdog test mode functionality in the SBD operator.

## What is Test Mode?

Test mode enables the `soft_noboot=1` parameter when loading the Linux `softdog` kernel module. This prevents the system from actually rebooting when the watchdog timeout occurs, making it safe for development and testing.

## Command Line Usage

### Normal Mode (Default)
```bash
# Normal operation - will cause system reboot on watchdog timeout
./sbd-agent --watchdog-path /dev/watchdog --node-name test-node
```

### Test Mode
```bash
# Test mode - watchdog timeouts won't cause system reboot
./sbd-agent --watchdog-path /dev/watchdog --node-name test-node --watchdog-test-mode
```

## Programmatic Usage

### Using the Watchdog Package Directly

```go
package main

import (
    "time"
    "github.com/medik8s/sbd-operator/pkg/watchdog"
    "github.com/go-logr/logr"
)

func main() {
    logger := logr.Discard()
    
    // Enable test mode to prevent system reboots
    wd, err := watchdog.NewWithSoftdogFallbackAndTestMode("/dev/watchdog", true, logger)
    if err != nil {
        panic(err)
    }
    defer wd.Close()
    
    if wd.IsSoftdog() {
        logger.Info("Using softdog in test mode - no reboots will occur")
    }
    
    // Pet the watchdog a few times
    for i := 0; i < 3; i++ {
        if err := wd.Pet(); err != nil {
            logger.Error(err, "Failed to pet watchdog")
            break
        }
        time.Sleep(5 * time.Second)
    }
    
    // Stop petting - in normal mode this would cause a reboot
    // In test mode, the system will continue running
    logger.Info("Stopped petting watchdog - in test mode, system won't reboot")
    time.Sleep(30 * time.Second)
    logger.Info("System is still running!")
}
```

## Module Loading Behavior

### Normal Mode
When test mode is disabled, the softdog module is loaded with:
```bash
modprobe softdog soft_margin=60
```

### Test Mode
When test mode is enabled, the softdog module is loaded with:
```bash
modprobe softdog soft_margin=60 soft_noboot=1
```

## Use Cases

### Development
- Testing watchdog logic without system resets
- Debugging watchdog-related issues
- Developing new watchdog features

### CI/CD Pipelines
- Running integration tests that involve watchdog functionality
- Testing SBD agent behavior without infrastructure impact
- Validating watchdog configuration changes

### Debugging Production Issues
- Reproducing watchdog-related problems in a safe environment
- Testing fixes before deploying to production
- Understanding watchdog timing behavior

## Important Notes

1. **Hardware Watchdogs**: Test mode only affects the `softdog` kernel module. Hardware watchdogs will still cause system resets regardless of the test mode setting.

2. **Privileges Required**: Loading the softdog module requires appropriate privileges (typically root or `SYS_MODULE` capability).

3. **Container Environments**: When running in containers, ensure the container has the necessary capabilities and volume mounts:
   ```yaml
   securityContext:
     privileged: true
     capabilities:
       add:
       - SYS_MODULE
   volumeMounts:
   - name: dev
     mountPath: /dev
   - name: modules
     mountPath: /lib/modules
     readOnly: true
   ```

4. **Logging**: Enable debug logging to see detailed information about module loading:
   ```bash
   ./sbd-agent --watchdog-test-mode --log-level debug
   ```

## Verification

To verify that test mode is working correctly:

1. **Check Module Parameters**:
   ```bash
   # After starting sbd-agent with --watchdog-test-mode
   cat /sys/module/softdog/parameters/soft_noboot
   # Should output: 1
   ```

2. **Check Logs**: Look for log messages indicating test mode:
   ```
   INFO Successfully loaded softdog module in test mode (soft_noboot=1)
   INFO Successfully loaded and opened softdog watchdog device testMode=true
   ```

3. **Test Timeout**: Stop the sbd-agent process and verify the system doesn't reboot after the watchdog timeout period.

## Troubleshooting

### Common Issues

1. **Permission Denied**: Ensure sufficient privileges to load kernel modules
2. **Module Not Found**: Verify `softdog` module is available in the kernel
3. **Hardware Watchdog Present**: Test mode won't affect hardware watchdogs

### Debug Commands

```bash
# Check if softdog module is loaded
lsmod | grep softdog

# Check softdog parameters
cat /sys/module/softdog/parameters/soft_margin
cat /sys/module/softdog/parameters/soft_noboot

# Check watchdog devices
ls -la /dev/watchdog*

# Test module loading manually
modprobe softdog soft_margin=60 soft_noboot=1
``` 