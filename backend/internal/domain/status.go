// Package domain содержит ядро предметной области кредитного конвейера:
// статусы заявки, допустимые переходы, продукты и типы решений.
// Пакет не зависит от инфраструктуры (БД, HTTP) и полностью покрыт unit-тестами.
package domain

import "fmt"

// Status — состояние кредитной заявки.
type Status string

const (
	StatusDraft        Status = "DRAFT"         // черновик, клиент заполняет
	StatusSubmitted    Status = "SUBMITTED"     // отправлена, ждёт обработки конвейером
	StatusChecking     Status = "CHECKING"      // проверки: ЕГРЮЛ/ЕГРИП, ФНС, БКИ
	StatusNeedsInfo    Status = "NEEDS_INFO"    // банк запросил данные или документы
	StatusScoring      Status = "SCORING"       // скоринг и кредитная политика
	StatusManualReview Status = "MANUAL_REVIEW" // серая зона — решение андеррайтера
	StatusApproved     Status = "APPROVED"      // одобрено на запрошенных условиях
	StatusCounterOffer Status = "COUNTER_OFFER" // одобрено на иных условиях
	StatusRejected     Status = "REJECTED"      // отказ банка
	StatusSigned       Status = "SIGNED"        // клиент подписал оферту (ПЭП, смоделировано)
	StatusIssued       Status = "ISSUED"        // средства выданы (смоделировано)
	StatusDeclined     Status = "DECLINED"      // клиент отказался от предложения
	StatusCancelled    Status = "CANCELLED"     // клиент отозвал заявку
)

// AllStatuses — полный список статусов в порядке жизненного цикла.
var AllStatuses = []Status{
	StatusDraft, StatusSubmitted, StatusChecking, StatusNeedsInfo, StatusScoring,
	StatusManualReview, StatusApproved, StatusCounterOffer, StatusRejected,
	StatusSigned, StatusIssued, StatusDeclined, StatusCancelled,
}

// ActorKind — кто инициирует переход.
type ActorKind string

const (
	ActorClient      ActorKind = "client"
	ActorSystem      ActorKind = "system"
	ActorUnderwriter ActorKind = "underwriter"
)

// transitions описывает машину состояний: из статуса → в статус → кто может инициировать.
// Любой переход, не описанный здесь, запрещён. Это единственный источник правды,
// по нему же генерируется диаграмма в docs/state-machine.md.
var transitions = map[Status]map[Status][]ActorKind{
	StatusDraft: {
		StatusSubmitted: {ActorClient},
		StatusCancelled: {ActorClient},
	},
	StatusSubmitted: {
		StatusChecking: {ActorSystem},
	},
	StatusChecking: {
		StatusNeedsInfo: {ActorSystem},
		StatusScoring:   {ActorSystem},
		StatusRejected:  {ActorSystem}, // стоп-фактор (например, компания ликвидируется)
	},
	StatusNeedsInfo: {
		StatusSubmitted: {ActorClient}, // клиент дополнил данные
		StatusCancelled: {ActorClient},
	},
	StatusScoring: {
		StatusApproved:     {ActorSystem},
		StatusCounterOffer: {ActorSystem},
		StatusManualReview: {ActorSystem},
		StatusRejected:     {ActorSystem},
	},
	StatusManualReview: {
		StatusApproved:     {ActorUnderwriter},
		StatusCounterOffer: {ActorUnderwriter},
		StatusRejected:     {ActorUnderwriter},
		StatusNeedsInfo:    {ActorUnderwriter},
	},
	StatusApproved: {
		StatusSigned:   {ActorClient},
		StatusDeclined: {ActorClient},
	},
	StatusCounterOffer: {
		StatusSigned:   {ActorClient},
		StatusDeclined: {ActorClient},
	},
	StatusSigned: {
		StatusIssued: {ActorSystem},
	},
}

// CanTransition проверяет, разрешён ли переход from → to для данного актора.
func CanTransition(from, to Status, actor ActorKind) bool {
	targets, ok := transitions[from]
	if !ok {
		return false
	}
	actors, ok := targets[to]
	if !ok {
		return false
	}
	for _, a := range actors {
		if a == actor {
			return true
		}
	}
	return false
}

// ValidateTransition возвращает ErrInvalidTransition с пояснением, если переход запрещён.
func ValidateTransition(from, to Status, actor ActorKind) error {
	if CanTransition(from, to, actor) {
		return nil
	}
	return fmt.Errorf("%w: %s → %s (actor=%s)", ErrInvalidTransition, from, to, actor)
}

// Transition — одно ребро графа состояний (для документации и API).
type Transition struct {
	From   Status      `json:"from"`
	To     Status      `json:"to"`
	Actors []ActorKind `json:"actors"`
}

// Transitions возвращает все рёбра графа в стабильном порядке.
func Transitions() []Transition {
	var out []Transition
	for _, from := range AllStatuses {
		for _, to := range AllStatuses {
			if actors, ok := transitions[from][to]; ok {
				out = append(out, Transition{From: from, To: to, Actors: actors})
			}
		}
	}
	return out
}

// IsTerminal — статус, из которого нет переходов.
func (s Status) IsTerminal() bool {
	return len(transitions[s]) == 0
}

// IsValid — известный ли статус.
func (s Status) IsValid() bool {
	for _, st := range AllStatuses {
		if st == s {
			return true
		}
	}
	return false
}

// StatusInfo — человекочитаемое описание статуса для клиента.
// Клиент не должен разбираться во внутренних процессах банка (см. кейс, стр. 5),
// поэтому каждый статус сопровождается понятным текстом и подсказкой о следующем шаге.
type StatusInfo struct {
	Status       Status `json:"status"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	NextStep     string `json:"next_step"`
	ClientAction bool   `json:"client_action"` // требуется ли действие клиента
	Stage        int    `json:"stage"`         // номер этапа в трекере (1..5)
}

var statusInfo = map[Status]StatusInfo{
	StatusDraft:        {Title: "Черновик", Description: "Заявка ещё не отправлена в банк.", NextStep: "Проверьте параметры и нажмите «Отправить».", ClientAction: true, Stage: 1},
	StatusSubmitted:    {Title: "Отправлена", Description: "Заявка получена банком и стоит в очереди на проверку.", NextStep: "Ничего делать не нужно — обычно это занимает до минуты.", Stage: 2},
	StatusChecking:     {Title: "Проверяем данные", Description: "Сверяем сведения о компании с ЕГРЮЛ/ЕГРИП, ФНС и бюро кредитных историй.", NextStep: "Ничего делать не нужно.", Stage: 2},
	StatusNeedsInfo:    {Title: "Нужны дополнительные данные", Description: "Чтобы принять решение, банку не хватает сведений.", NextStep: "Загрузите запрошенные документы и отправьте заявку повторно.", ClientAction: true, Stage: 2},
	StatusScoring:      {Title: "Оцениваем заявку", Description: "Рассчитываем кредитный рейтинг и подбираем условия.", NextStep: "Ничего делать не нужно.", Stage: 3},
	StatusManualReview: {Title: "На рассмотрении у специалиста", Description: "Заявку изучает кредитный эксперт банка.", NextStep: "Обычно это занимает до одного рабочего дня. Мы сообщим о решении здесь.", Stage: 3},
	StatusApproved:     {Title: "Одобрено", Description: "Банк готов выдать кредит на запрошенных условиях.", NextStep: "Ознакомьтесь с условиями и подпишите договор.", ClientAction: true, Stage: 4},
	StatusCounterOffer: {Title: "Есть предложение", Description: "Банк готов выдать кредит, но на других условиях.", NextStep: "Сравните предложение с запросом: подпишите его или откажитесь.", ClientAction: true, Stage: 4},
	StatusRejected:     {Title: "Отказ", Description: "К сожалению, сейчас банк не может выдать кредит.", NextStep: "Посмотрите причины и рекомендации — можно подать заявку снова позже.", Stage: 4},
	StatusSigned:       {Title: "Договор подписан", Description: "Договор подписан, готовим выдачу средств.", NextStep: "Ничего делать не нужно — деньги поступят на расчётный счёт.", Stage: 5},
	StatusIssued:       {Title: "Кредит выдан", Description: "Средства зачислены на расчётный счёт компании.", NextStep: "График платежей доступен в карточке заявки.", Stage: 5},
	StatusDeclined:     {Title: "Вы отказались", Description: "Вы отказались от предложения банка.", NextStep: "Можно подать новую заявку на других условиях.", Stage: 4},
	StatusCancelled:    {Title: "Отозвана", Description: "Заявка отозвана.", NextStep: "Можно создать новую заявку.", Stage: 1},
}

// Info возвращает клиентское описание статуса.
func (s Status) Info() StatusInfo {
	info := statusInfo[s]
	info.Status = s
	return info
}
