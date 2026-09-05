#!/bin/sh
# Приёмка: харнесс squozebench плюс остальной набор тестов 2papi против
# рабочего дерева squoze.
#
#   MSYS_NO_PATHCONV=1 docker run --rm -v "$PWD:/w" -v "/abs/path/to/squoze:/squoze" golang:1.23 sh /w/test/squozebench/repro/accept.sh
#
# Через -modfile=go.local.mod: альтернативный модуль-файл с replace на /squoze,
# go.mod и go.sum репозитория не трогаются, восстанавливать нечего.
export PATH=$PATH:/usr/local/go/bin
export GOFLAGS=-mod=mod
cd /w || exit 1

cp go.mod go.local.mod
cp go.sum go.local.sum
go mod edit -replace github.com/Rethinger/squoze=/squoze go.local.mod
go mod tidy -modfile=go.local.mod 2>&1 | tail -5

echo "=== squozebench against LOCAL squoze ==="
go test -modfile=go.local.mod ./test/squozebench/ -v -count=1 2>&1 | grep -E "^(=== RUN|--- (PASS|FAIL|SKIP)|ok|FAIL|PASS)|ENVELOPE|NON-DETERMIN|PREFIX BROKEN|kept:|deterministic|history stable|output is" | head -70

echo "=== rest of the 2papi suite ==="
go build -modfile=go.local.mod ./... 2>&1 | tail -10
go test -modfile=go.local.mod ./... -count=1 2>&1 | grep -vE "no test files" | tail -25

rm -f go.local.mod go.local.sum
echo "=== go.mod untouched ==="
grep -n "squoze" go.mod
