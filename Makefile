BINARY := cli2api
PKG    := ./cmd/cli2api
OUT    := bin/$(BINARY)

GOFLAGS := -trimpath
LDFLAGS := -s -w

.PHONY: build run test vet fmt clean docker docker-run

build:
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(OUT) $(PKG)

run: build
	$(OUT)

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

clean:
	rm -rf bin

docker:
	docker build -t cli2api:dev .

docker-run:
	docker run --rm -p 8080:8080 \
		-e MULERUN_TOKEN=$$MULERUN_TOKEN \
		-v $$HOME/.mulerun:/home/nonroot/.mulerun:ro \
		cli2api:dev
