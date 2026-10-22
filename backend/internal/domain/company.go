package domain

import (
	"encoding/json"
	"time"
)

// CompanyKind — организационная форма.
type CompanyKind string

const (
	CompanyLegal      CompanyKind = "LEGAL"      // юрлицо (ЕГРЮЛ)
	CompanyIndividual CompanyKind = "INDIVIDUAL" // ИП (ЕГРИП)
)

// Company — профиль клиента-бизнеса. Заполняется автоматически из ЕГРЮЛ/ЕГРИП
// по ИНН, клиент вводит только ИНН.
type Company struct {
	ID             string      `json:"id"`
	INN            string      `json:"inn"`
	OGRN           string      `json:"ogrn"`
	Name           string      `json:"name"`
	Kind           CompanyKind `json:"kind"`
	OKVED          string      `json:"okved"`
	OKVEDName      string      `json:"okved_name"`
	Region         string      `json:"region"`
	Address        string      `json:"address"`
	Director       string      `json:"director"`
	RegisteredAt   time.Time   `json:"registered_at"`
	RegistryStatus string      `json:"registry_status"` // ACTIVE / LIQUIDATING / LIQUIDATED
	CreatedAt      time.Time   `json:"created_at"`
}

// AgeMonths — возраст бизнеса в месяцах на дату now.
func (c Company) AgeMonths(now time.Time) int {
	if c.RegisteredAt.IsZero() {
		return 0
	}
	y, m, _ := now.Date()
	ry, rm, _ := c.RegisteredAt.Date()
	months := (y-ry)*12 + int(m-rm)
	if now.Day() < c.RegisteredAt.Day() {
		months--
	}
	if months < 0 {
		return 0
	}
	return months
}

// OKVEDSection возвращает класс ОКВЭД (первые 2 цифры), например "47".
func (c Company) OKVEDSection() string {
	if len(c.OKVED) < 2 {
		return ""
	}
	return c.OKVED[:2]
}

// CheckSource — источник внешней проверки.
type CheckSource string

const (
	SourceRegistry CheckSource = "REGISTRY" // ЕГРЮЛ / ЕГРИП
	SourceFNS      CheckSource = "FNS"      // «Прозрачный бизнес»
	SourceBKI      CheckSource = "BKI"      // бюро кредитных историй
)

// CheckStatus — итог отдельной проверки.
type CheckStatus string

const (
	CheckOK      CheckStatus = "ok"
	CheckWarning CheckStatus = "warning"
	CheckFail    CheckStatus = "fail"
)

// CheckResult — результат обращения к внешнему источнику, сохраняется для аудита
// и показывается андеррайтеру.
type CheckResult struct {
	Source    CheckSource     `json:"source"`
	Status    CheckStatus     `json:"status"`
	Summary   string          `json:"summary"`
	Payload   json.RawMessage `json:"payload"`
	CheckedAt time.Time       `json:"checked_at"`
}
