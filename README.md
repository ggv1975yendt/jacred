# JacRed

Веб-приложение на Go для поиска по торрент-трекерам. Аналог Jackett — единый HTTP-интерфейс и JSON API поверх нескольких трекеров одновременно.

---

## Возможности

- Поиск по названию с выводом результатов в таблицу (название, размер, сиды, пиры, дата)
- Прямые магнет-ссылки и кнопка копирования
- JSON API с авторизацией по ключу (совместимо с торрент-клиентами, поддерживающими Jackett)
- Несколько независимых API-ключей, каждый со своим набором трекеров
- Веб-панель администрирования (`/admin`) — настройка порта, API-ключей и параметров трекеров
- Горячая перезагрузка конфигурации — изменения применяются без перезапуска
- Светлая и тёмная тема (переключение сохраняется в браузере)
- Сортировка результатов по любому столбцу

---

## Структура проекта

```
jacred/
├── cmd/
│   └── main.go              # Точка входа
├── config/
│   └── config.go            # Структуры и загрузка init.yaml
├── cron/
│   └── rutor/
│       └── tracker.go       # Реализация трекера rutor.info
├── server/
│   ├── server.go            # HTTP-сервер (тонкая обёртка)
│   └── router/
│       └── router.go        # Все HTTP-обработчики, маршрутизация
├── tracker/
│   └── interface.go         # Интерфейс Tracker и типы данных
├── wwwroot/
│   ├── index.html           # Страница поиска
│   ├── admin.html           # Панель администрирования
│   └── static/
│       └── style.css        # Стили (темы: тёмная / светлая)
├── init.yaml                # Конфигурация
├── jacred.service           # Systemd unit-файл
├── Dockerfile
├── Makefile
└── README.md
```

---

## Конфигурация (`init.yaml`)

```yaml
port: "9117"

apis:
  - name: Мой Sonarr
    key: "supersecretkey"
    trackers:
      - rutor.info

trackers:
  rutor.info:
    domain: rutor.info          # Основной домен
    alt_domain: rutor.is        # Резервный домен
    categories:                 # ID категорий трекера
      - "1"
      - "5"
      - "4"
      - "16"
      - "12"
      - "6"
      - "7"
      - "10"
      - "17"
      - "13"
      - "15"
```

Файл создаётся автоматически при первом запуске. Все параметры можно менять через веб-интерфейс `/admin` — изменения применяются мгновенно без перезапуска.

---

## Сборка

### Требования

- Go 1.21 или выше ([скачать](https://go.dev/dl/))
- Доступ в интернет для загрузки зависимостей (только при первой сборке)

### Linux / macOS

```bash
# Клонировать репозиторий
git clone <url> jacred
cd jacred

# Собрать бинарник
go build -o jacred ./cmd

# Запустить
./jacred
```

Или через Makefile:

```bash
make build   # → бинарник ./jacred
make run     # запустить без сборки (go run)
make start   # собрать и запустить
```

### Windows

В PowerShell или cmd:

```powershell
git clone <url> jacred
cd jacred

go build -o jacred.exe ./cmd

.\jacred.exe
```

При сборке из PowerShell переменные окружения задаются так:

```powershell
$env:GOOS="windows"; $env:GOARCH="amd64"; go build -o jacred.exe ./cmd
```

### Кросс-компиляция

Go позволяет собрать бинарник под любую платформу с любой машины:

```bash
# Linux x86-64 (из Windows или macOS)
GOOS=linux GOARCH=amd64 go build -o jacred-linux ./cmd

# Windows (из Linux или macOS)
GOOS=windows GOARCH=amd64 go build -o jacred.exe ./cmd

# macOS Apple Silicon
GOOS=darwin GOARCH=arm64 go build -o jacred-darwin ./cmd

# Linux ARM64 (Raspberry Pi 4, серверы ARM)
GOOS=linux GOARCH=arm64 go build -o jacred-linux-arm64 ./cmd
```

Через Makefile:

```bash
make build-linux       # Linux x86-64
make build-windows     # Windows x86-64
make build-darwin      # macOS Apple Silicon
make build-linux-arm64 # Linux ARM64
make build-all         # все сразу
```

---

## Установка

### Linux (Ubuntu / Debian) — как системный сервис

```bash
# 1. Создать пользователя без привилегий
sudo useradd --system --no-create-home --shell /usr/sbin/nologin jacred

# 2. Создать рабочую директорию
sudo mkdir -p /opt/jacred/wwwroot/static
sudo cp jacred           /usr/local/bin/jacred
sudo cp init.yaml        /opt/jacred/
sudo cp wwwroot/index.html wwwroot/admin.html /opt/jacred/wwwroot/
sudo cp wwwroot/static/style.css /opt/jacred/wwwroot/static/
sudo chown -R jacred:jacred /opt/jacred

# 3. Установить systemd unit
sudo cp jacred.service /etc/systemd/system/
sudo systemctl daemon-reload

# 4. Включить автозапуск и запустить
sudo systemctl enable --now jacred

# Проверить статус
sudo systemctl status jacred

# Просмотр логов
sudo journalctl -u jacred -f
```

Остановка и перезапуск:

```bash
sudo systemctl stop    jacred
sudo systemctl restart jacred
```

#### Firewall (если нужен доступ извне)

```bash
sudo ufw allow 9117/tcp
```

### Windows — ручной запуск

```powershell
# Запустить в текущей консоли
cd C:\jacred
.\jacred.exe
```

#### Windows — регистрация как служба (через NSSM)

[NSSM](https://nssm.cc) — утилита для запуска любого исполняемого файла как службы Windows.

```powershell
# Скачать nssm и установить службу
nssm install JacRed "C:\jacred\jacred.exe"
nssm set JacRed AppDirectory "C:\jacred"
nssm set JacRed DisplayName "JacRed Torrent Search"
nssm set JacRed Start SERVICE_AUTO_START

nssm start JacRed
```

Управление:

```powershell
nssm stop    JacRed
nssm restart JacRed
nssm remove  JacRed confirm
```

### Docker

```bash
# Сборка образа
docker build -t jacred .

# Запуск (порт 9117, конфиг сохраняется в volume)
docker run -d \
  --name jacred \
  -p 9117:9117 \
  -v $(pwd)/init.yaml:/opt/jacred/init.yaml \
  jacred
```

#### Docker Compose

```yaml
services:
  jacred:
    build: .
    container_name: jacred
    ports:
      - "9117:9117"
    volumes:
      - ./init.yaml:/opt/jacred/init.yaml
    restart: unless-stopped
```

```bash
docker compose up -d
```

---

## Работа с приложением

### Веб-интерфейс поиска

Откройте в браузере: **http://localhost:9117**

- Введите название фильма, сериала или игры и нажмите «Найти»
- В результатах: нажмите кнопку магнита, чтобы открыть в торрент-клиенте, или скопируйте ссылку
- Столбцы таблицы кликабельны — сортируют результаты по возрастанию/убыванию
- Переключатель темы (☀ / 🌙) в правом верхнем углу — выбор запоминается

### Панель администрирования

Откройте: **http://localhost:9117/admin**

**Раздел «Сервер»** — изменение порта (вступает в силу после перезапуска).

**Раздел «API»** — управление API-ключами:
- Добавить / удалить ключ
- Задать название (для удобства)
- Сгенерировать случайный ключ (64 hex-символа)
- Назначить каждому ключу свой набор трекеров

**Раздел «Трекеры»** — параметры каждого трекера:
- Основной домен (например `rutor.info`)
- Резервный домен (переключается автоматически при недоступности основного)
- Список категорий через запятую

Кнопка **Сохранить** применяет изменения мгновенно — перезапуск не требуется.

---

## API

### Аутентификация

API-ключ передаётся одним из двух способов:

```
# Заголовок (предпочтительно)
X-Api-Key: ваш_ключ

# Или параметр URL
?apikey=ваш_ключ
```

### Поиск

```
GET /api/search?q=<запрос>[&apikey=<ключ>]
```

| Параметр | Тип | Описание |
|----------|-----|----------|
| `q` | string | Поисковый запрос (обязательный) |
| `apikey` | string | API-ключ (или через заголовок `X-Api-Key`) |

**Пример запроса:**

```bash
curl -H "X-Api-Key: supersecretkey" \
     "http://localhost:9117/api/search?q=терминатор"
```

**Пример ответа:**

```json
[
  {
    "query": "терминатор",
    "source": "rutor.info",
    "count": 12,
    "results": [
      {
        "name": "Терминатор / The Terminator (1984) BDRip",
        "magnet": "magnet:?xt=urn:btih:...",
        "size": "2.1 GB",
        "seeds": "42",
        "peers": "7",
        "date": "12.03.2024",
        "info_hash": "ABCDEF1234..."
      }
    ]
  }
]
```

**Коды ответов:**

| Код | Описание |
|-----|----------|
| 200 | Успех |
| 400 | Пустой запрос |
| 401 | Неверный или отсутствующий API-ключ |

### Совместимость с Sonarr / Radarr / Lidarr

Jackett-совместимые клиенты могут подключаться напрямую. Укажите в настройках:
- URL индексера: `http://localhost:9117/api/search`
- API Key: ваш ключ из `init.yaml`

---

## Добавление нового трекера

1. Создайте пакет `cron/<tracker_name>/tracker.go`, реализующий интерфейс:

```go
type Tracker interface {
    Name() string
    Search(query string) *tracker.SearchResult
}
```

2. Зарегистрируйте фабрику в `cmd/main.go`:

```go
factories := map[string]router.TrackerFactory{
    "rutor.info": func(tcfg config.TrackerConfig) tracker.Tracker {
        return rutor.New(tcfg)
    },
    "mytracker.org": func(tcfg config.TrackerConfig) tracker.Tracker {
        return mytracker.New(tcfg)
    },
}
```

3. Добавьте блок конфигурации в `init.yaml`:

```yaml
trackers:
  mytracker.org:
    domain: mytracker.org
    alt_domain: mytracker.net
    categories: []
```

Новый трекер появится в панели администрирования автоматически.