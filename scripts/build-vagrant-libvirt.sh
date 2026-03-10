#!/bin/bash
set -ex

# Detect OS version
OS_VER=$(rpm -E %{rhel})
echo "Building vagrant-libvirt on AlmaLinux ${OS_VER}"

# Install build dependencies
dnf install -y rpm-build ruby-devel rubygems-devel libvirt-devel \
  libxml2-devel libxslt-devel gcc make \
  vagrant rubygem-bundler rubygem-rake rubygem-rspec rubygem-thor \
  rubygem-rexml rubygem-xml-simple || true

# Install remaining gems that may not be packaged
gem install diffy fog-libvirt --no-document 2>/dev/null || true

# Setup rpmbuild tree
mkdir -p ~/rpmbuild/{SOURCES,SPECS}

# Copy sources and spec (provisioned to /tmp/vagrant-libvirt-src/)
cp /tmp/vagrant-libvirt-src/*.patch ~/rpmbuild/SOURCES/
cp /tmp/vagrant-libvirt-src/vagrant-libvirt.spec ~/rpmbuild/SPECS/

# Download gem source
cd ~/rpmbuild/SOURCES
SPEC=/tmp/vagrant-libvirt-src/vagrant-libvirt.spec
GEM_VER=$(grep '^Version:' "$SPEC" | awk '{print $2}')
SPEC_COMMIT=$(grep 'vagrant_spec_commit' "$SPEC" | head -1 | awk '{print $NF}')

if [ ! -f "vagrant-libvirt-${GEM_VER}.gem" ]; then
  curl -LO "https://rubygems.org/gems/vagrant-libvirt-${GEM_VER}.gem"
fi
if [ ! -f "vagrant-spec-${SPEC_COMMIT}.tar.gz" ]; then
  curl -LO "https://github.com/mitchellh/vagrant-spec/archive/${SPEC_COMMIT}/vagrant-spec-${SPEC_COMMIT}.tar.gz"
fi

# Build RPM (skip dep checks and tests)
cd ~/rpmbuild
rpmbuild -ba --nocheck --nodeps SPECS/vagrant-libvirt.spec 2>&1

echo "=== Build complete ==="
ls -la ~/rpmbuild/RPMS/noarch/ 2>/dev/null || ls -la ~/rpmbuild/RPMS/*/
