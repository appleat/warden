APP      := warden
VERSION  ?= 1.0.0
BUILT    := $(shell date +%Y-%m-%dT%H:%M:%S)
LDFLAGS  := -s -w -X main.version=$(VERSION) -X main.buildTime=$(BUILT)

.PHONY: help web build run dev linux clean tidy

help:
	@echo "make web        构建前端（npm install + vite build）"
	@echo "make build      构建当前平台二进制（自动先构建前端）"
	@echo "make run        构建并运行"
	@echo "make linux      交叉编译 linux/amd64 与 linux/arm64"
	@echo "make clean      清理产物"

web:
	cd web && npm install --no-audit --no-fund && npm run build

build: web
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(APP) ./cmd/server
	@echo "built bin/$(APP)"

run: build
	./bin/$(APP)

dev:
	go run ./cmd/server -listen :8080

linux: web
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(APP)-linux-amd64 ./cmd/server
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(APP)-linux-arm64 ./cmd/server
	@ls -lh bin/$(APP)-linux-*

tidy:
	go mod tidy

clean:
	rm -rf bin web/dist
