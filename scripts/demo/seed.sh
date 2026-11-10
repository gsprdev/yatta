#!/usr/bin/env bash
# Builds a demo world from scratch: a fresh Redmine in podman and a yatta
# database whose uploaded records match that Redmine's time entries.
#
#   scripts/demo/seed.sh [--errors] [yatta.db path]   (default: yatta-demo.db)
#   yatta --db yatta-demo.db
#
# --errors adds the bad states (failed, departed, unassigned) for testing.
# Dates are relative to now: the last 10 working days plus today, a running
# timer, the oldest 7 days uploaded. Rerun to refresh.
#
# Redmine: http://127.0.0.1:3001, login admin / admin. The API key goes into
# the OS keyring under yatta/redmine-demo, so nothing else needs entering.
#
# Needs podman, curl, go, and sqlite3 >= 3.44 on PATH.
set -euo pipefail
cd "$(dirname "$0")/../.."

errors=0
if [[ ${1:-} == --errors ]]; then errors=1; shift; fi
db=$(realpath -m "${1:-yatta-demo.db}")

name=yatta-demo-redmine
port=3001
image=docker.io/library/redmine:7.0.1
token=yattademo0000000000000000000000000000000 # 40 chars, Redmine's token width

echo "Starting a fresh Redmine..."
podman rm -f "$name" >/dev/null 2>&1 || true
podman volume rm -f "$name" >/dev/null 2>&1 || true
podman run -d --name "$name" -p "$port:3000" -v "$name:/usr/src/redmine/sqlite" "$image" >/dev/null
for _ in $(seq 120); do
	curl -sf -o /dev/null "http://127.0.0.1:$port/" && break
	sleep 2
done
curl -sf -o /dev/null "http://127.0.0.1:$port/" || { echo "Redmine did not come up; see: podman logs $name" >&2; exit 1; }
podman exec -u redmine -e REDMINE_LANG=en "$name" bundle exec rake redmine:load_default_data >/dev/null
podman stop "$name" >/dev/null

echo "Seeding $db and Redmine..."
rm -f "$db" "$db-wal" "$db-shm"
last=$(ls internal/store/migrations/*.sql | tail -1)
version=$((10#$(basename "$last" | cut -d_ -f1)))
{ cat internal/store/migrations/*.sql; echo "PRAGMA user_version = $version;"; } | sqlite3 "$db"

extra=()
if ((errors)); then extra=(-cmd ".read scripts/demo/errors.sql"); fi
vol=$(podman volume inspect -f '{{.Mountpoint}}' "$name")
# The volume belongs to the container's user namespace; unshare to write it.
podman unshare sqlite3 "$db" \
	-cmd "ATTACH '$vol/redmine.db' AS rm" \
	-cmd ".parameter set @token '$token'" \
	-cmd ".parameter set @url 'http://127.0.0.1:$port'" \
	-cmd ".read scripts/demo/seed.sql" \
	"${extra[@]}" \
	"SELECT 'entries: ' || count(*) || ', uploaded: ' || count(remote_record_id) FROM time_entries;"

podman start "$name" >/dev/null
go run scripts/demo/setkey.go redmine-demo "$token"
echo "Done. Run: yatta --db $db"
