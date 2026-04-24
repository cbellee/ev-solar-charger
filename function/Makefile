build:
	go build -o handler ./cmd/handler

build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o handler ./cmd/handler

test:
	go test ./...

start: build
	func start
