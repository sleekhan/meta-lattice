.PHONY: all build windows build-all release package test clean install

# Binary names
BINARY_NAME=meta-lattice
WIN_BINARY_NAME=meta-lattice.exe
SRC_DIR=./src
TEST_DIR=./tests
VERSION?=v1.0.0
PWD=$(shell pwd)

all: build windows

## build: Compile native binary with LatticeDB
build:
	@echo "Building native binary ($(BINARY_NAME)) with LatticeDB..."
	@cp -f deps/latticedb/lib/darwin-arm64/liblattice.dylib ./ 2>/dev/null || true
	PKG_CONFIG_PATH="$(PWD)/deps/latticedb/lib/pkgconfig:$$PKG_CONFIG_PATH" \
	CGO_LDFLAGS="-Wl,-rpath,@executable_path -Wl,-rpath,$(PWD)" \
	go build -ldflags="-s -w" -o $(BINARY_NAME) $(SRC_DIR)

## windows: Cross-compile for Windows x86 64-bit (windows/amd64)
windows:
	@echo "Building Windows x86 64-bit binary ($(WIN_BINARY_NAME))..."
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o $(WIN_BINARY_NAME) $(SRC_DIR)

## build-all: Compile both native and Windows x86 64-bit binaries
build-all: build windows
	@echo "All binaries successfully built."

## release / package: Build multi-platform release packages (Windows, Linux, macOS) without tests
release: package
package:
	@echo "Building distribution packages for $(VERSION)..."
	@./scripts/build-release.sh $(VERSION)

## test: Run all regression tests
test:
	@echo "Running tests..."
	PKG_CONFIG_PATH="$(PWD)/deps/latticedb/lib/pkgconfig:$$PKG_CONFIG_PATH" \
	CGO_LDFLAGS="-Wl,-rpath,@executable_path -Wl,-rpath,$(PWD) -Wl,-rpath,$(PWD)/deps/latticedb/lib" \
	go test -v -count=1 $(TEST_DIR)

## clean: Remove compiled binaries and dist folder
clean:
	@echo "Cleaning up binaries and dist folder..."
	rm -rf $(BINARY_NAME) $(WIN_BINARY_NAME) dist/ liblattice.dylib

## install: Run the interactive installer
install: build
	./$(BINARY_NAME) install

