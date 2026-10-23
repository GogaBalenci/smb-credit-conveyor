package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"alfahack/backend/internal/domain"
	"alfahack/backend/internal/recommend"
	"alfahack/backend/internal/service"
	"alfahack/backend/internal/store"
)

func (s *Server) meta(w http.ResponseWriter, r *http.Request) {
	statuses := make([]domain.StatusInfo, 0, len(domain.AllStatuses))
	for _, st := range domain.AllStatuses {
		statuses = append(statuses, st.Info())
	}
	docKinds := []map[string]string{}
	for _, k := range []domain.DocKind{domain.DocFinancialStatements, domain.DocBankStatement, domain.DocCollateral, domain.DocContract, domain.DocOther} {
		docKinds = append(docKinds, map[string]string{"code": string(k), "title": domain.DocKindTitles[k]})
	}
	out := map[string]any{
		"products":    domain.Products,
		"purposes":    domain.Purposes,
		"statuses":    statuses,
		"transitions": domain.Transitions(),
		"doc_kinds":   docKinds,
	}
	if s.cfg.DemoMode {
		out["demo_accounts"] = service.DemoAccounts
		out["demo_password"] = service.DemoPassword
	}
	writeJSON(w, 200, out)
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var in service.RegisterInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, r, err)
		return
	}
	sess, err := s.svc.Register(r.Context(), in)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, sess)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, r, err)
		return
	}
	sess, err := s.svc.Login(r.Context(), in.Email, in.Password)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, 200, sess)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	p, _ := principal(r.Context())
	u, c, err := s.svc.Me(r.Context(), p)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"user": u, "company": c})
}

func (s *Server) lookupCompany(w http.ResponseWriter, r *http.Request) {
	c, err := s.svc.LookupCompany(r.Context(), r.URL.Query().Get("inn"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, 200, c)
}

func (s *Server) recommend(w http.ResponseWriter, r *http.Request) {
	var need recommend.Need
	if err := decodeJSON(r, &need); err != nil {
		writeError(w, r, err)
		return
	}
	if need.Purpose != "" && !need.Purpose.IsValid() {
		v := domain.NewValidation()
		v.Add("purpose", "Неизвестная цель")
		writeError(w, r, v)
		return
	}
	writeJSON(w, 200, map[string]any{"items": s.svc.Recommend(need)})
}

func (s *Server) prefill(w http.ResponseWriter, r *http.Request) {
	p, _ := principal(r.Context())
	out, err := s.svc.GetPrefill(r.Context(), p)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) feed(w http.ResponseWriter, r *http.Request) {
	p, _ := principal(r.Context())
	out, err := s.svc.GetFeed(r.Context(), p)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) listApplications(w http.ResponseWriter, r *http.Request) {
	p, _ := principal(r.Context())
	q := r.URL.Query()
	f := store.ListFilter{Search: q.Get("q")}
	f.Limit, _ = strconv.Atoi(q.Get("limit"))
	f.Offset, _ = strconv.Atoi(q.Get("offset"))
	if st := q.Get("status"); st != "" {
		for _, x := range strings.Split(st, ",") {
			status := domain.Status(strings.TrimSpace(x))
			if !status.IsValid() {
				writeError(w, r, badRequest("неизвестный статус "+x))
				return
			}
			f.Statuses = append(f.Statuses, status)
		}
	}
	if q.Get("assignee") == "me" {
		f.AssigneeID = p.UserID
	}
	items, total, err := s.svc.List(r.Context(), p, f)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": total})
}

func (s *Server) createApplication(w http.ResponseWriter, r *http.Request) {
	p, _ := principal(r.Context())
	var in domain.ApplicationInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, r, err)
		return
	}
	app, err := s.svc.CreateDraft(r.Context(), p, in)
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/applications/"+app.ID)
	writeJSON(w, http.StatusCreated, app)
}

func (s *Server) getApplication(w http.ResponseWriter, r *http.Request) {
	p, _ := principal(r.Context())
	d, err := s.svc.GetDetails(r.Context(), p, r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, d.Application.Version))
	writeJSON(w, 200, d)
}

func (s *Server) updateApplication(w http.ResponseWriter, r *http.Request) {
	p, _ := principal(r.Context())
	var in struct {
		domain.ApplicationInput
		Version int `json:"version"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, r, err)
		return
	}
	version := in.Version
	if im := strings.Trim(r.Header.Get("If-Match"), `"`); im != "" {
		version, _ = strconv.Atoi(im)
	}
	app, err := s.svc.UpdateDraft(r.Context(), p, r.PathValue("id"), version, in.ApplicationInput)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, 200, app)
}

func (s *Server) submit(w http.ResponseWriter, r *http.Request) {
	p, _ := principal(r.Context())
	app, err := s.svc.Submit(r.Context(), p, r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, 200, app)
}

func (s *Server) cancel(w http.ResponseWriter, r *http.Request) {
	p, _ := principal(r.Context())
	app, err := s.svc.Cancel(r.Context(), p, r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, 200, app)
}

func (s *Server) uploadDocument(w http.ResponseWriter, r *http.Request) {
	p, _ := principal(r.Context())
	r.Body = http.MaxBytesReader(w, r.Body, service.MaxDocumentSize+1<<20)
	if err := r.ParseMultipartForm(2 << 20); err != nil {
		writeError(w, r, badRequest("ожидается multipart/form-data с полями kind и file (до 10 МБ)"))
		return
	}
	defer r.MultipartForm.RemoveAll()
	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeError(w, r, badRequest("не передан файл"))
		return
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, service.MaxDocumentSize+1))
	if err != nil {
		writeError(w, r, badRequest("не удалось прочитать файл"))
		return
	}
	doc, err := s.svc.UploadDocument(r.Context(), p, r.PathValue("id"), domain.DocKind(r.FormValue("kind")), hdr.Filename, content)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, doc)
}

func (s *Server) downloadDocument(w http.ResponseWriter, r *http.Request) {
	p, _ := principal(r.Context())
	doc, content, err := s.svc.GetDocument(r.Context(), p, r.PathValue("id"), r.PathValue("docId"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", doc.ContentType)
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(doc.Filename))
	_, _ = w.Write(content)
}

func (s *Server) signCode(w http.ResponseWriter, r *http.Request) {
	p, _ := principal(r.Context())
	ch, err := s.svc.RequestSignCode(r.Context(), p, r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, 200, ch)
}

func (s *Server) sign(w http.ResponseWriter, r *http.Request) {
	p, _ := principal(r.Context())
	var in struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, r, err)
		return
	}
	app, err := s.svc.Sign(r.Context(), p, r.PathValue("id"), in.Code)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, 200, app)
}

func (s *Server) decline(w http.ResponseWriter, r *http.Request) {
	p, _ := principal(r.Context())
	var in struct {
		Reason string `json:"reason"`
	}
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &in); err != nil {
			writeError(w, r, err)
			return
		}
	}
	app, err := s.svc.Decline(r.Context(), p, r.PathValue("id"), in.Reason)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, 200, app)
}

func (s *Server) take(w http.ResponseWriter, r *http.Request) {
	p, _ := principal(r.Context())
	app, err := s.svc.Take(r.Context(), p, r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, 200, app)
}

func (s *Server) decision(w http.ResponseWriter, r *http.Request) {
	p, _ := principal(r.Context())
	var in service.UnderwriterDecision
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, r, err)
		return
	}
	app, err := s.svc.Decide(r.Context(), p, r.PathValue("id"), in)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, 200, app)
}

// summary — сводка досье для эксперта (модель с открытыми весами или шаблон).
func (s *Server) summary(w http.ResponseWriter, r *http.Request) {
	p, _ := principal(r.Context())
	sum, err := s.svc.Summary(r.Context(), p, r.PathValue("id"), r.URL.Query().Get("refresh") == "1")
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, 200, sum)
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	p, _ := principal(r.Context())
	st, err := s.svc.GetStats(r.Context(), p)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, 200, st)
}

// faults проксирует управление сбоями симулятора (только андеррайтер, только для демо).
func (s *Server) faults(w http.ResponseWriter, r *http.Request) {
	p, _ := principal(r.Context())
	if p.Role != domain.RoleUnderwriter {
		writeError(w, r, domain.ErrForbidden)
		return
	}
	if s.cfg.MockAdminURL == "" {
		writeError(w, r, domain.ErrNotFound)
		return
	}
	var body io.Reader
	if r.Method == http.MethodPost {
		var in map[string]any
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in); err != nil {
			writeError(w, r, badRequest("некорректный JSON"))
			return
		}
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, _ := http.NewRequestWithContext(r.Context(), r.Method, s.cfg.MockAdminURL+"/admin/faults", body)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		writeError(w, r, service.ErrSourceUnavailable)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 4096))
}
