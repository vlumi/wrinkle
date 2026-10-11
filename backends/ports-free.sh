#!/bin/bash
# Fails with a clear message when a backend port is already taken, usually by
# another backend still running. Usage: ports-free.sh port...
busy=0
for port in "$@"; do
  holder=$(lsof -nP -iTCP:"$port" -sTCP:LISTEN 2>/dev/null | awk 'NR == 2 { print $1 " (pid " $2 ")" }')
  if [ -n "$holder" ]; then
    echo "port $port is in use by $holder" >&2
    busy=1
  fi
done
if [ "$busy" = 1 ]; then
  echo "Run one backend at a time: stop the other one (Ctrl-C) first." >&2
  exit 1
fi
