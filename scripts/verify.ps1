$ErrorActionPreference = "Stop"

Push-Location (Split-Path -Parent $PSScriptRoot)
try {
    gofmt -w .\cmd .\internal .\gen
    go test ./...

    if (Get-Command docker -ErrorAction SilentlyContinue) {
        docker run --rm `
            --mount "type=bind,source=$PWD,target=/src" `
            --workdir /src `
            ubuntu:24.04 `
            bash -lc "apt-get update -qq && apt-get install -y -qq cmake g++ >/dev/null && cmake -S agent -B build/agent -DSENTINEL_BUILD_TESTS=ON && cmake --build build/agent --parallel && ctest --test-dir build/agent --output-on-failure"
    }
} finally {
    Pop-Location
}
