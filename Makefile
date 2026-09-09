.PHONY: web server implant test tidy

web:
	cd web && npm install && npm run build
	rm -rf webui/dist
	cp -R web/dist webui/dist

server:
	mkdir -p bin
	go build -o bin/server ./cmd/server

implant:
	$(MAKE) -C implant

test:
	go test ./...

tidy:
	go mod tidy
