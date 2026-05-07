# Архитектура проекта JACRED

## Структура проекта

```
jacred/
├── cmd/
│   └── main.go              # Точка входа приложения
├── server/
│   └── server.go            # HTTP сервер и обработка запросов
├── tracker/
│   ├── interface.go         # Общий интерфейс и типы данных
│   └── tracker.go           # Совместимость (пересчисление)
├── cron/
│   └── rutor/
│       └── tracker.go       # Реализация трекера Rutor
├── wwwroot/
│   └── index.html           # HTML шаблон интерфейса
├── Dockerfile
├── go.mod
├── Makefile
└── README.md
```

## Компоненты

### 1. Пакет `tracker` - Общая часть

**Файл: `tracker/interface.go`**

Определяет общий интерфейс и типы данных для всех трекеров:

```go
type Tracker interface {
    Name() string
    Search(query string) *SearchResult
}

type SearchResult struct {
    Query   string
    Source  string
    Count   int
    Results []Torrent
    Error   string
}

type Torrent struct {
    Name     string
    Magnet   string
    Size     string
    Seeds    string
    Peers    string
    Date     string
    InfoHash string
}
```

### 2. Пакет `cron/rutor` - Реализация Rutor трекера

**Файл: `cron/rutor/tracker.go`**

Содержит всю логику для поиска на Rutor:
- HTTP запросы к трекеру
- Парсинг HTML
- Обработка кодировок (Windows-1251)
- Извлечение информации о торрентах

Тип `Tracker` реализует интерфейс `tracker.Tracker`.

### 3. Пакет `server` - HTTP сервер

**Файл: `server/server.go`**

Обрабатывает HTTP запросы и координирует поиск по нескольким трекерам:

- `NewServer(trackers []tracker.Tracker, templatePath string)` - создание сервера с несколькими трекерами
- `searchAllTrackers(query string)` - **запуск каждого трекера в отдельной горутине** и сбор результатов
- Рендеринг HTML шаблона с результатами от всех трекеров

**Ключевая функция:**

```go
func (s *Server) searchAllTrackers(query string) []*tracker.SearchResult {
    results := make([]*tracker.SearchResult, len(s.trackers))
    var wg sync.WaitGroup

    for i, t := range s.trackers {
        wg.Add(1)
        go func(idx int, tr tracker.Tracker) {
            defer wg.Done()
            result := tr.Search(query)
            result.Source = tr.Name()
            results[idx] = result
        }(i, t)
    }

    wg.Wait()
    return results
}
```

Каждый трекер запускается в отдельной горутине с использованием `sync.WaitGroup` для ожидания результатов.

### 4. Интерфейс пользователя

**Файл: `wwwroot/index.html`**

- Отображает результаты от всех трекеров в отдельных таблицах
- Каждая таблица показывает результаты одного трекера
- Обновляется по мере получения результатов от различных трекеров
- Поддерживает копирование магнет-ссылок

## Как добавить новый трекер

1. Создать новый пакет в `cron/` (например, `cron/piratebay`):
   ```
   cron/piratebay/tracker.go
   ```

2. Реализовать интерфейс `tracker.Tracker`:
   ```go
   package piratebay

   import "jacred/tracker"

   type Tracker struct{}

   func New() *Tracker {
       return &Tracker{}
   }

   func (t *Tracker) Name() string {
       return "The Pirate Bay"
   }

   func (t *Tracker) Search(query string) *tracker.SearchResult {
       // Реализация поиска
       return &tracker.SearchResult{}
   }
   ```

3. Добавить трекер в `cmd/main.go`:
   ```go
   trackers := []tracker.Tracker{
       rutor.New(),
       piratebay.New(),  // Новый трекер
   }
   ```

## Процесс поиска

1. Пользователь вводит поисковый запрос в интерфейс
2. Запрос отправляется на `/search` или `/api/search`
3. Сервер создает отдельную горутину для каждого трекера
4. Каждая горутина независимо выполняет поиск на своем трекере
5. Сервер ожидает результатов от всех горутин (`wg.Wait()`)
6. Результаты отправляются клиенту и отображаются в таблицах

## Преимущества архитектуры

- ✅ **Параллелизм**: Каждый трекер работает независимо в отдельной горутине
- ✅ **Масштабируемость**: Легко добавить новые трекеры
- ✅ **Модульность**: Каждый трекер в отдельном пакете
- ✅ **Переиспользуемость**: Общие типы и интерфейсы в `tracker` пакете
- ✅ **Надежность**: Ошибка одного трекера не влияет на других
- ✅ **Гибкость**: Интерфейс пользователя обновляется по мере получения результатов
