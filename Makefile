GNOROOT ?= /home/ubuntu/gnome-tavern/gno
GNO_BIN ?= /home/ubuntu/gnome-tavern/bin/gno
ADDR    ?= g1zt8w45ecxhlsdse7hd5gwr6xzcaxy9zsej6hl2

.PHONY: all test gno-test gno-lint go-test ts-test build e2e deploy-onyx

all: gno-test gno-lint build go-test ts-test

gno-test:
	GNOROOT=$(GNOROOT) $(GNO_BIN) test -root-dir $(GNOROOT) -v ./gno/p/$(ADDR)/tollgate ./gno/r/$(ADDR)/tollgate

gno-lint:
	GNOROOT=$(GNOROOT) $(GNO_BIN) lint -root-dir $(GNOROOT) -v ./gno/p/$(ADDR)/tollgate ./gno/r/$(ADDR)/tollgate

build:
	mkdir -p bin
	go build -o bin/tollgate-gateway ./cmd/tollgate-gateway
	go build -o bin/tollgate-agent ./cmd/tollgate-agent

go-test:
	go test -race -v ./...

ts-test:
	cd sdk/ts && npm test

e2e: build
	./scripts/e2e_gnodev.sh

deploy-onyx: build
	./scripts/deploy_onyx.sh
