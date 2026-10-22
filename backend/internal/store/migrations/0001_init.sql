-- Схема кредитного конвейера. Описание модели данных: docs/data-model.md

CREATE TABLE companies (
    id              UUID PRIMARY KEY,
    inn             TEXT NOT NULL UNIQUE,
    ogrn            TEXT NOT NULL DEFAULT '',
    name            TEXT NOT NULL,
    kind            TEXT NOT NULL CHECK (kind IN ('LEGAL', 'INDIVIDUAL')),
    okved           TEXT NOT NULL DEFAULT '',
    okved_name      TEXT NOT NULL DEFAULT '',
    region          TEXT NOT NULL DEFAULT '',
    address         TEXT NOT NULL DEFAULT '',
    director        TEXT NOT NULL DEFAULT '',
    registered_at   DATE,
    registry_status TEXT NOT NULL DEFAULT 'ACTIVE',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE users (
    id            UUID PRIMARY KEY,
    email         TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    full_name     TEXT NOT NULL,
    role          TEXT NOT NULL CHECK (role IN ('client', 'underwriter')),
    company_id    UUID REFERENCES companies (id),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT client_has_company CHECK (role <> 'client' OR company_id IS NOT NULL)
);

CREATE SEQUENCE application_number_seq START 1001;

CREATE TABLE applications (
    id              UUID PRIMARY KEY,
    number          TEXT NOT NULL UNIQUE,
    company_id      UUID NOT NULL REFERENCES companies (id),
    applicant_id    UUID NOT NULL REFERENCES users (id),
    product_code    TEXT NOT NULL,
    purpose         TEXT NOT NULL DEFAULT '',
    purpose_comment TEXT NOT NULL DEFAULT '',
    amount          BIGINT NOT NULL DEFAULT 0 CHECK (amount >= 0),
    term_months     INT NOT NULL DEFAULT 0 CHECK (term_months >= 0),
    financials      JSONB NOT NULL DEFAULT '{}',
    status          TEXT NOT NULL,
    required_docs   JSONB NOT NULL DEFAULT '[]',
    score           JSONB,
    decision        JSONB,
    assignee_id     UUID REFERENCES users (id),
    sign_code_hash  TEXT,
    sign_code_until TIMESTAMPTZ,
    version         INT NOT NULL DEFAULT 1,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    submitted_at    TIMESTAMPTZ
);
CREATE INDEX applications_company_idx ON applications (company_id, created_at DESC);
CREATE INDEX applications_status_idx ON applications (status, updated_at);

-- Журнал переходов — аудит и таймлайн для клиента. Только вставка.
CREATE TABLE application_events (
    id             BIGSERIAL PRIMARY KEY,
    application_id UUID NOT NULL REFERENCES applications (id),
    from_status    TEXT,
    to_status      TEXT NOT NULL,
    actor_kind     TEXT NOT NULL,
    actor_id       TEXT NOT NULL DEFAULT '',
    comment        TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX application_events_app_idx ON application_events (application_id, id);

-- Результаты внешних проверок: последний ответ каждого источника по заявке.
CREATE TABLE check_results (
    application_id UUID NOT NULL REFERENCES applications (id),
    source         TEXT NOT NULL,
    status         TEXT NOT NULL,
    summary        TEXT NOT NULL,
    payload        JSONB NOT NULL,
    checked_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (application_id, source)
);

CREATE TABLE documents (
    id             UUID PRIMARY KEY,
    application_id UUID NOT NULL REFERENCES applications (id),
    kind           TEXT NOT NULL,
    filename       TEXT NOT NULL,
    content_type   TEXT NOT NULL,
    size_bytes     BIGINT NOT NULL,
    sha256         TEXT NOT NULL,
    content        BYTEA NOT NULL,
    uploaded_by    UUID NOT NULL REFERENCES users (id),
    uploaded_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX documents_app_idx ON documents (application_id);

-- Надёжная очередь задач конвейера (transactional outbox):
-- задача создаётся в той же транзакции, что и переход статуса, поэтому
-- ни один переход не «теряется» при падении процесса. Воркеры берут задачи
-- через FOR UPDATE SKIP LOCKED и могут масштабироваться горизонтально.
CREATE TABLE tasks (
    id             BIGSERIAL PRIMARY KEY,
    kind           TEXT NOT NULL,
    application_id UUID NOT NULL REFERENCES applications (id),
    status         TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'done', 'failed')),
    attempts       INT NOT NULL DEFAULT 0,
    max_attempts   INT NOT NULL DEFAULT 8,
    run_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error     TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX tasks_one_pending_idx ON tasks (application_id, kind) WHERE status = 'pending';
CREATE INDEX tasks_due_idx ON tasks (run_at) WHERE status = 'pending';

-- Идемпотентность POST-запросов: повтор с тем же ключом возвращает сохранённый ответ.
CREATE TABLE idempotency_keys (
    user_id      UUID NOT NULL,
    key          TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    status_code  INT NOT NULL,
    response     BYTEA NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, key)
);

CREATE TABLE news (
    id           TEXT PRIMARY KEY,
    title        TEXT NOT NULL,
    body         TEXT NOT NULL,
    category     TEXT NOT NULL,
    tags         JSONB NOT NULL DEFAULT '{}',
    priority     INT NOT NULL DEFAULT 0,
    link_action  TEXT NOT NULL DEFAULT '',
    published_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
