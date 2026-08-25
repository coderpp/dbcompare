.PHONY: frontend bindings test integration build build-server docker-up docker-down serve serve-stop serve-status serve-restart

serve: build-server
	chmod +x scripts/run-server.sh
	./scripts/run-server.sh start

serve-stop:
	./scripts/run-server.sh stop

serve-status:
	./scripts/run-server.sh status

serve-restart: build-server
	./scripts/run-server.sh restart

serve-daemon: build-server
	chmod +x scripts/install-launchagent.sh scripts/uninstall-launchagent.sh
	./scripts/install-launchagent.sh

serve-daemon-stop:
	./scripts/uninstall-launchagent.sh

frontend:
	cd frontend && npm install && npm run build

bindings:
	wails3 generate bindings -ts -names -i ./...

test:
	go test ./internal/...

integration: docker-up
	go test -tags integration ./test/integration/ -v -count=1

build: frontend
	CGO_ENABLED=1 go build -o bin/dbcompare .

build-server: frontend
	CGO_ENABLED=0 go build -tags server -o bin/dbcompare-server .

docker-up:
	cd docker && docker-compose up -d

docker-down:
	cd docker && docker-compose down
