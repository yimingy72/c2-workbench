.PHONY: web server implant test tidy run stop tools dist slim-tools templates

web:
	cd web && npm install && npm run build
	rm -rf webui/dist
	cp -R web/dist webui/dist

server:
	mkdir -p bin
	go build -o bin/server ./cmd/server

implant:
	$(MAKE) -C implant

templates: implant
	mkdir -p implant/templates
	cp -f implant/implant.exe implant/templates/windows-x64.exe
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags '-s -w -buildid=' -o implant/templates/linux-amd64 ./cmd/implant
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags '-s -w -buildid=' -o implant/templates/linux-arm64 ./cmd/implant
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags '-s -w -buildid=' -o implant/templates/darwin-amd64 ./cmd/implant
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags '-s -w -buildid=' -o implant/templates/darwin-arm64 ./cmd/implant

test:
	go test ./...

tidy:
	go mod tidy

run: server
	mkdir -p logs
	@if lsof -nP -iTCP:8082 -sTCP:LISTEN >/dev/null 2>&1; then \
		echo "already listening on 8082"; \
	else \
		nohup ./bin/server -config conf/setting.conf >> logs/server.log 2>&1 & \
		echo $$! > logs/server.pid; \
		echo "started pid $$(cat logs/server.pid)  http://127.0.0.1:8082"; \
	fi

ZIG_VER := 0.14.0
UPX_VER := 4.2.4
UNAME_S := $(shell uname -s)
UNAME_M := $(shell uname -m)
ifeq ($(UNAME_S),Darwin)
ifeq ($(UNAME_M),arm64)
ZIG_DIST := zig-macos-aarch64-$(ZIG_VER)
else
ZIG_DIST := zig-macos-x86_64-$(ZIG_VER)
endif
else
ifeq ($(UNAME_M),aarch64)
ZIG_DIST := zig-linux-aarch64-$(ZIG_VER)
UPX_DIST := upx-$(UPX_VER)-arm64_linux
else
ZIG_DIST := zig-linux-x86_64-$(ZIG_VER)
UPX_DIST := upx-$(UPX_VER)-amd64_linux
endif
endif

tools:
	mkdir -p tools
	@if [ ! -x tools/zig/zig ]; then \
		echo "fetch $(ZIG_DIST)"; \
		curl -fsSL "https://ziglang.org/download/$(ZIG_VER)/$(ZIG_DIST).tar.xz" -o /tmp/$(ZIG_DIST).tar.xz; \
		rm -rf tools/zig $(ZIG_DIST); \
		tar -xJf /tmp/$(ZIG_DIST).tar.xz; \
		mv $(ZIG_DIST) tools/zig; \
		rm -f /tmp/$(ZIG_DIST).tar.xz; \
		echo "zig -> tools/zig/zig"; \
	else \
		echo "tools/zig/zig exists"; \
	fi
	@$(MAKE) slim-tools
	@if [ ! -x tools/garble ]; then \
		echo "build garble"; \
		GOBIN="$(CURDIR)/tools" GOPROXY=https://proxy.golang.org,direct go install mvdan.cc/garble@latest; \
	else \
		echo "tools/garble exists"; \
	fi
	@if [ "$(UNAME_S)" = "Linux" ] && [ ! -x tools/upx ] && [ -n "$(UPX_DIST)" ]; then \
		echo "fetch $(UPX_DIST)"; \
		curl -fsSL "https://github.com/upx/upx/releases/download/v$(UPX_VER)/$(UPX_DIST).tar.xz" -o /tmp/$(UPX_DIST).tar.xz; \
		tar -xJf /tmp/$(UPX_DIST).tar.xz -C /tmp; \
		cp /tmp/$(UPX_DIST)/upx tools/upx; \
		chmod +x tools/upx; \
		rm -rf /tmp/$(UPX_DIST) /tmp/$(UPX_DIST).tar.xz; \
		echo "upx -> tools/upx"; \
	elif [ -x tools/upx ]; then \
		echo "tools/upx exists"; \
	else \
		echo "UPX: macOS 无官方二进制，跳过（Linux 发布包会带）"; \
	fi

slim-tools:
	@if [ -d tools/zig ]; then \
		rm -rf tools/zig/doc tools/zig/LICENSE tools/zig/lib/docs \
			tools/zig/lib/libcxx tools/zig/lib/libcxxabi tools/zig/lib/tsan tools/zig/lib/fuzzer \
			tools/zig/lib/libc/musl tools/zig/lib/libc/glibc tools/zig/lib/libc/wasi tools/zig/lib/libc/darwin; \
		rm -f tools/zig/*.md tools/zig/*.txt tools/zig/*.html 2>/dev/null || true; \
		if [ -d tools/zig/lib/libc/include ]; then \
			cd tools/zig/lib/libc/include && for d in *; do \
				case "$$d" in any-windows-any|*windows*) ;; *) rm -rf "$$d" ;; esac; \
			done; \
		fi; \
		echo "slimmed tools/zig"; \
	fi

dist: tools server
	mkdir -p dist
	tar -C . -cJf dist/c2-platform.tar.xz \
		--exclude 'tools/zig/doc' \
		bin/server tools webui conf/setting.conf.example
	@ls -lh dist/c2-platform.tar.xz
	@echo "unpacked tools:" && du -sh tools tools/zig tools/garble 2>/dev/null || true

stop:
	@if [ -f logs/server.pid ]; then kill $$(cat logs/server.pid) 2>/dev/null || true; rm -f logs/server.pid; fi
	@pkill -f './bin/server -config' 2>/dev/null || true
	@echo stopped
