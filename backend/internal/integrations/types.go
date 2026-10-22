// Package integrations — контракты и HTTP-клиент внешних источников данных:
// ЕГРЮЛ/ЕГРИП, ФНС «Прозрачный бизнес», бюро кредитных историй (БКИ).
//
// Контракты упрощены (кейс, «Пространство решений», п. 5), но сохраняют смысл
// интеграций: реестр подтверждает существование и статус бизнеса, ФНС даёт
// отчётность и налоговые риски, БКИ — платёжную дисциплину и текущую нагрузку.
// Реальные сервисы заменены mock-сервисом cmd/mockgov с тем же контрактом.
package integrations

// RegistryRecord — выписка ЕГРЮЛ/ЕГРИП.
type RegistryRecord struct {
	INN          string `json:"inn"`
	OGRN         string `json:"ogrn"`
	Kind         string `json:"kind"` // LEGAL / INDIVIDUAL
	FullName     string `json:"full_name"`
	ShortName    string `json:"short_name"`
	Status       string `json:"status"`        // ACTIVE / LIQUIDATING / LIQUIDATED
	RegisteredAt string `json:"registered_at"` // YYYY-MM-DD
	OKVED        string `json:"okved"`
	OKVEDName    string `json:"okved_name"`
	Region       string `json:"region"`
	Address      string `json:"address"`
	Director     string `json:"director"`
}

// FinReport — годовая отчётность.
type FinReport struct {
	Year      int   `json:"year"`
	Revenue   int64 `json:"revenue"`
	NetProfit int64 `json:"net_profit"`
}

// FNSProfile — сведения сервиса «Прозрачный бизнес».
type FNSProfile struct {
	INN                  string      `json:"inn"`
	Employees            int         `json:"employees"`
	TaxRegime            string      `json:"tax_regime"`
	Reports              []FinReport `json:"reports"` // от последнего года к прошлым
	TaxArrears           int64       `json:"tax_arrears"`
	MassAddress          bool        `json:"mass_address"`
	DisqualifiedDirector bool        `json:"disqualified_director"`
	Bankruptcy           bool        `json:"bankruptcy"`
}

// Loan — кредит в кредитной истории.
type Loan struct {
	Lender         string `json:"lender"`
	Kind           string `json:"kind"`
	Amount         int64  `json:"amount"`
	Outstanding    int64  `json:"outstanding"`
	MonthlyPayment int64  `json:"monthly_payment"`
	OpenedAt       string `json:"opened_at"`
	ClosedAt       string `json:"closed_at,omitempty"`
	MaxOverdueDays int    `json:"max_overdue_days"`
	Status         string `json:"status"` // ACTIVE / CLOSED
}

// CreditReport — кредитный отчёт БКИ.
type CreditReport struct {
	INN          string `json:"inn"`
	Found        bool   `json:"found"`
	Loans        []Loan `json:"loans"`
	Inquiries30d int    `json:"inquiries_30d"`
}

// Summary агрегирует отчёт БКИ для скоринга.
func (r CreditReport) Summary() (active, closed int, monthly int64, maxOverdue int) {
	for _, l := range r.Loans {
		if l.Status == "ACTIVE" {
			active++
			monthly += l.MonthlyPayment
		} else {
			closed++
		}
		if l.MaxOverdueDays > maxOverdue {
			maxOverdue = l.MaxOverdueDays
		}
	}
	return
}
