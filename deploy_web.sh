#!/bin/bash
# deploy_web.sh: build the Flowsearch web server and switch to the new one WITHOUT downtime.
# The site is mysearch-front (port 8812, never restarts) in front of mysearch-web@8830 or mysearch-web@8831. The new build is started on the
# port that is not in use, and only when its index is loaded (the port opens only then) does the front service start sending visitors to it.
# Then the old one is stopped. Use this instead of `systemctl restart mysearch-web`.
set -e
cd "${FLOWSEARCH_HOME:-/srv/uploads/mysearch}"
go vet ./web
go build -o mysearch-web.new ./web
mv mysearch-web.new mysearch-web
cur=$(cat /run/mysearch-front.target 2>/dev/null || true)
cur=${cur##*:}
[ -z "$cur" ] && cur=18830
if [ "$cur" = 18830 ]; then new=18831; else new=18830; fi
# ports 18830 / 18831 are only for Flowsearch (8830 and 8831 belong to other services on this box)
if ss -ltn | grep -q ":$new "; then systemctl stop mysearch-web@$new 2>/dev/null || true; sleep 2; fi
if ss -ltn | grep -q ":$new "; then echo "port $new is used by something else: stopping"; exit 1; fi
systemctl restart mysearch-web@$new
echo "the new web server is loading its index on port $new (the site stays up on port $cur) ..."
ok=0
for i in $(seq 1 240); do
  # ready = Flowsearch itself answers (its stats say how many pages it knows), not just any program on that port
  if curl -s -m 3 "http://127.0.0.1:$new/api/stats" | grep -q '"pages"'; then ok=1; break; fi
  sleep 1
done
if [ "$ok" != 1 ]; then
  echo "the new web server was not ready in 4 minutes: staying on port $cur"
  systemctl stop mysearch-web@$new || true
  exit 1
fi
echo "127.0.0.1:$new" > /run/mysearch-front.target
sleep 3
systemctl stop mysearch-web@$cur 2>/dev/null || true
echo "done: the site now runs on port $new"
