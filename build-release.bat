@echo off
set GOOS=linux
set GOARCH=amd64
set CGO_ENABLED=0
D:\tools\go\bin\go.exe build -tags embedui -trimpath -ldflags "-s -w -buildid= -X main.version=0.1.5" -o dist\artex-linux-amd64\artex ./cmd/artex
