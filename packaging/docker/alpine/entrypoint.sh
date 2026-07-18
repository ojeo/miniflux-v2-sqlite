#!/bin/sh
# Fix ownership of /var/lib/miniflux on startup so the miniflux user
# can write the SQLite database file, regardless of how the volume was mounted.
#
# - Without external volume: the directory was created during build with
#   miniflux:miniflux ownership. This is a no-op.
# - With external volume: the mounted directory's ownership is updated to
#   miniflux:miniflux, ensuring writability inside the container.
chown miniflux /var/lib/miniflux 2>/dev/null || true

exec su-exec miniflux "$@"
