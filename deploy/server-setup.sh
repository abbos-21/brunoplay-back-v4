#!/usr/bin/env bash
# One-time base setup of a FRESH Ubuntu 22.04 / 24.04 server for BrunoPlay. Run it as root, once:
#
#   apt-get update && apt-get install -y git
#   git clone https://github.com/abbos-21/brunoplay-back-v4.git /root/brunoplay-back-v4
#   bash /root/brunoplay-back-v4/deploy/server-setup.sh YOUR_NEW_USER_NAME
#
# It installs Docker, nginx, certbot, git, restic and fail2ban, creates the user you will work as, adds swap, opens only the
# ports 22, 80 and 443 and turns on automatic security updates. It does NOT start the game: that is the next step of the guide.
# Safe to run again (every step checks what is already there).
set -euo pipefail

USER_NAME="${1:?usage: server-setup.sh NEW_USER_NAME   (the name of the user you will log in as, for example abbos)}"
[ "$(id -u)" = 0 ] || { echo "run this as root"; exit 1; }
. /etc/os-release
case "$ID:$VERSION_ID" in
  ubuntu:22.04|ubuntu:24.04) ;;
  *) echo "this script is written for Ubuntu 22.04 or 24.04, this is $PRETTY_NAME"; exit 1 ;;
esac
export DEBIAN_FRONTEND=noninteractive
step() { echo; echo "== $*"; }

step "updating the system"
apt-get update -y
apt-get upgrade -y

step "installing the basics"
apt-get install -y ca-certificates curl gnupg git ufw nginx certbot python3-certbot-nginx openssl util-linux restic fail2ban \
  unattended-upgrades cron htop

step "installing Docker (from Docker's own repository)"
if ! command -v docker >/dev/null 2>&1; then
  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
  chmod a+r /etc/apt/keyrings/docker.asc
  echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu $VERSION_CODENAME stable" \
    > /etc/apt/sources.list.d/docker.list
  apt-get update -y
  apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
fi
systemctl enable --now docker

step "creating the user $USER_NAME"
if ! id "$USER_NAME" >/dev/null 2>&1; then
  adduser --disabled-password --gecos "" "$USER_NAME"
fi
usermod -aG sudo,docker "$USER_NAME"
# the same SSH key that lets you in as root lets you in as the new user
if [ -s /root/.ssh/authorized_keys ] && [ ! -s "/home/$USER_NAME/.ssh/authorized_keys" ]; then
  install -d -m 700 -o "$USER_NAME" -g "$USER_NAME" "/home/$USER_NAME/.ssh"
  install -m 600 -o "$USER_NAME" -g "$USER_NAME" /root/.ssh/authorized_keys "/home/$USER_NAME/.ssh/authorized_keys"
fi

step "swap (a safety net for memory peaks)"
if [ "$(swapon --show --noheadings | wc -l)" = 0 ]; then
  mem_mb="$(awk '/MemTotal/ {print int($2/1024)}' /proc/meminfo)"
  size="2G"; [ "$mem_mb" -ge 6000 ] && size="1G"
  fallocate -l "$size" /swapfile
  chmod 600 /swapfile
  mkswap /swapfile >/dev/null
  swapon /swapfile
  grep -q '^/swapfile ' /etc/fstab || echo '/swapfile none swap sw 0 0' >> /etc/fstab
  echo "created a $size swap file"
else
  echo "swap already there"
fi
echo 'vm.swappiness=10' > /etc/sysctl.d/99-brunoplay.conf
sysctl -p /etc/sysctl.d/99-brunoplay.conf >/dev/null

step "firewall: only SSH, HTTP and HTTPS are open"
ssh_port="$(sshd -T 2>/dev/null | awk '/^port / {print $2; exit}')"
ssh_port="${ssh_port:-22}"
ufw default deny incoming >/dev/null
ufw default allow outgoing >/dev/null
ufw allow "$ssh_port"/tcp >/dev/null
ufw allow 80/tcp >/dev/null
ufw allow 443/tcp >/dev/null
ufw --force enable >/dev/null
ufw status | sed 's/^/   /'

step "automatic security updates, brute-force protection, time"
systemctl enable --now unattended-upgrades >/dev/null 2>&1 || true
systemctl enable --now fail2ban >/dev/null 2>&1 || true
timedatectl set-timezone UTC || true
timedatectl set-ntp true || true
systemctl enable --now nginx

step "folders for the web files"
install -d -m 755 -o "$USER_NAME" -g "$USER_NAME" /opt/brunoplay /opt/brunoplay/www

step "can this server reach what the game needs?"
check() { # name url
  if curl -s -m 10 -o /dev/null -w '%{http_code}' "$2" | grep -qE '^(200|301|302|401|403|404|405)$'; then
    echo "   ok   $1"
  else
    echo "   FAIL $1 ($2): choose another provider or location before going on"
  fi
}
check "Telegram API" "https://api.telegram.org"
check "toncenter (TON network)" "https://toncenter.com/api/v2/getAddressBalance?address=UQAMJQn8mvCIdRF6jo133-c4fFHB3-V-JVakETamDO2iCy7W"
check "Adsgram" "https://sad.adsgram.ai/js/sad.min.js"
check "GitHub" "https://github.com"
check "DuckDNS" "https://www.duckdns.org"

echo
echo "Base setup done. Next:"
echo "  1. set a password for $USER_NAME (needed for sudo):   passwd $USER_NAME"
echo "  2. open a NEW terminal and log in as $USER_NAME:      ssh $USER_NAME@THIS_SERVER_IP"
echo "  3. check that it works there:                         docker ps && sudo nginx -t"
echo "Do not close this root session until step 3 works."
