#!/usr/bin/env bash

echo "=============================================================================="
echo "Freeing up disk space on CI system"
echo "=============================================================================="

set -u
export DEBIAN_FRONTEND=noninteractive

safe_apt_remove() {
  local pkg="$1"
  sudo apt-get remove -y "$pkg" || true
}

echo "Listing 100 largest packages"
dpkg-query -Wf '${Installed-Size}\t${Package}\n' | sort -n | tail -n 100
df -h
echo "Removing large packages"
safe_apt_remove '^ghc-8.*'
safe_apt_remove '^dotnet-.*'
safe_apt_remove '^llvm-.*'
safe_apt_remove 'php.*'
safe_apt_remove 'azure-cli'
safe_apt_remove 'google-cloud-sdk'
safe_apt_remove 'hhvm'
safe_apt_remove 'google-chrome-stable'
safe_apt_remove 'firefox'
safe_apt_remove 'powershell'
safe_apt_remove 'monodoc-http'
safe_apt_remove 'mono-devel'
sudo apt-get autoremove -y
sudo apt-get clean
df -h
echo "Removing large directories"
sudo rm -rf /opt/hostedtoolcache || true
sudo rm -rf /usr/share/dotnet/ || true
sudo rm -rf /usr/local/lib/android || true
sudo rm -rf /opt/ghc || true
sudo rm -rf /usr/local/.ghcup || true
sudo docker system prune -af --volumes || true
df -h
