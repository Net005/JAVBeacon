#!/bin/sh
set -eu

puid="${PUID:-1000}"
pgid="${PGID:-${GUID:-1000}}"

case "$puid:$pgid" in
  *[!0-9:]*|:*|*:)
    echo "PUID and PGID (or legacy GUID) must be numeric" >&2
    exit 1
    ;;
esac

mkdir -p /app/data

# Bind mounts keep their host ownership. Repair both existing content and the
# mount root before dropping privileges so newly-created cache files inherit
# the requested Docker identity as well.
chown -R "$puid:$pgid" /app/data
chmod -R u+rwX,g+rwX /app/data

umask 0002
exec su-exec "$puid:$pgid" /usr/local/bin/javbeacon "$@"
