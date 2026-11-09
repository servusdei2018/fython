.PHONY: all build test lint format clean shim test-extensions

export GOTOOLCHAIN ?= go1.27.0

BIN_DIR := bin
BINARY := $(BIN_DIR)/fython
SRC := ./cmd/fython
SHIM_SRC := shim/fython_abi3_shim.c
SHIM_OUT := shim/libfython_abi3.so
SHIM_DYLIB := shim/libfython_abi3.dylib
SHIM_DLL := shim/python315.dll
TEST_EXT_SRC := testdata/capi/fython_testext.c
TEST_EXT_MULTI_SRC := testdata/capi/fython_testext_multi.c
EXT_DIR := testdata/capi/build
# Compute CPython extension suffix for the host (e.g. .cpython-315-x86_64-linux-gnu.so)
EXT_SUFFIX := $(shell python3-config --extension-suffix 2>/dev/null || echo .so)

all: lint test build

build:
	@mkdir -p $(BIN_DIR)
	go build -o $(BINARY) $(SRC)

test:
	go test ./...

lint:
	golangci-lint run ./...

format:
	go fmt ./...
	@go tool goimports -local github.com/servusdei2018/fython -w cmd internal pkg

# Shim is pure C (no Python.h needed) — built separately so `go build ./...`
# stays CGO_ENABLED=0. fython loads it at runtime via purego when present.
shim: $(SHIM_OUT)

$(SHIM_OUT): $(SHIM_SRC)
	cc -fPIC -shared -o $@ $(SHIM_SRC)

shim-darwin: $(SHIM_DYLIB)

$(SHIM_DYLIB): $(SHIM_SRC)
	cc -fPIC -shared -o $@ $(SHIM_SRC)

shim-windows: $(SHIM_DLL)

$(SHIM_DLL): $(SHIM_SRC)
	cc -shared -o $@ $(SHIM_SRC)

# Test extensions built with system python3-devel (optional; tests skip if missing).
test-extensions: shim $(EXT_DIR)/fython_testext$(EXT_SUFFIX) $(EXT_DIR)/fython_testext_multi$(EXT_SUFFIX) $(EXT_DIR)/fython_testext.abi3.so

$(EXT_DIR)/fython_testext$(EXT_SUFFIX): $(TEST_EXT_SRC)
	@mkdir -p $(EXT_DIR)
	cc -fPIC -shared $$(python3-config --includes 2>/dev/null || echo -I/usr/include/python3.15) -o $@ $(TEST_EXT_SRC)

$(EXT_DIR)/fython_testext_multi$(EXT_SUFFIX): $(TEST_EXT_MULTI_SRC)
	@mkdir -p $(EXT_DIR)
	cc -fPIC -shared $$(python3-config --includes 2>/dev/null || echo -I/usr/include/python3.15) -o $@ $(TEST_EXT_MULTI_SRC)

$(EXT_DIR)/fython_testext.abi3.so: $(TEST_EXT_SRC)
	@mkdir -p $(EXT_DIR)
	cc -fPIC -shared -DPy_LIMITED_API=0x030A0000 $$(python3-config --includes 2>/dev/null || echo -I/usr/include/python3.15) -o $@ $(TEST_EXT_SRC)

clean:
	rm -rf $(BIN_DIR) $(SHIM_OUT) $(SHIM_DYLIB) $(SHIM_DLL) $(EXT_DIR)
	rm -f libfython_abi3.so
