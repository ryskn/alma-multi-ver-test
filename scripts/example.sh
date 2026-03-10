#!/bin/bash
set -e

echo "=== OS Info ==="
cat /etc/os-release | grep -E '^(NAME|VERSION)='

echo "=== Kernel ==="
uname -r

echo "=== SELinux ==="
getenforce 2>/dev/null || echo "not available"

echo "=== Network ==="
ip -br addr show

echo "=== Disk ==="
df -h /
