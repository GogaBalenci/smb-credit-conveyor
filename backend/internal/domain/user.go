package domain

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// Role — роль пользователя.
type Role string

const (
	RoleClient      Role = "client"      // представитель бизнеса
	RoleUnderwriter Role = "underwriter" // кредитный эксперт банка
)

// User — учётная запись.
type User struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	FullName     string    `json:"full_name"`
	Role         Role      `json:"role"`
	CompanyID    string    `json:"company_id,omitempty"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
}

// Event — запись журнала переходов заявки.
type Event struct {
	ID            int64     `json:"id"`
	ApplicationID string    `json:"application_id"`
	From          Status    `json:"from,omitempty"`
	To            Status    `json:"to"`
	ActorKind     ActorKind `json:"actor_kind"`
	ActorID       string    `json:"actor_id,omitempty"`
	Comment       string    `json:"comment,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// Document — загруженный клиентом документ (метаданные).
type Document struct {
	ID            string    `json:"id"`
	ApplicationID string    `json:"application_id"`
	Kind          DocKind   `json:"kind"`
	Filename      string    `json:"filename"`
	ContentType   string    `json:"content_type"`
	SizeBytes     int64     `json:"size_bytes"`
	SHA256        string    `json:"sha256"`
	UploadedBy    string    `json:"uploaded_by"`
	UploadedAt    time.Time `json:"uploaded_at"`
}

// NewID генерирует UUID v4 без внешних зависимостей.
func NewID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
