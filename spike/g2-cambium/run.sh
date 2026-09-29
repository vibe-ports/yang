#!/usr/bin/env bash
# Differential run: cambium pure-Go datatree (DTCHECK, built CGO_ENABLED=0) vs yanglint (libyang 5.8.6).
# Each case dir: *.yang, data.json|data.xml, args (yanglint type/print flags).
set -u
DTCHECK=${DTCHECK:?path to dtcheck binary}
YL=${YL:-/opt/homebrew/bin/yanglint}
cd "$(dirname "$0")/cases"
for d in */; do
  d=${d%/}; data=$(ls $d/data.* | head -1)
  yl=$(cd $d && $YL -D $(cat args) *.yang $(basename $data) 2>&1); ylrc=$?
  ct=$($DTCHECK $data $d/*.yang 2>&1); ctrc=$?
  v(){ [ $1 = 0 ] && echo accept || echo reject; }
  printf '%s | yanglint=%s | cambium=%s\n  YL: %s\n  CT: %s\n' "$d" "$(v $ylrc)" "$(v $ctrc)" \
    "$(echo "$yl" | tr '\n' ' ' | cut -c1-220)" "$(echo "$ct" | tr '\n' ' ' | cut -c1-220)"
done
