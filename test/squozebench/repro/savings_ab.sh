#!/bin/sh
# A/B харнесса squozebench: релиз squoze, который пинит go.mod шлюза
# (сегодня v0.3.0), против локального рабочего дерева squoze.
#
#   MSYS_NO_PATHCONV=1 docker run --rm -v "$PWD:/w" -v "/abs/path/to/squoze:/squoze" golang:1.23 sh /w/test/squozebench/repro/savings_ab.sh
#
# MSYS_NO_PATHCONV=1 обязателен из Git Bash. N=<повторов> переопределяет число
# прогонов на каждой стороне (по умолчанию 3): один прогон даёт экономию, но не
# даёт разброса латентности, а p95 по одному прогону не значит ничего.
#
# Сторона HEAD собирается через -modfile=go.local.mod: Go читает альтернативный
# модуль-файл (и производный go.local.sum), поэтому go.mod и go.sum репозитория
# не меняются вовсе и восстанавливать их не нужно — прерванный прогон не
# оставляет дерево с replace на локальный каталог.
export PATH=$PATH:/usr/local/go/bin
export GOFLAGS=-mod=mod
cd /w || exit 1
OUT=/w/test/results/squoze_ab
mkdir -p "$OUT"
N=${N:-3}

echo "=== BASELINE: squoze release pinned by go.mod ==="
i=1
while [ "$i" -le "$N" ]; do
  go run ./test/squozebench > /dev/null 2>&1 || { echo "baseline run $i failed"; break; }
  cp /w/test/results/squoze_quality_report.json "$OUT/base.$i.json"
  i=$((i + 1))
done

echo "=== HEAD: local squoze working tree (-modfile=go.local.mod) ==="
cp go.mod go.local.mod
cp go.sum go.local.sum
go mod edit -replace github.com/Rethinger/squoze=/squoze go.local.mod
go mod tidy -modfile=go.local.mod >/dev/null 2>&1
i=1
while [ "$i" -le "$N" ]; do
  go run -modfile=go.local.mod ./test/squozebench > /dev/null 2>&1 || { echo "head run $i failed"; break; }
  cp /w/test/results/squoze_quality_report.json "$OUT/head.$i.json"
  i=$((i + 1))
done
rm -f go.local.mod go.local.sum

# Канонической парой для cmp_savings.mjs берётся первый прогон каждой стороны.
cp "$OUT/base.1.json" "$OUT/squoze_quality_report.base.json"
cp "$OUT/head.1.json" "$OUT/squoze_quality_report.head.json"

echo "=== reports written to test/results/squoze_ab ==="
echo "go.mod untouched:"
grep -n "squoze v" go.mod
