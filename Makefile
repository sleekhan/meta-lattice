.PHONY: all build windows build-all release package test clean install \
	docker-builder docker-shell build-host windows-host test-host

# Binary names
BINARY_NAME=meta-lattice
WIN_BINARY_NAME=meta-lattice.exe
SRC_DIR=./src
TEST_DIR=./tests
VERSION?=v1.0.0
PWD=$(shell pwd)

# DOCKER=1 (default): build HOST source inside a reproducible Go toolchain
# container (no host Go/CGO installation required). Portable binaries use the
# embedded pure-Go engine (-tags nolattice).
# DOCKER=0: build directly on the host (requires Go + C compiler; macOS/Linux
# produce native LatticeDB binaries, Windows cross-builds fall back).
DOCKER?=1
BUILDER_IMAGE?=meta-lattice-builder:latest

all: build windows

## build: Compile binary for the host platform (default fallback/portable or host-native depending on DOCKER)
build:
ifeq ($(DOCKER),1)
	@BUILDER_IMAGE=$(BUILDER_IMAGE) VERSION=$(VERSION) ./scripts/docker-build.sh build $(BINARY_NAME)
else
	@$(MAKE) build-host
endif

## build-native: Compile host binary WITH native LatticeDB (CGO enabled)
build-native:
ifeq ($(DOCKER),1)
	@BUILDER_IMAGE=$(BUILDER_IMAGE) VERSION=$(VERSION) ./scripts/docker-build.sh build-native $(BINARY_NAME)
else
	@$(MAKE) build-host
endif

## build-nolattice: Compile host binary WITHOUT LatticeDB (pure-Go engine, standalone)
build-nolattice:
ifeq ($(DOCKER),1)
	@BUILDER_IMAGE=$(BUILDER_IMAGE) VERSION=$(VERSION) ./scripts/docker-build.sh build-nolattice $(BINARY_NAME)-nolattice
else
	@echo "Building pure-Go host binary ($(BINARY_NAME)-nolattice)..."
	CGO_ENABLED=0 go build -tags nolattice -ldflags="-s -w" -o $(BINARY_NAME)-nolattice $(SRC_DIR)
endif

## windows: Cross-compile for Windows x86 64-bit (defaults to portable nolattice engine)
windows: windows-nolattice

## windows-native: Compile Windows x86 64-bit binary WITH native LatticeDB (CGO + DLL)
windows-native:
	@echo "Building Windows x86 64-bit binary with native LatticeDB (CGO + DLL)..."
	@BUILDER_IMAGE=$(BUILDER_IMAGE) VERSION=$(VERSION) ./scripts/docker-build.sh windows-native $(WIN_BINARY_NAME)

## windows-nolattice: Cross-compile for Windows x86 64-bit WITHOUT LatticeDB (portable standalone)
windows-nolattice:
ifeq ($(DOCKER),1)
	@BUILDER_IMAGE=$(BUILDER_IMAGE) VERSION=$(VERSION) ./scripts/docker-build.sh windows-nolattice $(WIN_BINARY_NAME)
else
	@$(MAKE) windows-host
endif

## latticedb-dll: Compile LatticeDB Windows DLL using Zig Docker
latticedb-dll:
	@echo "Compiling LatticeDB Windows DLL via Zig Docker..."
	@./scripts/build-latticedb-dll.sh x86_64-windows-gnu

## build-all: Compile both host and Windows x86 64-bit binaries (standard portable)
build-all: build windows
	@echo "Standard binaries successfully built."

## build-all-flavors: Compile all variants (native with LatticeDB & pure-Go nolattice for all platforms)
build-all-flavors:
	@BUILDER_IMAGE=$(BUILDER_IMAGE) VERSION=$(VERSION) ./scripts/docker-build.sh all-flavors

## release / package: Build multi-platform release packages (Windows, Linux, macOS)
## Uses Docker by default (DOCKER=1); set DOCKER=0 for host-toolchain builds.
release: package
package:
	@echo "Building distribution packages for $(VERSION)..."
	@USE_DOCKER=$(DOCKER) BUILDER_IMAGE=$(BUILDER_IMAGE) ./scripts/build-release.sh $(VERSION)

## test: Run all regression tests (Docker by default)
test:
ifeq ($(DOCKER),1)
	@BUILDER_IMAGE=$(BUILDER_IMAGE) ./scripts/docker-build.sh test -v
else
	@$(MAKE) test-host
endif

## clean: Remove compiled binaries and dist folder
clean:
	@echo "Cleaning up binaries and dist folder..."
	rm -rf $(BINARY_NAME) $(WIN_BINARY_NAME) meta-lattice-linux-* dist/ liblattice.dylib liblattice.so

## install: Run the interactive installer
install: build
	./$(BINARY_NAME) install

## docker-builder: (Re)build the Docker Go toolchain image
docker-builder:
	@BUILDER_IMAGE=$(BUILDER_IMAGE) ./scripts/docker-build.sh builder

## docker-shell: Open an interactive shell in the Docker Go toolchain
docker-shell:
	@BUILDER_IMAGE=$(BUILDER_IMAGE) ./scripts/docker-build.sh shell

## build-host: Compile native binary with LatticeDB directly on the host
build-host:
	@echo "Building native binary ($(BINARY_NAME)) with LatticeDB (host toolchain)..."
	@cp -f deps/latticedb/lib/darwin-arm64/liblattice.dylib ./ 2>/dev/null || true
	PKG_CONFIG_PATH="$(PWD)/deps/latticedb/lib/pkgconfig:$$PKG_CONFIG_PATH" \
	CGO_LDFLAGS="-Wl,-rpath,@executable_path -Wl,-rpath,$(PWD)" \
	go build -ldflags="-s -w" -o $(BINARY_NAME) $(SRC_DIR)

## windows-host: Cross-compile for Windows directly on the host (fallback engine)
windows-host:
	@echo "Building Windows x86 64-bit binary ($(WIN_BINARY_NAME)) (host toolchain)..."
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -tags nolattice -ldflags="-s -w" -o $(WIN_BINARY_NAME) $(SRC_DIR)

## test-host: Run all regression tests directly on the host
test-host:
	@echo "Running tests (host toolchain)..."
	PKG_CONFIG_PATH="$(PWD)/deps/latticedb/lib/pkgconfig:$$PKG_CONFIG_PATH" \
	CGO_LDFLAGS="-Wl,-rpath,@executable_path -Wl,-rpath,$(PWD) -Wl,-rpath,$(PWD)/deps/latticedb/lib" \
	go test -v -count=1 $(TEST_DIR)
