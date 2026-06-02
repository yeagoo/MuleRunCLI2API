BINARY := cli2api
PKG    := ./cmd/cli2api
OUT    := bin/$(BINARY)

GOFLAGS := -trimpath
LDFLAGS := -s -w

.PHONY: build run test vet fmt clean docker docker-run test-e2e test-e2e-live test-e2e-live-cheap

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

# End-to-end test scripts — see scripts/test_e2e.py for details.
# Uses a local venv at .venv/ for deps so it doesn't fight PEP 668.
PYTHON_VENV := .venv
PYTHON_BIN  := $(PYTHON_VENV)/bin/python

$(PYTHON_BIN):
	python3 -m venv $(PYTHON_VENV)
	$(PYTHON_VENV)/bin/pip install --quiet --upgrade pip
	$(PYTHON_VENV)/bin/pip install --quiet httpx openai anthropic

test-e2e: build $(PYTHON_BIN)
	$(PYTHON_BIN) scripts/test_e2e.py

test-e2e-live: build $(PYTHON_BIN)
	$(PYTHON_BIN) scripts/test_e2e.py --live

test-e2e-live-cheap: build $(PYTHON_BIN)
	$(PYTHON_BIN) scripts/test_e2e.py --live --skip-video
