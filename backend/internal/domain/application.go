package domain

import (
	"strings"
	"time"
)

// Financials — финансовые показатели, которые клиент подтверждает в заявке.
// Значения по умолчанию подставляются из ФНС («Прозрачный бизнес»), чтобы
// клиенту не приходилось вводить их вручную.
type Financials struct {
	AnnualRevenue      int64 `json:"annual_revenue"`      // выручка за последний год, ₽
	PrevAnnualRevenue  int64 `json:"prev_annual_revenue"` // выручка за предыдущий год, ₽
	AnnualNetProfit    int64 `json:"annual_net_profit"`   // чистая прибыль за год, ₽ (может быть < 0)
	MonthlyObligations int64 `json:"monthly_obligations"` // текущие платежи по кредитам в месяц, ₽
	CollateralValue    int64 `json:"collateral_value"`    // стоимость предлагаемого залога, ₽ (0 — без залога)
	Employees          int   `json:"employees"`
}

// Application — кредитная заявка.
type Application struct {
	ID             string       `json:"id"`
	Number         string       `json:"number"`
	CompanyID      string       `json:"company_id"`
	CompanyName    string       `json:"company_name,omitempty"`
	ApplicantID    string       `json:"applicant_id"`
	ProductCode    string       `json:"product_code"`
	Purpose        Purpose      `json:"purpose"`
	PurposeComment string       `json:"purpose_comment"`
	Amount         int64        `json:"amount"`
	TermMonths     int          `json:"term_months"`
	Financials     Financials   `json:"financials"`
	Status         Status       `json:"status"`
	RequiredDocs   []DocRequest `json:"required_docs"`
	Score          *ScoreResult `json:"score,omitempty"`
	Decision       *Decision    `json:"decision,omitempty"`
	AssigneeID     string       `json:"assignee_id,omitempty"`
	Version        int          `json:"version"`
	CreatedAt      time.Time    `json:"created_at"`
	UpdatedAt      time.Time    `json:"updated_at"`
	SubmittedAt    *time.Time   `json:"submitted_at,omitempty"`
}

// DocRequest — запрос банка на дополнительный документ.
type DocRequest struct {
	Kind     DocKind `json:"kind"`
	Reason   string  `json:"reason"`
	Provided bool    `json:"provided"`
}

// DocKind — тип документа.
type DocKind string

const (
	DocFinancialStatements DocKind = "financial_statements" // бухгалтерская отчётность
	DocBankStatement       DocKind = "bank_statement"       // выписка по счёту из другого банка
	DocCollateral          DocKind = "collateral"           // документы по залогу
	DocContract            DocKind = "contract"             // госконтракт
	DocOther               DocKind = "other"
)

// DocKindTitles — подписи для UI.
var DocKindTitles = map[DocKind]string{
	DocFinancialStatements: "Бухгалтерская отчётность за последний год",
	DocBankStatement:       "Выписка по расчётному счёту за 6 месяцев",
	DocCollateral:          "Документы на предмет залога",
	DocContract:            "Государственный контракт",
	DocOther:               "Иной документ",
}

// IsValid — известный ли тип документа.
func (k DocKind) IsValid() bool { _, ok := DocKindTitles[k]; return ok }

// MissingDocs возвращает запрошенные, но не предоставленные документы.
func (a *Application) MissingDocs() []DocRequest {
	var out []DocRequest
	for _, d := range a.RequiredDocs {
		if !d.Provided {
			out = append(out, d)
		}
	}
	return out
}

// MarkDocProvided отмечает документ как предоставленный.
func (a *Application) MarkDocProvided(kind DocKind) {
	for i := range a.RequiredDocs {
		if a.RequiredDocs[i].Kind == kind {
			a.RequiredDocs[i].Provided = true
		}
	}
}

// Editable — можно ли менять параметры заявки клиенту.
func (a *Application) Editable() bool {
	return a.Status == StatusDraft || a.Status == StatusNeedsInfo
}

// ApplicationInput — данные, которые клиент вводит в мастере.
type ApplicationInput struct {
	ProductCode    string     `json:"product_code"`
	Purpose        Purpose    `json:"purpose"`
	PurposeComment string     `json:"purpose_comment"`
	Amount         int64      `json:"amount"`
	TermMonths     int        `json:"term_months"`
	Financials     Financials `json:"financials"`
}

// Validate проверяет параметры заявки на соответствие продукту.
// Для черновика допускаются пустые поля (strict=false), при отправке — нет.
func (in ApplicationInput) Validate(strict bool) error {
	v := NewValidation()
	p, ok := ProductByCode(in.ProductCode)
	if !ok {
		v.Add("product_code", "Неизвестный продукт")
		return v
	}
	if in.Purpose != "" && !in.Purpose.IsValid() {
		v.Add("purpose", "Неизвестная цель финансирования")
	}
	if strict && in.Purpose == "" {
		v.Add("purpose", "Укажите цель финансирования")
	}
	if len(strings.TrimSpace(in.PurposeComment)) > 500 {
		v.Add("purpose_comment", "Не более 500 символов")
	}
	if in.Amount != 0 || strict {
		if in.Amount < p.MinAmount || in.Amount > p.MaxAmount {
			v.Add("amount", "Сумма должна быть от "+FormatRub(p.MinAmount)+" до "+FormatRub(p.MaxAmount))
		}
	}
	if in.TermMonths != 0 || strict {
		if in.TermMonths < p.MinTermMonths || in.TermMonths > p.MaxTermMonths {
			v.Add("term_months", "Срок должен быть от "+itoa(p.MinTermMonths)+" до "+itoa(p.MaxTermMonths)+" мес.")
		}
	}
	f := in.Financials
	if f.AnnualRevenue < 0 || f.PrevAnnualRevenue < 0 || f.MonthlyObligations < 0 || f.CollateralValue < 0 || f.Employees < 0 {
		v.Add("financials", "Показатели не могут быть отрицательными (кроме прибыли)")
	}
	if strict && f.AnnualRevenue == 0 {
		v.Add("financials.annual_revenue", "Укажите выручку за последний год")
	}
	if strict && p.RequiresCollateral && f.CollateralValue == 0 {
		v.Add("financials.collateral_value", "Для этого продукта нужен залог — укажите его стоимость")
	}
	return v.OrNil()
}
