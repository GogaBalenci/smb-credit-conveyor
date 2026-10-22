package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"alfahack/backend/internal/auth"
	"alfahack/backend/internal/domain"
	"alfahack/backend/internal/integrations"
	"alfahack/backend/internal/policy"
	"alfahack/backend/internal/recommend"
	"alfahack/backend/internal/store"
)

// CreateDraft создаёт черновик заявки.
func (s *Service) CreateDraft(ctx context.Context, p auth.Principal, in domain.ApplicationInput) (domain.Application, error) {
	if err := requireRole(p, domain.RoleClient); err != nil {
		return domain.Application{}, err
	}
	if err := in.Validate(false); err != nil {
		return domain.Application{}, err
	}
	var out domain.Application
	err := s.store.InTx(ctx, func(q *store.Queries) error {
		app, err := q.CreateApplication(ctx, domain.Application{
			CompanyID: p.CompanyID, ApplicantID: p.UserID, ProductCode: in.ProductCode, Purpose: in.Purpose,
			PurposeComment: strings.TrimSpace(in.PurposeComment), Amount: in.Amount, TermMonths: in.TermMonths,
			Financials: in.Financials, Status: domain.StatusDraft,
		})
		if err != nil {
			return err
		}
		out = app
		return q.InsertEvent(ctx, domain.Event{ApplicationID: app.ID, To: domain.StatusDraft, ActorKind: domain.ActorClient, ActorID: p.UserID})
	})
	return out, err
}

// UpdateDraft изменяет параметры заявки (черновик или дозапрос). version — версия,
// которую видел клиент: если заявку изменили параллельно, вернётся ErrConflict.
func (s *Service) UpdateDraft(ctx context.Context, p auth.Principal, id string, version int, in domain.ApplicationInput) (domain.Application, error) {
	if err := requireRole(p, domain.RoleClient); err != nil {
		return domain.Application{}, err
	}
	app, err := s.authorize(ctx, p, id)
	if err != nil {
		return app, err
	}
	if !app.Editable() {
		return app, fmt.Errorf("%w: заявку в статусе %s нельзя изменить", domain.ErrInvalidTransition, app.Status)
	}
	if err := in.Validate(false); err != nil {
		return app, err
	}
	if version != 0 && version != app.Version {
		return app, domain.ErrConflict
	}
	app.ProductCode, app.Purpose, app.PurposeComment = in.ProductCode, in.Purpose, strings.TrimSpace(in.PurposeComment)
	app.Amount, app.TermMonths, app.Financials = in.Amount, in.TermMonths, in.Financials
	return s.store.SaveApplication(ctx, app)
}

// Submit отправляет заявку в конвейер (из черновика или после дозапроса).
func (s *Service) Submit(ctx context.Context, p auth.Principal, id string) (domain.Application, error) {
	if err := requireRole(p, domain.RoleClient); err != nil {
		return domain.Application{}, err
	}
	if _, err := s.authorize(ctx, p, id); err != nil {
		return domain.Application{}, err
	}
	return s.transition(ctx, transitionReq{
		appID: id, to: domain.StatusSubmitted, actor: domain.ActorClient, actorID: p.UserID, task: TaskChecks,
		mutate: func(a *domain.Application) error {
			in := domain.ApplicationInput{ProductCode: a.ProductCode, Purpose: a.Purpose, PurposeComment: a.PurposeComment,
				Amount: a.Amount, TermMonths: a.TermMonths, Financials: a.Financials}
			if err := in.Validate(true); err != nil {
				return err
			}
			if missing := a.MissingDocs(); len(missing) > 0 {
				v := domain.NewValidation()
				v.Add("documents", "Загрузите запрошенные документы: "+docTitles(missing))
				return v
			}
			if a.SubmittedAt == nil {
				now := s.now()
				a.SubmittedAt = &now
			}
			return nil
		},
	})
}

// Cancel — клиент отзывает заявку.
func (s *Service) Cancel(ctx context.Context, p auth.Principal, id string) (domain.Application, error) {
	if err := requireRole(p, domain.RoleClient); err != nil {
		return domain.Application{}, err
	}
	if _, err := s.authorize(ctx, p, id); err != nil {
		return domain.Application{}, err
	}
	return s.transition(ctx, transitionReq{appID: id, to: domain.StatusCancelled, actor: domain.ActorClient, actorID: p.UserID, comment: "Заявка отозвана клиентом"})
}

// Decline — клиент отказывается от предложения банка.
func (s *Service) Decline(ctx context.Context, p auth.Principal, id, reason string) (domain.Application, error) {
	if err := requireRole(p, domain.RoleClient); err != nil {
		return domain.Application{}, err
	}
	if _, err := s.authorize(ctx, p, id); err != nil {
		return domain.Application{}, err
	}
	return s.transition(ctx, transitionReq{appID: id, to: domain.StatusDeclined, actor: domain.ActorClient, actorID: p.UserID, comment: strings.TrimSpace(reason)})
}

// Ограничения на загружаемые документы.
const MaxDocumentSize = 10 << 20

var allowedDocTypes = map[string]bool{"application/pdf": true, "image/png": true, "image/jpeg": true}

// UploadDocument прикладывает документ к заявке и отмечает запрос как выполненный.
func (s *Service) UploadDocument(ctx context.Context, p auth.Principal, id string, kind domain.DocKind, filename string, content []byte) (domain.Document, error) {
	if err := requireRole(p, domain.RoleClient); err != nil {
		return domain.Document{}, err
	}
	v := domain.NewValidation()
	if !kind.IsValid() {
		v.Add("kind", "Неизвестный тип документа")
	}
	if len(content) == 0 || len(content) > MaxDocumentSize {
		v.Add("file", "Файл должен быть непустым и не больше 10 МБ")
	}
	// Тип определяем по содержимому, а не по заголовку от клиента.
	ctype := http.DetectContentType(content)
	if !allowedDocTypes[ctype] {
		v.Add("file", "Допустимы PDF, PNG и JPEG")
	}
	if err := v.OrNil(); err != nil {
		return domain.Document{}, err
	}
	sum := sha256.Sum256(content)
	doc := domain.Document{
		ApplicationID: id, Kind: kind, Filename: sanitizeFilename(filename), ContentType: ctype,
		SizeBytes: int64(len(content)), SHA256: hex.EncodeToString(sum[:]), UploadedBy: p.UserID,
	}
	err := s.store.InTx(ctx, func(q *store.Queries) error {
		app, err := q.LockApplication(ctx, id)
		if err != nil {
			return err
		}
		if app.CompanyID != p.CompanyID {
			return domain.ErrNotFound
		}
		if !app.Editable() && app.Status != domain.StatusManualReview {
			return fmt.Errorf("%w: документы можно загрузить в черновик или по запросу банка", domain.ErrInvalidTransition)
		}
		if doc, err = q.CreateDocument(ctx, doc, content); err != nil {
			return err
		}
		app.MarkDocProvided(kind)
		_, err = q.SaveApplication(ctx, app)
		return err
	})
	return doc, err
}

func sanitizeFilename(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, name)
	if name == "" || name == "." {
		return "document"
	}
	if len(name) > 120 {
		name = name[:120]
	}
	return name
}

// SignChallenge — запрос кода для подписания оферты.
type SignChallenge struct {
	ExpiresInSec int    `json:"expires_in_sec"`
	DemoCode     string `json:"demo_code,omitempty"`
	Hint         string `json:"hint"`
}

// RequestSignCode выпускает одноразовый код подписания (ПЭП смоделирована).
func (s *Service) RequestSignCode(ctx context.Context, p auth.Principal, id string) (SignChallenge, error) {
	app, err := s.authorize(ctx, p, id)
	if err != nil {
		return SignChallenge{}, err
	}
	if err := requireRole(p, domain.RoleClient); err != nil {
		return SignChallenge{}, err
	}
	if app.Status != domain.StatusApproved && app.Status != domain.StatusCounterOffer {
		return SignChallenge{}, fmt.Errorf("%w: подписать можно только одобренное предложение", domain.ErrInvalidTransition)
	}
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return SignChallenge{}, err
	}
	code := fmt.Sprintf("%06d", n.Int64())
	if err := s.store.SetSignCode(ctx, id, hashCode(id, code), s.now().Add(s.cfg.SignCodeTTL)); err != nil {
		return SignChallenge{}, err
	}
	ch := SignChallenge{ExpiresInSec: int(s.cfg.SignCodeTTL.Seconds()),
		Hint: "Код подтверждения подписи. В промышленной версии приходит push-уведомлением в приложение банка."}
	if s.cfg.DemoSignCode {
		ch.DemoCode = code
	}
	return ch, nil
}

func hashCode(appID, code string) string {
	sum := sha256.Sum256([]byte(appID + ":" + code))
	return hex.EncodeToString(sum[:])
}

// Sign подписывает оферту кодом и ставит выдачу средств в конвейер.
func (s *Service) Sign(ctx context.Context, p auth.Principal, id, code string) (domain.Application, error) {
	if err := requireRole(p, domain.RoleClient); err != nil {
		return domain.Application{}, err
	}
	if _, err := s.authorize(ctx, p, id); err != nil {
		return domain.Application{}, err
	}
	hash, until, err := s.store.GetSignCode(ctx, id)
	if err != nil {
		return domain.Application{}, err
	}
	if hash == "" || s.now().After(until) || subtle.ConstantTimeCompare([]byte(hash), []byte(hashCode(id, strings.TrimSpace(code)))) != 1 {
		v := domain.NewValidation()
		v.Add("code", "Неверный или просроченный код — запросите новый")
		return domain.Application{}, v
	}
	app, err := s.transition(ctx, transitionReq{
		appID: id, to: domain.StatusSigned, actor: domain.ActorClient, actorID: p.UserID, task: TaskDisburse,
		comment: "Договор подписан простой электронной подписью",
	})
	if err == nil {
		_ = s.store.SetSignCode(ctx, id, "", s.now()) // код одноразовый
	}
	return app, err
}

// AllowedActions — что пользователь может сделать с заявкой сейчас (для UI).
func AllowedActions(app domain.Application, role domain.Role) []string {
	var out []string
	if role == domain.RoleClient {
		if app.Editable() {
			out = append(out, "edit", "upload_document", "submit")
		}
		if domain.CanTransition(app.Status, domain.StatusCancelled, domain.ActorClient) {
			out = append(out, "cancel")
		}
		if domain.CanTransition(app.Status, domain.StatusSigned, domain.ActorClient) {
			out = append(out, "sign", "decline")
		}
		if app.Status == domain.StatusManualReview {
			out = append(out, "upload_document")
		}
	}
	if role == domain.RoleUnderwriter && app.Status == domain.StatusManualReview {
		out = append(out, "take", "approve", "reject", "request_info")
	}
	if out == nil {
		out = []string{}
	}
	return out
}

// ApplicationDetails — заявка со всем контекстом для карточки.
type ApplicationDetails struct {
	Application domain.Application    `json:"application"`
	StatusInfo  domain.StatusInfo     `json:"status_info"`
	Product     domain.Product        `json:"product"`
	Company     domain.Company        `json:"company"`
	Events      []domain.Event        `json:"events"`
	Checks      []domain.CheckResult  `json:"checks"`
	Documents   []domain.Document     `json:"documents"`
	Schedule    []policy.ScheduleItem `json:"schedule,omitempty"`
	Actions     []string              `json:"actions"`
	Pipeline    *PipelineNotice       `json:"pipeline,omitempty"`
}

// PipelineNotice — заявка задержалась из-за сбоя внешнего источника. Клиенту
// объясняем, что происходит и что делать ничего не нужно; андеррайтеру
// дополнительно показываем техническую причину.
type PipelineNotice struct {
	State          string     `json:"state"` // retrying / failed
	Message        string     `json:"message"`
	Attempts       int        `json:"attempts"`
	MaxAttempts    int        `json:"max_attempts"`
	NextAttemptAt  *time.Time `json:"next_attempt_at,omitempty"`
	TechnicalError string     `json:"technical_error,omitempty"`
}

// pipelineNotice переводит проблемную задачу в сообщение для пользователя.
func pipelineNotice(t *store.TaskTrouble, role domain.Role) *PipelineNotice {
	if t == nil {
		return nil
	}
	n := &PipelineNotice{State: "retrying", Attempts: t.Attempts, MaxAttempts: t.MaxAttempts}
	src, many := failedSourceTitle(t.LastError)
	verb := "не отвечает"
	if many {
		verb = "не отвечают"
	}
	if t.Status == "failed" {
		n.State = "failed"
		n.Message = src + " " + verb + " дольше обычного. Заявка сохранена, специалист банка проверит её вручную — ничего делать не нужно."
	} else {
		next := t.NextRunAt
		n.NextAttemptAt = &next
		n.Message = src + " временно " + verb + ". Мы повторим запрос автоматически — ничего делать не нужно, заявка не потеряется."
	}
	if role == domain.RoleUnderwriter {
		n.TechnicalError = t.LastError
	}
	return n
}

// failedSourceTitle называет недоступные источники; many — во множественном числе.
func failedSourceTitle(errText string) (title string, many bool) {
	var names []string
	for key, title := range map[string]string{"registry:": "ЕГРЮЛ/ЕГРИП", "fns:": "ФНС", "bki:": "бюро кредитных историй"} {
		if strings.Contains(errText, key) {
			names = append(names, title)
		}
	}
	sort.Strings(names)
	switch len(names) {
	case 0:
		return "Внешний сервис", false
	case 1:
		return "Сервис " + names[0], false
	default:
		return "Сервисы " + strings.Join(names, ", "), true
	}
}

// GetDetails возвращает карточку заявки.
func (s *Service) GetDetails(ctx context.Context, p auth.Principal, id string) (ApplicationDetails, error) {
	app, err := s.authorize(ctx, p, id)
	if err != nil {
		return ApplicationDetails{}, err
	}
	d := ApplicationDetails{Application: app, StatusInfo: app.Status.Info(), Actions: AllowedActions(app, p.Role)}
	d.Product, _ = domain.ProductByCode(app.ProductCode)
	if d.Company, err = s.store.GetCompany(ctx, app.CompanyID); err != nil {
		return d, err
	}
	if d.Events, err = s.store.ListEvents(ctx, id); err != nil {
		return d, err
	}
	if d.Checks, err = s.store.ListChecks(ctx, id); err != nil {
		return d, err
	}
	if d.Documents, err = s.store.ListDocuments(ctx, id); err != nil {
		return d, err
	}
	if p.Role == domain.RoleClient {
		// Клиенту не показываем сырые ответы источников — только итог проверки.
		for i := range d.Checks {
			d.Checks[i].Payload = nil
		}
	}
	if app.Decision != nil && app.Decision.Offer != nil {
		d.Schedule = policy.Schedule(*app.Decision.Offer)
	}
	if !app.Status.IsTerminal() {
		trouble, err := s.store.LatestTrouble(ctx, id)
		if err != nil {
			return d, err
		}
		d.Pipeline = pipelineNotice(trouble, p.Role)
	}
	return d, nil
}

// GetDocument возвращает файл документа.
func (s *Service) GetDocument(ctx context.Context, p auth.Principal, appID, docID string) (domain.Document, []byte, error) {
	if _, err := s.authorize(ctx, p, appID); err != nil {
		return domain.Document{}, nil, err
	}
	return s.store.GetDocumentContent(ctx, appID, docID)
}

// List возвращает заявки: клиенту — своей компании, андеррайтеру — все по фильтру.
func (s *Service) List(ctx context.Context, p auth.Principal, f store.ListFilter) ([]domain.Application, int, error) {
	if p.Role == domain.RoleClient {
		f.CompanyID = p.CompanyID
		f.AssigneeID = ""
	}
	return s.store.ListApplications(ctx, f)
}

// Prefill — данные для автозаполнения заявки из ФНС и БКИ, чтобы клиент
// не вводил то, что банк может узнать сам.
type Prefill struct {
	Financials domain.Financials `json:"financials"`
	Sources    []string          `json:"sources"`
	Warnings   []string          `json:"warnings"`
}

// GetPrefill собирает финансовые показатели компании из внешних источников.
func (s *Service) GetPrefill(ctx context.Context, p auth.Principal) (Prefill, error) {
	if err := requireRole(p, domain.RoleClient); err != nil {
		return Prefill{}, err
	}
	c, err := s.store.GetCompany(ctx, p.CompanyID)
	if err != nil {
		return Prefill{}, err
	}
	var (
		wg     sync.WaitGroup
		fns    integrations.FNSProfile
		bki    integrations.CreditReport
		fnsErr error
		bkiErr error
	)
	wg.Add(2)
	go func() { defer wg.Done(); fns, fnsErr = s.src.FNS(ctx, c.INN) }()
	go func() { defer wg.Done(); bki, bkiErr = s.src.CreditReport(ctx, c.INN) }()
	wg.Wait()

	out := Prefill{Sources: []string{}, Warnings: []string{}}
	if fnsErr == nil {
		out.Financials = financialsFromFNS(fns)
		out.Sources = append(out.Sources, "ФНС «Прозрачный бизнес»: выручка, прибыль, численность")
	} else {
		out.Warnings = append(out.Warnings, "ФНС временно недоступна — заполните выручку вручную")
	}
	if bkiErr == nil {
		_, _, monthly, _ := bki.Summary()
		out.Financials.MonthlyObligations = monthly
		out.Sources = append(out.Sources, "Бюро кредитных историй: текущие платежи по кредитам")
	} else if !errors.Is(bkiErr, integrations.ErrNotFound) {
		out.Warnings = append(out.Warnings, "Бюро кредитных историй временно недоступно — укажите платежи по кредитам вручную")
	}
	return out, nil
}

func financialsFromFNS(f integrations.FNSProfile) domain.Financials {
	var fin domain.Financials
	fin.Employees = f.Employees
	if len(f.Reports) > 0 {
		fin.AnnualRevenue, fin.AnnualNetProfit = f.Reports[0].Revenue, f.Reports[0].NetProfit
	}
	if len(f.Reports) > 1 {
		fin.PrevAnnualRevenue = f.Reports[1].Revenue
	}
	return fin
}

// Recommend подбирает продукты под потребность.
func (s *Service) Recommend(need recommend.Need) []recommend.Recommendation {
	return recommend.Recommend(need)
}

func docTitles(docs []domain.DocRequest) string {
	t := make([]string, len(docs))
	for i, d := range docs {
		t[i] = strings.ToLower(domain.DocKindTitles[d.Kind])
	}
	return strings.Join(t, "; ")
}
