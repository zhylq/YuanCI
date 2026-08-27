.PHONY: test build web docker

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
