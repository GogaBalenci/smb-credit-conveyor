package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"alfahack/backend/internal/auth"
	"alfahack/backend/internal/domain"
	"alfahack/backend/internal/policy"
	"alfahack/backend/internal/store"
)

// Take — андеррайтер берёт заявку в работу (видно в журнале и очереди).
func (s *Service) Take(ctx context.Context, p auth.Principal, id string) (domain.Application, error) {
	if err := requireRole(p, domain.RoleUnderwriter); err != nil {
		return domain.Application{}, err
	}
	var out domain.Application
	err := s.store.InTx(ctx, func(q *store.Queries) error {
		app, err := q.LockApplication(ctx, id)
		if err != nil {
			return err
		}
		if app.Status != domain.StatusManualReview {
			return fmt.Errorf("%w: в работу берутся только заявки на ручном рассмотрении", domain.ErrInvalidTransition)
		}
		app.AssigneeID = p.UserID
		if out, err = q.SaveApplication(ctx, app); err != nil {
			return err
		}
		return q.InsertEvent(ctx, domain.Event{ApplicationID: id, From: app.Status, To: app.Status,
			ActorKind: domain.ActorUnderwriter, ActorID: p.UserID, Comment: "Заявка взята в работу кредитным экспертом"})
	})
	return out, err
}

// UnderwriterDecision — решение эксперта.
type UnderwriterDecision struct {
	Action     string           `json:"action"` // approve / reject / request_info
	Amount     int64            `json:"amount,omitempty"`
	TermMonths int              `json:"term_months,omitempty"`
	Rate       float64          `json:"rate,omitempty"`
	Comment    string           `json:"comment"`
	Documents  []domain.DocKind `json:"documents,omitempty"`
}

// Decide применяет решение андеррайтера к заявке на ручном рассмотрении.
func (s *Service) Decide(ctx context.Context, p auth.Principal, id string, in UnderwriterDecision) (domain.Application, error) {
	if err := requireRole(p, domain.RoleUnderwriter); err != nil {
		return domain.Application{}, err
	}
	app, err := s.store.GetApplication(ctx, id)
	if err != nil {
		return app, err
	}
	product, _ := domain.ProductByCode(app.ProductCode)
	comment := strings.TrimSpace(in.Comment)
	v := domain.NewValidation()

	switch in.Action {
	case "approve":
		amount, term := in.Amount, in.TermMonths
		if amount == 0 {
			amount = app.Amount
		}
		if term == 0 {
			term = app.TermMonths
		}
		if amount < product.MinAmount || amount > product.MaxAmount {
			v.Add("amount", fmt.Sprintf("Сумма вне лимитов продукта: %s – %s", domain.FormatRub(product.MinAmount), domain.FormatRub(product.MaxAmount)))
		}
		if term < product.MinTermMonths || term > product.MaxTermMonths {
			v.Add("term_months", fmt.Sprintf("Срок вне лимитов продукта: %d – %d мес.", product.MinTermMonths, product.MaxTermMonths))
		}
		rate := in.Rate
		if rate == 0 {
			rate = product.BaseRate
			if app.Score != nil {
				rate = policy.Rate(product, app.Score.Grade)
			}
		}
		if rate < product.BaseRate-3 || rate > 40 {
			v.Add("rate", "Ставка вне допустимого диапазона")
		}
		if err := v.OrNil(); err != nil {
			return app, err
		}
		offer := policy.MakeOffer(product, amount, term, rate)
		outcome, to := domain.OutcomeApproved, domain.StatusApproved
		reasons := []string{"Решение принято кредитным экспертом."}
		if amount != app.Amount || term != app.TermMonths {
			outcome, to = domain.OutcomeCounterOffer, domain.StatusCounterOffer
			reasons = []string{fmt.Sprintf("Кредитный эксперт предлагает %s на %d мес.", domain.FormatRub(amount), term)}
		}
		if comment != "" {
			reasons = append(reasons, comment)
		}
		return s.decide(ctx, p, id, to, comment, domain.Decision{Outcome: outcome, Reasons: reasons,
			ReasonCodes: []string{"UNDERWRITER_APPROVED"}, Offer: offer})

	case "reject":
		if comment == "" {
			v.Add("comment", "Укажите причину отказа — клиент увидит её в кабинете")
			return app, v
		}
		return s.decide(ctx, p, id, domain.StatusRejected, comment, domain.Decision{Outcome: domain.OutcomeRejected,
			ReasonCodes: []string{"UNDERWRITER_REJECTED"}, Reasons: []string{comment},
			Suggestions: []string{"Повторную заявку можно подать через 3 месяца."}})

	case "request_info":
		if len(in.Documents) == 0 {
			v.Add("documents", "Выберите хотя бы один документ")
		}
		for _, k := range in.Documents {
			if !k.IsValid() {
				v.Add("documents", "Неизвестный тип документа: "+string(k))
			}
		}
		if err := v.OrNil(); err != nil {
			return app, err
		}
		reason := comment
		if reason == "" {
			reason = "Запрошено кредитным экспертом"
		}
		return s.transition(ctx, transitionReq{
			appID: id, to: domain.StatusNeedsInfo, actor: domain.ActorUnderwriter, actorID: p.UserID, comment: reason,
			mutate: func(a *domain.Application) error {
				for _, k := range in.Documents {
					found := false
					for i := range a.RequiredDocs {
						if a.RequiredDocs[i].Kind == k {
							a.RequiredDocs[i].Provided, a.RequiredDocs[i].Reason, found = false, reason, true
						}
					}
					if !found {
						a.RequiredDocs = append(a.RequiredDocs, domain.DocRequest{Kind: k, Reason: reason})
					}
				}
				return nil
			},
		})
	default:
		v.Add("action", "Допустимо: approve, reject, request_info")
		return app, v
	}
}

func (s *Service) decide(ctx context.Context, p auth.Principal, id string, to domain.Status, comment string, d domain.Decision) (domain.Application, error) {
	if d.Suggestions == nil {
		d.Suggestions = []string{}
	}
	d.DecidedBy, d.Comment, d.PolicyVersion, d.DecidedAt = p.UserID, comment, policy.Version, s.now()
	app, err := s.transition(ctx, transitionReq{
		appID: id, to: to, actor: domain.ActorUnderwriter, actorID: p.UserID, comment: comment,
		mutate: func(a *domain.Application) error {
			a.Decision = &d
			if a.AssigneeID == "" {
				a.AssigneeID = p.UserID
			}
			return nil
		},
	})
	if errors.Is(err, domain.ErrInvalidTransition) {
		return app, fmt.Errorf("%w: решение принимается только по заявкам на ручном рассмотрении", domain.ErrInvalidTransition)
	}
	return app, err
}

// Stats — сводка для рабочего места банка.
type Stats struct {
	ByStatus           map[domain.Status]int `json:"by_status"`
	Total              int                   `json:"total"`
	AvgAutoDecisionS   float64               `json:"avg_auto_decision_sec"`
	AvgManualDecisionS float64               `json:"avg_manual_decision_sec"`
	Queue              store.QueueStats      `json:"queue"`
	AutomationRate     float64               `json:"automation_rate"` // доля решений без участия человека
}

// GetStats считает сводку по конвейеру.
func (s *Service) GetStats(ctx context.Context, p auth.Principal) (Stats, error) {
	if err := requireRole(p, domain.RoleUnderwriter); err != nil {
		return Stats{}, err
	}
	var st Stats
	var err error
	if st.ByStatus, err = s.store.StatusCounts(ctx); err != nil {
		return st, err
	}
	for _, n := range st.ByStatus {
		st.Total += n
	}
	if st.AvgAutoDecisionS, st.AvgManualDecisionS, err = s.store.AvgDecisionSeconds(ctx); err != nil {
		return st, err
	}
	if st.Queue, err = s.store.TaskStats(ctx); err != nil {
		return st, err
	}
	var auto, manual int
	err = s.store.DB.QueryRowContext(ctx, `SELECT
		count(*) FILTER (WHERE decision->>'decided_by' = 'system' AND status <> 'MANUAL_REVIEW'),
		count(*) FILTER (WHERE decision IS NOT NULL AND (decision->>'decided_by' <> 'system' OR status = 'MANUAL_REVIEW'))
		FROM applications`).Scan(&auto, &manual)
	if err == nil && auto+manual > 0 {
		st.AutomationRate = float64(auto) / float64(auto+manual)
	}
	return st, err
}
