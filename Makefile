.PHONY: test build web docker proto proto-check

BUF_IMAGE := bufbuild/buf:1.72.0@sha256:65bd496a89c762ad7151ca9e7d885a45dacb3671a8e8ec39738b9f844d3405ea

test:
	go test ./...
	npm --prefix web test

web:
	npm --prefix web run build

build: web
	go build ./cmd/yuanci-server ./cmd/yuanci-runner ./cmd/yuancictl

docker:
	docker build --target server -t yuanci/server:dev .
	docker build --target runner -t yuanci/runner:dev .

# Generated bindings are committed. The pinned container and remote plugin
# versions make regeneration independent of a host protoc installation.
proto:
	docker run --rm -v "$(CURDIR):/workspace" -w /workspace $(BUF_IMAGE) generate

proto-check: proto
	git diff --exit-code -- api/runner/v1/runner.proto gen/runner/v1
