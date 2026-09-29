#!/bin/sh
# Сборка .exe для Windows (и бинарников для Linux) в папку dist/.
set -e
cd "$(dirname "$0")"
mkdir -p dist
for cmd in asiair-agent asiair-receiver; do
  GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "dist/$cmd.exe" "./cmd/$cmd"
  GOOS=linux   GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "dist/$cmd"     "./cmd/$cmd"
done
# раскладка кадров для Siril: только окно Windows
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -H windowsgui" -o dist/asiair-sorter.exe ./cmd/asiair-sorter
cp configs/*.example.yaml dist/
ls -l dist
