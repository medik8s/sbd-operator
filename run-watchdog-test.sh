#!/bin/bash

# Simple ARM64 vs AMD64 watchdog test that actually works
set -eo pipefail

AWS_REGION="ap-southeast-2"
KEY_NAME="watchdog-test-key"
KEY_FILE="/tmp/watchdog-test-key.pem"
export AWS_PAGER=""

log() {
    echo "[$(date +'%H:%M:%S')] $*"
}

main() {
    log "=== ARM64 vs AMD64 Watchdog Test ==="
    log "This test will:"
    log "1. Create two EC2 instances (AMD64 and ARM64)"
    log "2. Deploy the debug tool to both"
    log "3. Run watchdog tests"
    log "4. Compare WDIOC_KEEPALIVE behavior"
    
    # Get VPC
    log "Getting default VPC..."
    vpc_id=$(aws ec2 describe-vpcs \
        --region "${AWS_REGION}" \
        --filters "Name=is-default,Values=true" \
        --query 'Vpcs[0].VpcId' \
        --output text)
    log "Using VPC: $vpc_id"
    
    # Create security group
    log "Creating security group..."
    sg_id=$(aws ec2 create-security-group \
        --region "${AWS_REGION}" \
        --group-name "watchdog-test-$(date +%s)" \
        --description "Watchdog test security group" \
        --vpc-id "$vpc_id" \
        --query 'GroupId' \
        --output text)
    log "Created security group: $sg_id"
    
    # Add SSH rule
    aws ec2 authorize-security-group-ingress \
        --region "${AWS_REGION}" \
        --group-id "$sg_id" \
        --protocol tcp \
        --port 22 \
        --cidr "0.0.0.0/0" >/dev/null
    
    # Create AMD64 instance
    log "Creating AMD64 instance..."
    amd64_id=$(aws ec2 run-instances \
        --region "${AWS_REGION}" \
        --image-id "ami-0e6874cbf738602e7" \
        --instance-type "t3.micro" \
        --key-name "${KEY_NAME}" \
        --security-group-ids "$sg_id" \
        --user-data "#!/bin/bash
yum update -y
yum install -y golang
modprobe softdog || true
mkdir -p /home/ec2-user/debug
chown ec2-user:ec2-user /home/ec2-user/debug" \
        --tag-specifications "ResourceType=instance,Tags=[{Key=Name,Value=watchdog-test-amd64}]" \
        --query 'Instances[0].InstanceId' \
        --output text)
    log "AMD64 instance: $amd64_id"
    
    # Create ARM64 instance  
    log "Creating ARM64 instance..."
    arm64_id=$(aws ec2 run-instances \
        --region "${AWS_REGION}" \
        --image-id "ami-06494f9d4da11dbad" \
        --instance-type "t4g.micro" \
        --key-name "${KEY_NAME}" \
        --security-group-ids "$sg_id" \
        --user-data "#!/bin/bash
yum update -y  
yum install -y golang
modprobe softdog || true
mkdir -p /home/ec2-user/debug
chown ec2-user:ec2-user /home/ec2-user/debug" \
        --tag-specifications "ResourceType=instance,Tags=[{Key=Name,Value=watchdog-test-arm64}]" \
        --query 'Instances[0].InstanceId' \
        --output text)
    log "ARM64 instance: $arm64_id"
    
    # Wait for instances
    log "Waiting for instances to be running..."
    aws ec2 wait instance-running --region "${AWS_REGION}" --instance-ids "$amd64_id" "$arm64_id"
    
    # Get IPs
    amd64_ip=$(aws ec2 describe-instances \
        --region "${AWS_REGION}" \
        --instance-ids "$amd64_id" \
        --query 'Reservations[0].Instances[0].PublicIpAddress' \
        --output text)
    
    arm64_ip=$(aws ec2 describe-instances \
        --region "${AWS_REGION}" \
        --instance-ids "$arm64_id" \
        --query 'Reservations[0].Instances[0].PublicIpAddress' \
        --output text)
    
    log "AMD64 IP: $amd64_ip"
    log "ARM64 IP: $arm64_ip"
    
    # Now let's actually run the tests automatically
    log "Waiting for SSH to be ready..."
    sleep 90
    
    # Test AMD64
    log "=== Testing AMD64 Instance ==="
    scp -i "$KEY_FILE" -o StrictHostKeyChecking=no debug-watchdog-arm64.go ec2-user@"$amd64_ip":debug/
    ssh -i "$KEY_FILE" -o StrictHostKeyChecking=no ec2-user@"$amd64_ip" 'cd debug && go build debug-watchdog-arm64.go'
    log "Running watchdog test on AMD64..."
    ssh -i "$KEY_FILE" -o StrictHostKeyChecking=no ec2-user@"$amd64_ip" 'cd debug && sudo ./debug-watchdog-arm64' | tee /tmp/amd64-results.txt
    
    # Test ARM64
    log "=== Testing ARM64 Instance ==="
    scp -i "$KEY_FILE" -o StrictHostKeyChecking=no debug-watchdog-arm64.go ec2-user@"$arm64_ip":debug/
    ssh -i "$KEY_FILE" -o StrictHostKeyChecking=no ec2-user@"$arm64_ip" 'cd debug && go build debug-watchdog-arm64.go'
    log "Running watchdog test on ARM64..."
    ssh -i "$KEY_FILE" -o StrictHostKeyChecking=no ec2-user@"$arm64_ip" 'cd debug && sudo ./debug-watchdog-arm64' | tee /tmp/arm64-results.txt
    
    log "=== COMPARISON RESULTS ==="
    log "AMD64 results in /tmp/amd64-results.txt"
    log "ARM64 results in /tmp/arm64-results.txt"
    
    # Look for ENOTTY specifically
    if grep -q "ENOTTY" /tmp/arm64-results.txt; then
        log "✅ CONFIRMED: ARM64 shows ENOTTY error for WDIOC_KEEPALIVE"
    else
        log "❌ ARM64 did not show expected ENOTTY error"
    fi
    
    if grep -q "ENOTTY" /tmp/amd64-results.txt; then
        log "❌ UNEXPECTED: AMD64 also shows ENOTTY error"
    else
        log "✅ CONFIRMED: AMD64 does not show ENOTTY error"
    fi
    
    log "Clean up with:"
    log "aws ec2 terminate-instances --region $AWS_REGION --instance-ids $amd64_id $arm64_id"
    log "aws ec2 delete-security-group --region $AWS_REGION --group-id $sg_id"
    log "aws ec2 delete-key-pair --region $AWS_REGION --key-name $KEY_NAME"
}

main "$@" 