#!/bin/sh
# Fix ownership of /var/lib/miniflux on startup so the non-root user (65534)
# can write the SQLite database file, regardless of how the volume was mounted.
chown 65534 /var/lib/miniflux 2>/dev/null

exec su-exec 65534 "$@"
