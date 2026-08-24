.PHONY: generate fmt test test-go build-agent test-agent

generate:
	go run github.com/bufbuild/buf/cmd/buf@v1.72.0 generate

fmt:
	gofmt -w ./cmd ./internal ./gen

test: test-go test-agent

test-go:
	go test ./...

build-agent:
	cmake -S agent -B build/agent -DSENTINEL_BUILD_TESTS=ON
	cmake --build build/agent --parallel

test-agent: build-agent
	ctest --test-dir build/agent --output-on-failure
