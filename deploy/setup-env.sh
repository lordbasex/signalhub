#!/bin/sh
# Copyright (c) 2026 Federico Pereira <lord.basex@gmail.com>
#
# Creates deploy/.env from its example the first time, with a random
# TURN_SECRET (openssl rand -hex 32). An existing .env is never changed.
#
#   deploy/setup-env.sh local        # from .env.example (make up)
#   deploy/setup-env.sh production   # from .env.production.example (make deploy, on the server)
#
# In production it stops until the example values (example.com) are
# replaced with the server's own: domain, email, origins and addresses.
set -eu

mode="${1:-local}"
cd "$(dirname "$0")"

case "$mode" in
local) example=.env.example ;;
production) example=.env.production.example ;;
*) echo "usage: $0 local|production" >&2; exit 1 ;;
esac

if [ ! -f .env ]; then
	secret="$(openssl rand -hex 32 2>/dev/null || od -An -N32 -tx1 /dev/urandom | tr -d ' \n')"
	umask 077
	sed "s/^TURN_SECRET=.*/TURN_SECRET=$secret/" "$example" > .env
	echo "Created deploy/.env with a random TURN_SECRET (only this server knows it)."
	if [ "$mode" = production ]; then
		echo "Now edit $(pwd)/.env: SIGNAL_DOMAIN, ACME_EMAIL, ALLOWED_ORIGINS, STUN_URLS, TURN_URLS, TURN_REALM and TURN_EXTERNAL_IP. Then deploy again." >&2
		exit 2
	fi
fi

if [ "$mode" = production ] && grep -q "example\.com\|203\.0\.113\." .env; then
	echo "$(pwd)/.env still has example values (example.com or 203.0.113.x): set the server's own and deploy again." >&2
	exit 3
fi
