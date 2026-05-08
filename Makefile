BINARY   = jacred
CMD      = ./cmd
WWWROOT  = wwwroot
CONFIG   = init.yaml

.PHONY: run build deps clean \
        build-linux build-windows build-darwin \
        build-linux-arm64

## Скачать зависимости
deps:
	go mod tidy

## Собрать бинарник (текущая платформа)
build: deps
	go build -o $(BINARY) $(CMD)

## Запустить без сборки
run: deps
	go run $(CMD)

## Собрать и запустить
start: build
	./$(BINARY)

## Удалить артефакты
clean:
	rm -f $(BINARY) $(BINARY).exe $(BINARY)-linux $(BINARY)-linux-arm64 $(BINARY)-windows.exe $(BINARY)-darwin

## ─── Кросс-компиляция ───────────────────────────────────────

## Linux x86-64
build-linux: deps
	GOOS=linux GOARCH=amd64 go build -o $(BINARY)-linux $(CMD)

## Linux ARM64 (Raspberry Pi 4, Oracle Cloud ARM и т.п.)
build-linux-arm64: deps
	GOOS=linux GOARCH=arm64 go build -o $(BINARY)-linux-arm64 $(CMD)

## Windows x86-64
build-windows: deps
	GOOS=windows GOARCH=amd64 go build -o $(BINARY)-windows.exe $(CMD)

## macOS (Apple Silicon)
build-darwin: deps
	GOOS=darwin GOARCH=arm64 go build -o $(BINARY)-darwin $(CMD)

## Все платформы сразу
build-all: build-linux build-linux-arm64 build-windows build-darwin