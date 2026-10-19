.PHONY: up down logs build run test check models shell

up:      ## build the image and start the app
	docker compose up -d --build

down:    ## stop the app, keep the volumes
	docker compose down

reset:   ## stop the app and wipe the recordings
	docker compose down -v

logs:
	docker compose logs -f

models:  ## download the speech recognition models
	docker compose run --rm models

build:   ## build the binary for this machine
	go build -o meeting_transcriber .

run:     ## run on this machine, no containers
	go run .

test:
	go test ./...

check:   ## vet and test
	go vet ./... && go test ./...

shell:   ## a shell in the running container
	docker compose exec app sh
