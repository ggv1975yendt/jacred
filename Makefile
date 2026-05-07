BINARY=torrent-search
PORT?=8080

.PHONY: run build deps clean

## Скачать зависимости
deps:
	go mod tidy

## Собрать бинарник
build: deps
	go build -o $(BINARY) .

## Запустить (без сборки, go run)
run: deps
	PORT=$(PORT) go run .

## Собрать и запустить
start: build
	PORT=$(PORT) ./$(BINARY)

## Очистить артефакты
clean:
	rm -f $(BINARY)

## Собрать под Linux (для Docker)
build-linux:
	GOOS=linux GOARCH=amd64 go build -o $(BINARY)-linux .
