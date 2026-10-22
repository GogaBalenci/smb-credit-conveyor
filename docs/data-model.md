# Модель данных

Схема — `backend/internal/store/migrations/0001_init.sql`. Миграции встроены в бинарник и применяются
при старте (`AUTO_MIGRATE=true`) или командой `conveyor migrate`.

```mermaid
erDiagram
    companies ||--o{ users : "сотрудники"
    companies ||--o{ applications : "заявки"
    users ||--o{ applications : "подал"
    users |o--o{ applications : "андеррайтер (assignee)"
    applications ||--o{ application_events : "журнал"
    applications ||--o{ check_results : "проверки"
    applications ||--o{ documents : "документы"
    applications ||--o{ tasks : "шаги конвейера"

    companies {
        uuid id PK
        text inn UK "ИНН, проверка контрольных цифр"
        text ogrn
        text name
        text kind "LEGAL | INDIVIDUAL"
        text okved
        text region
        date registered_at
        text registry_status "ACTIVE | LIQUIDATING | LIQUIDATED"
    }
    users {
        uuid id PK
        text email UK
        text password_hash "pbkdf2-sha256"
        text role "client | underwriter"
        uuid company_id FK "обязателен для client"
    }
    applications {
        uuid id PK
        text number UK "КЗ-2026-1001"
        uuid company_id FK
        uuid applicant_id FK
        text product_code
        text purpose
        bigint amount
        int term_months
        jsonb financials "выручка, прибыль, платежи, залог"
        text status
        jsonb required_docs "запрошенные документы"
        jsonb score "балл, грейд, факторы"
        jsonb decision "итог, причины, оффер"
        uuid assignee_id FK
        text sign_code_hash "одноразовый код ПЭП"
        int version "оптимистическая блокировка"
        timestamptz submitted_at
    }
    application_events {
        bigserial id PK
        uuid application_id FK
        text from_status
        text to_status
        text actor_kind "client | system | underwriter"
        text actor_id
        text comment
        timestamptz created_at
    }
    check_results {
        uuid application_id PK
        text source PK "REGISTRY | FNS | BKI"
        text status "ok | warning | fail"
        text summary
        jsonb payload "сырой ответ источника"
    }
    documents {
        uuid id PK
        uuid application_id FK
        text kind
        text filename
        text content_type
        bigint size_bytes
        text sha256
        bytea content
    }
    tasks {
        bigserial id PK
        text kind "checks | score | disburse"
        uuid application_id FK
        text status "pending | done | failed"
        int attempts
        timestamptz run_at
        text last_error
    }
    idempotency_keys {
        uuid user_id PK
        text key PK
        text request_hash
        int status_code
        bytea response
    }
    news {
        text id PK
        text title
        jsonb tags "okved, regions, kinds, stages, products, max_age_months"
        int priority
    }
```

## Почему так

- **Заявка — агрегат, журнал — append-only.** Текущее состояние лежит в `applications`, история
  переходов — в `application_events`, где записи только добавляются. Из журнала строятся история для клиента,
  аудит и метрики времени решения.
- **JSONB для скоринга и решения.** Набор факторов модели и структура решения будут меняться с версиями
  политики. JSONB позволяет хранить их вместе с `model_version` и `policy_version` без миграции на каждое изменение.
  Поля, по которым нужен поиск и фильтр (статус, сумма, продукт), — обычные колонки с индексами.
- **Результаты проверок хранятся.** Скоринг строится по сохранённым ответам источников, а не по новым запросам.
  Решение воспроизводимо, а андеррайтер видит ровно те данные, на которых система приняла решение.
- **Очередь в той же БД.** Для MVP это даёт атомарность «переход + следующий шаг» без распределённых
  транзакций. При росте нагрузки таблица `tasks` превращается в outbox, который публикуется в Kafka,
  а воркеры становятся консьюмерами (см. `docs/decisions.md`).
- **Документы в `bytea`.** Для прототипа так проще. В проде файлы уходят в объектное хранилище (S3-совместимое),
  в БД остаются метаданные и хэш.
- **Справочник продуктов в коде** (`domain/product.go`). Параметры продуктов — часть кредитной политики и
  проходят ревью вместе с кодом. В проде это версионируемый справочник.
