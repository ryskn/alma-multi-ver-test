---
name: rpm-test
description: Generate a YAML job file for building and testing an RPM package across AlmaLinux 8/9/10
argument-hint: "<spec-repo-path> [branch-prefix]"
---

Generate a YAML job file for alma-ctl that builds and tests an RPM package across AlmaLinux 8, 9, and 10.

## Arguments

- `$0` — Path to the source repository (relative to the alma project root, e.g. `../vagrant-libvirt/`)
- `$1` — (optional) Branch prefix. Defaults to `el`. Branches will be `{prefix}8`, `{prefix}9`, `{prefix}10`

## Instructions

1. First, inspect the source repository to understand the package:
   - Read the `.spec` file to get the package name, version, build dependencies, and source files
   - Check available git branches with `git branch -a`
   - Look for patches, source tarballs, or gem files referenced in the spec

2. Generate a YAML job file at `jobs/build-<package-name>.yaml` with the following structure:

```yaml
name: build-<package-name>

vars:
  alma8:
    branch: <prefix>8
  alma9:
    branch: <prefix>9
  alma10:
    branch: <prefix>10

steps:
  - name: install build deps
    run: |
      # Install BuildRequires from the spec file
      dnf install -y <packages> || true

  - name: upload sources
    upload:
      src: <repo-path>/
      dest: /tmp/<package-name>-src/
      git_archive: "{{branch}}"

  - name: prepare rpmbuild tree
    run: |
      mkdir -p ~/rpmbuild/{SOURCES,SPECS}
      cp /tmp/<package-name>-src/src/*.patch ~/rpmbuild/SOURCES/ 2>/dev/null || true
      cp /tmp/<package-name>-src/src/<package>.spec ~/rpmbuild/SPECS/

  - name: download sources
    run: |
      # Download Source0, Source1, etc. from the spec
      cd ~/rpmbuild/SOURCES
      # curl commands for each source URL

  - name: build RPM
    run: |
      cd ~/rpmbuild
      rpmbuild -ba --nocheck --nodeps SPECS/<package>.spec 2>&1
      echo "=== Build complete ==="
      ls -la ~/rpmbuild/RPMS/*/ 2>/dev/null

  - name: install RPM
    run: |
      RPM=$(find ~/rpmbuild/RPMS -name '<package-name>-*.rpm' ! -name '*doc*' ! -name '*debug*' 2>/dev/null | head -1)
      if [ -z "$RPM" ]; then
        echo "ERROR: RPM not found"
        exit 1
      fi
      echo "Installing: $RPM"
      rpm -Uvh --nodeps --force "$RPM"
      rpm -qi <package-name>

  - name: test
    run: |
      # Package-specific functional test
      # e.g. for libraries: try to load them
      # e.g. for services: start and verify
      # e.g. for vagrant-libvirt: create a VM with virsh
```

3. Adapt the test step based on the package type:
   - **Library/gem**: `ruby -e "require '<name>'"` or `python -c "import <name>"`
   - **Service/daemon**: Start the service, check status, verify functionality
   - **CLI tool**: Run the binary with `--version` or a basic command
   - **Vagrant plugin (libvirt)**: Install libvirt, create/start/destroy a test VM with virsh
   - **System library**: Check `ldconfig -p | grep <lib>`, or run a linking test

4. Important rules:
   - Use `|| true` for dnf install steps where some packages may not exist on all versions
   - Use `--nodeps --nocheck` for rpmbuild to avoid missing dependency issues
   - Use `rpm -Uvh --nodeps --force` for installation
   - Quote all YAML special characters (avoid bare `:` in flow contexts)
   - Don't use heredocs with `<<` in YAML `run: |` blocks — use `printf '%s\n'` instead
   - Per-target vars use `{{varname}}` template syntax

5. After generating the file, show the user how to run it:
   ```
   alma-ctl run jobs/build-<package-name>.yaml
   ```
