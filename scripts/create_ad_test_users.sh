#!/bin/bash
set -euo pipefail

# Creates test AD users and a "developers" group inside the Samba DC container
# Usage:
#   chmod +x scripts/create_ad_test_users.sh
#   ./scripts/create_ad_test_users.sh
# Optionally set SAMBA_CONTAINER to the container name if autodetection fails.

CONTAINER=${SAMBA_CONTAINER:-$(docker ps --filter "ancestor=instantlinux/samba-dc" --format '{{.Names}}' | head -n1)}

if [ -z "${CONTAINER}" ]; then
  echo "Error: Samba DC container not found."
  echo "Set SAMBA_CONTAINER to the container name or ensure the container is running."
  exit 1
fi

echo "Using Samba container: ${CONTAINER}"

run_in_container() {
  docker exec -i "${CONTAINER}" sh -c "$1"
}

create_user() {
  USER=$1
  PASS=$2
  echo "Creating user ${USER}..."
  if run_in_container "samba-tool user show ${USER} >/dev/null 2>&1"; then
    echo "  ${USER} already exists"
  else
    run_in_container "samba-tool user create ${USER} ${PASS} --given-name=${USER} --surname=Test"
    echo "  ${USER} created"
  fi
}

create_group() {
  GROUP=$1
  echo "Ensuring group ${GROUP} exists..."
  if run_in_container "samba-tool group show ${GROUP} >/dev/null 2>&1"; then
    echo "  ${GROUP} already exists"
  else
    run_in_container "samba-tool group add ${GROUP}"
    echo "  ${GROUP} created"
  fi
}

add_member() {
  GROUP=$1
  USER=$2
  echo "Adding ${USER} to ${GROUP}..."
  # attempt to add; ignore errors if already member
  run_in_container "samba-tool group addmembers ${GROUP} ${USER} || true"
}

echo "Creating test users: ellie, freddy, helen (password: Password123!)"
create_user ellie Password123!
create_user freddy Password123!
create_user helen Password123!

echo "Creating group: developers and adding members"
create_group developers
add_member developers ellie
add_member developers freddy

echo "Done. Verify with: docker exec -it ${CONTAINER} sh -c 'samba-tool user list; samba-tool group listmembers developers'"
