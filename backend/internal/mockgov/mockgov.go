// Package mockgov — симулятор внешних источников: ЕГРЮЛ/ЕГРИП, ФНС
// «Прозрачный бизнес» и бюро кредитных историй.
//
// Решение должно работать автономно, без доступа к системам банка и платным API
// (кейс, ограничения 02 и 04). Симулятор отдаёт синтетические данные:
//   - фиксированные «персоны» из fixtures/companies.json — под каждый исход конвейера;
//   - для любого другого валидного ИНН — правдоподобный профиль, детерминированно
//     сгенерированный из ИНН (один и тот же ИНН всегда даёт одни и те же данные).
//
// Поддерживается внесение сбоев (задержка, доля ошибок 503) — чтобы показать
// ретраи, circuit breaker и устойчивость конвейера.
package mockgov

import (
	"embed"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math/rand"
	"net/http"
	"strconv"
	"sync"
	"time"

	"alfahack/backend/internal/domain"
	"alfahack/backend/internal/integrations"
)

//go:embed fixtures/companies.json
var fixturesFS embed.FS

type fixtureReport struct {
	YearsAgo  int   `json:"years_ago"`
	Revenue   int64 `json:"revenue"`
	NetProfit int64 `json:"net_profit"`
}

type fixture struct {
	Persona             string `json:"persona"`
	INN                 string `json:"inn"`
	OGRN                string `json:"ogrn"`
	FullName            string `json:"full_name"`
	ShortName           string `json:"short_name"`
	Status              string `json:"status"`
	RegisteredMonthsAgo int    `json:"registered_months_ago"`
	OKVED               string `json:"okved"`
	OKVEDName           string `json:"okved_name"`
	Region              string `json:"region"`
	Address             string `json:"address"`
	Director            string `json:"director"`
	FNS                 struct {
		Employees            int             `json:"employees"`
		TaxRegime            string          `json:"tax_regime"`
		Reports              []fixtureReport `json:"reports"`
		TaxArrears           int64           `json:"tax_arrears"`
		MassAddress          bool            `json:"mass_address"`
		DisqualifiedDirector bool            `json:"disqualified_director"`
		Bankruptcy           bool            `json:"bankruptcy"`
	} `json:"fns"`
	BKI struct {
		Found        bool                `json:"found"`
		Inquiries30d int                 `json:"inquiries_30d"`
		Loans        []integrations.Loan `json:"loans"`
	} `json:"bki"`
}

// Persona — краткое описание демо-компании (для README и UI подсказок).
type Persona struct {
	INN         string `json:"inn"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Data — хранилище синтетических данных.
type Data struct {
	now      func() time.Time
	fixtures map[string]fixture
	order    []string
}

// LoadData загружает фикстуры.
func LoadData(now func() time.Time) (*Data, error) {
	raw, err := fixturesFS.ReadFile("fixtures/companies.json")
	if err != nil {
		return nil, err
	}
	var list []fixture
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("parse fixtures: %w", err)
	}
	d := &Data{now: now, fixtures: map[string]fixture{}}
	for _, f := range list {
		if !domain.ValidINN(f.INN) {
			return nil, fmt.Errorf("fixture %s: invalid INN checksum", f.INN)
		}
		d.fixtures[f.INN] = f
		d.order = append(d.order, f.INN)
	}
	return d, nil
}

// Personas — список демо-компаний.
func (d *Data) Personas() []Persona {
	out := make([]Persona, 0, len(d.order))
	for _, inn := range d.order {
		f := d.fixtures[inn]
		out = append(out, Persona{INN: inn, Name: f.ShortName, Description: f.Persona})
	}
	return out
}

func (d *Data) get(inn string) fixture {
	if f, ok := d.fixtures[inn]; ok {
		return f
	}
	return d.generate(inn)
}

// Registry возвращает выписку ЕГРЮЛ/ЕГРИП.
func (d *Data) Registry(inn string) integrations.RegistryRecord {
	f := d.get(inn)
	return integrations.RegistryRecord{
		INN: f.INN, OGRN: f.OGRN, Kind: string(domain.INNKind(f.INN)),
		FullName: f.FullName, ShortName: f.ShortName, Status: f.Status,
		RegisteredAt: d.now().AddDate(0, -f.RegisteredMonthsAgo, -3).Format("2006-01-02"),
		OKVED:        f.OKVED, OKVEDName: f.OKVEDName, Region: f.Region, Address: f.Address, Director: f.Director,
	}
}

// FNS возвращает профиль «Прозрачный бизнес».
func (d *Data) FNS(inn string) integrations.FNSProfile {
	f := d.get(inn)
	year := d.now().Year()
	reports := make([]integrations.FinReport, 0, len(f.FNS.Reports))
	for _, r := range f.FNS.Reports {
		reports = append(reports, integrations.FinReport{Year: year - r.YearsAgo, Revenue: r.Revenue, NetProfit: r.NetProfit})
	}
	return integrations.FNSProfile{
		INN: f.INN, Employees: f.FNS.Employees, TaxRegime: f.FNS.TaxRegime, Reports: reports,
		TaxArrears: f.FNS.TaxArrears, MassAddress: f.FNS.MassAddress,
		DisqualifiedDirector: f.FNS.DisqualifiedDirector, Bankruptcy: f.FNS.Bankruptcy,
	}
}

// CreditReport возвращает отчёт БКИ.
func (d *Data) CreditReport(inn string) integrations.CreditReport {
	f := d.get(inn)
	loans := f.BKI.Loans
	if loans == nil {
		loans = []integrations.Loan{}
	}
	return integrations.CreditReport{INN: f.INN, Found: f.BKI.Found, Loans: loans, Inquiries30d: f.BKI.Inquiries30d}
}

var (
	genNames   = []string{"Вектор", "Меридиан", "Альтаир", "Гранит", "Луч", "Импульс", "Полюс", "Сфера", "Ладья", "Кедр", "Прайм", "Орион"}
	genFirst   = []string{"Иван", "Елена", "Алексей", "Ольга", "Михаил", "Наталья", "Сергей", "Татьяна"}
	genLast    = []string{"Смирнов", "Иванова", "Попов", "Васильева", "Новиков", "Фёдорова", "Морозов", "Волкова"}
	genRegions = []string{"Москва", "Санкт-Петербург", "Свердловская область", "Краснодарский край", "Нижегородская область", "Самарская область"}
	genOKVED   = []struct{ code, name string }{
		{"47.19", "Торговля розничная прочая в неспециализированных магазинах"},
		{"46.90", "Торговля оптовая неспециализированная"},
		{"62.02", "Деятельность консультативная в области компьютерных технологий"},
		{"49.41", "Деятельность автомобильного грузового транспорта"},
		{"56.10", "Деятельность ресторанов"},
		{"25.62", "Обработка металлических изделий механическая"},
		{"96.02", "Предоставление услуг парикмахерскими и салонами красоты"},
		{"43.31", "Производство штукатурных работ"},
	}
)

// generate строит правдоподобный и детерминированный профиль по ИНН.
func (d *Data) generate(inn string) fixture {
	h := fnv.New64a()
	_, _ = h.Write([]byte(inn))
	r := rand.New(rand.NewSource(int64(h.Sum64())))

	var f fixture
	f.INN = inn
	f.Status = "ACTIVE"
	f.RegisteredMonthsAgo = 14 + r.Intn(110)
	o := genOKVED[r.Intn(len(genOKVED))]
	f.OKVED, f.OKVEDName = o.code, o.name
	f.Region = genRegions[r.Intn(len(genRegions))]
	first, last := genFirst[r.Intn(len(genFirst))], genLast[r.Intn(len(genLast))]
	f.Director = last + " " + first
	if domain.INNKind(inn) == domain.CompanyIndividual {
		f.FullName = "Индивидуальный предприниматель " + f.Director
		f.ShortName = "ИП " + f.Director
		f.OGRN = "3" + inn[:12] + "45"
	} else {
		name := genNames[r.Intn(len(genNames))]
		f.FullName = "Общество с ограниченной ответственностью «" + name + "»"
		f.ShortName = "ООО «" + name + "»"
		f.OGRN = "1" + inn[:10] + "12"
	}
	f.Address = f.Region + ", ул. Синтетическая, д. " + strconv.Itoa(1+r.Intn(150))

	revenue := int64(6+r.Intn(90)) * 1_000_000
	prev := revenue * int64(80+r.Intn(40)) / 100
	margin := int64(2 + r.Intn(14))
	f.FNS.Employees = 3 + r.Intn(60)
	f.FNS.TaxRegime = []string{"УСН", "ОСН"}[r.Intn(2)]
	f.FNS.Reports = []fixtureReport{
		{YearsAgo: 1, Revenue: revenue, NetProfit: revenue * margin / 100},
		{YearsAgo: 2, Revenue: prev, NetProfit: prev * margin / 100},
	}
	f.FNS.MassAddress = r.Intn(20) == 0

	f.BKI.Found = r.Intn(5) > 0
	f.BKI.Inquiries30d = r.Intn(4)
	if f.BKI.Found {
		for i := 0; i < 1+r.Intn(3); i++ {
			amount := int64(1+r.Intn(10)) * 500_000
			active := i == 0 && r.Intn(2) == 0
			l := integrations.Loan{
				Lender: "Банк " + string(rune('А'+r.Intn(6))), Kind: "Кредит", Amount: amount,
				OpenedAt: d.now().AddDate(-1-r.Intn(4), 0, 0).Format("2006-01-02"), Status: "CLOSED",
			}
			if r.Intn(8) == 0 {
				l.MaxOverdueDays = 5 + r.Intn(20)
			}
			if active {
				l.Status = "ACTIVE"
				l.Outstanding = amount / 2
				l.MonthlyPayment = amount / 24
			} else {
				l.ClosedAt = d.now().AddDate(0, -r.Intn(12)-1, 0).Format("2006-01-02")
			}
			f.BKI.Loans = append(f.BKI.Loans, l)
		}
	}
	return f
}

// Faults — параметры внесения сбоев; меняются на лету через /admin/faults.
type Faults struct {
	mu          sync.RWMutex
	FailureRate float64 `json:"failure_rate"` // 0..1 — доля ответов 503
	LatencyMS   int     `json:"latency_ms"`   // базовая задержка
}

func (f *Faults) get() (float64, int) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.FailureRate, f.LatencyMS
}

// Server — HTTP-обработчики симулятора.
type Server struct {
	data   *Data
	faults *Faults
	log    *slog.Logger
	rnd    *rand.Rand
	rndMu  sync.Mutex
}

// NewServer создаёт сервер симулятора.
func NewServer(data *Data, faults *Faults, log *slog.Logger) *Server {
	return &Server{data: data, faults: faults, log: log, rnd: rand.New(rand.NewSource(time.Now().UnixNano()))}
}

// Handler возвращает маршруты.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /personas", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, s.data.Personas()) })
	mux.Handle("GET /registry/{inn}", s.source(func(inn string) any { return s.data.Registry(inn) }))
	mux.Handle("GET /fns/{inn}", s.source(func(inn string) any { return s.data.FNS(inn) }))
	mux.Handle("GET /bki/{inn}", s.source(func(inn string) any { return s.data.CreditReport(inn) }))
	mux.HandleFunc("GET /admin/faults", func(w http.ResponseWriter, _ *http.Request) {
		rate, lat := s.faults.get()
		writeJSON(w, 200, map[string]any{"failure_rate": rate, "latency_ms": lat})
	})
	mux.HandleFunc("POST /admin/faults", s.setFaults)
	return mux
}

func (s *Server) source(fn func(inn string) any) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inn := r.PathValue("inn")
		rate, latency := s.faults.get()
		s.rndMu.Lock()
		jitter, roll := s.rnd.Intn(latency/2+1), s.rnd.Float64()
		s.rndMu.Unlock()
		select {
		case <-time.After(time.Duration(latency+jitter) * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		if roll < rate {
			s.log.Info("injected failure", "path", r.URL.Path, "request_id", r.Header.Get("X-Request-ID"))
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "source temporarily unavailable (injected)"})
			return
		}
		if !domain.ValidINN(inn) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "subject not found"})
			return
		}
		writeJSON(w, http.StatusOK, fn(inn))
	})
}

func (s *Server) setFaults(w http.ResponseWriter, r *http.Request) {
	var in struct {
		FailureRate *float64 `json:"failure_rate"`
		LatencyMS   *int     `json:"latency_ms"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid json"})
		return
	}
	s.faults.mu.Lock()
	if in.FailureRate != nil && *in.FailureRate >= 0 && *in.FailureRate <= 1 {
		s.faults.FailureRate = *in.FailureRate
	}
	if in.LatencyMS != nil && *in.LatencyMS >= 0 && *in.LatencyMS <= 10_000 {
		s.faults.LatencyMS = *in.LatencyMS
	}
	rate, lat := s.faults.FailureRate, s.faults.LatencyMS
	s.faults.mu.Unlock()
	s.log.Info("faults updated", "failure_rate", rate, "latency_ms", lat)
	writeJSON(w, 200, map[string]any{"failure_rate": rate, "latency_ms": lat})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
