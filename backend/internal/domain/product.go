package domain

// Purpose — цель финансирования, с которой начинается клиентский путь
// («на что нужны деньги?»), а не выбор продукта из витрины.
type Purpose string

const (
	PurposeWorkingCapital Purpose = "working_capital" // пополнить оборотные средства, закупка товара
	PurposeCashGap        Purpose = "cash_gap"        // кассовый разрыв, зарплата, налоги
	PurposeEquipment      Purpose = "equipment"       // покупка оборудования, транспорта
	PurposeExpansion      Purpose = "expansion"       // новая точка, расширение, ремонт
	PurposeTender         Purpose = "tender"          // исполнение госконтракта
	PurposeRefinance      Purpose = "refinance"       // рефинансирование кредитов других банков
)

// Purposes — список целей с подписями для UI.
var Purposes = []struct {
	Code  Purpose `json:"code"`
	Title string  `json:"title"`
	Hint  string  `json:"hint"`
}{
	{PurposeWorkingCapital, "Пополнить оборотные средства", "Закупка товара, сырья, оплата поставщикам"},
	{PurposeCashGap, "Закрыть кассовый разрыв", "Зарплата, налоги, аренда, пока не пришла оплата от клиентов"},
	{PurposeEquipment, "Купить оборудование или транспорт", "Станки, техника, автомобили"},
	{PurposeExpansion, "Расширить бизнес", "Новая точка, ремонт, запуск направления"},
	{PurposeTender, "Исполнить госконтракт", "Финансирование расходов по 44-ФЗ / 223-ФЗ"},
	{PurposeRefinance, "Рефинансировать кредиты", "Снизить платёж по кредитам других банков"},
}

// IsValid — известная ли цель.
func (p Purpose) IsValid() bool {
	for _, x := range Purposes {
		if x.Code == p {
			return true
		}
	}
	return false
}

// Product — кредитный продукт. Параметры синтетические (кейс, ограничение 03).
type Product struct {
	Code               string    `json:"code"`
	Name               string    `json:"name"`
	Description        string    `json:"description"`
	Purposes           []Purpose `json:"purposes"`
	MinAmount          int64     `json:"min_amount"`
	MaxAmount          int64     `json:"max_amount"`
	MinTermMonths      int       `json:"min_term_months"`
	MaxTermMonths      int       `json:"max_term_months"`
	BaseRate           float64   `json:"base_rate"`           // % годовых
	AutoDecisionLimit  int64     `json:"auto_decision_limit"` // выше — только ручное решение
	RequiresCollateral bool      `json:"requires_collateral"`
	MaxDebtLoad        float64   `json:"max_debt_load"` // макс. доля всех платежей по кредитам в месячной выручке
	DecisionTime       string    `json:"decision_time"` // ориентир для клиента
}

// Products — каталог продуктов. Хранится в коде (а не в БД), т.к. меняется вместе
// с кредитной политикой и проходит ревью как код; в проде — справочник с версионированием.
var Products = []Product{
	{
		Code: "EXPRESS", Name: "Экспресс-кредит без залога",
		Description: "Быстрое решение онлайн без залога и поручительства. Подходит для любых целей.",
		Purposes:    []Purpose{PurposeWorkingCapital, PurposeCashGap, PurposeEquipment, PurposeExpansion},
		MinAmount:   100_000, MaxAmount: 5_000_000, MinTermMonths: 6, MaxTermMonths: 36,
		BaseRate: 21.5, AutoDecisionLimit: 5_000_000, MaxDebtLoad: 0.25, DecisionTime: "несколько минут",
	},
	{
		Code: "WORKING_CAPITAL", Name: "Кредит на пополнение оборотных средств",
		Description: "Для закупок, оплаты поставщикам и финансирования текущей деятельности.",
		Purposes:    []Purpose{PurposeWorkingCapital, PurposeCashGap, PurposeRefinance},
		MinAmount:   500_000, MaxAmount: 30_000_000, MinTermMonths: 3, MaxTermMonths: 36,
		BaseRate: 19.0, AutoDecisionLimit: 10_000_000, MaxDebtLoad: 0.3, DecisionTime: "до 1 рабочего дня",
	},
	{
		Code: "OVERDRAFT", Name: "Овердрафт",
		Description: "Платежи сверх остатка на счёте. Гасится автоматически из поступлений — удобно для кассовых разрывов.",
		Purposes:    []Purpose{PurposeCashGap},
		MinAmount:   100_000, MaxAmount: 10_000_000, MinTermMonths: 1, MaxTermMonths: 12,
		BaseRate: 20.0, AutoDecisionLimit: 3_000_000, MaxDebtLoad: 0.3, DecisionTime: "несколько минут",
	},
	{
		Code: "INVESTMENT", Name: "Инвестиционный кредит",
		Description: "На покупку оборудования, транспорта, недвижимости и развитие бизнеса. Длинный срок, залог приобретаемого имущества.",
		Purposes:    []Purpose{PurposeEquipment, PurposeExpansion, PurposeRefinance},
		MinAmount:   1_000_000, MaxAmount: 100_000_000, MinTermMonths: 12, MaxTermMonths: 84,
		BaseRate: 17.5, AutoDecisionLimit: 15_000_000, RequiresCollateral: true, MaxDebtLoad: 0.35, DecisionTime: "до 3 рабочих дней",
	},
	{
		Code: "TENDER", Name: "Кредит на исполнение госконтракта",
		Description: "Финансирование расходов по контракту 44-ФЗ / 223-ФЗ с погашением из оплаты заказчика.",
		Purposes:    []Purpose{PurposeTender},
		MinAmount:   500_000, MaxAmount: 50_000_000, MinTermMonths: 3, MaxTermMonths: 24,
		BaseRate: 18.5, AutoDecisionLimit: 10_000_000, MaxDebtLoad: 0.4, DecisionTime: "до 2 рабочих дней",
	},
}

// ProductByCode ищет продукт в каталоге.
func ProductByCode(code string) (Product, bool) {
	for _, p := range Products {
		if p.Code == code {
			return p, true
		}
	}
	return Product{}, false
}

// SupportsPurpose — подходит ли продукт под цель.
func (p Product) SupportsPurpose(purpose Purpose) bool {
	for _, x := range p.Purposes {
		if x == purpose {
			return true
		}
	}
	return false
}
