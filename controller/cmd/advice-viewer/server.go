package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"gasboat/controller/internal/advice"
	"gasboat/controller/internal/beadsapi"
)

// Server handles HTTP requests for the advice viewer.
type Server struct {
	daemon   *beadsapi.Client
	logger   *slog.Logger
	pages    map[string]*template.Template
	basePath string // external URL prefix (e.g. "/advice"), empty for root
}

// NewServer creates an advice viewer server.
func NewServer(daemon *beadsapi.Client, logger *slog.Logger, basePath string) *Server {
	funcMap := template.FuncMap{
		"join": strings.Join,
	}
	// Parse each page template together with the layout so {{define "content"}}
	// blocks don't collide across pages.
	pageNames := []string{
		"index.html", "agent.html", "advice_list.html", "advice_show.html",
		"advice_edit.html", "advice_new.html", "generate.html",
	}
	pages := make(map[string]*template.Template, len(pageNames))
	for _, name := range pageNames {
		pages[name] = template.Must(
			template.New("").Funcs(funcMap).ParseFS(templateFS, "templates/layout.html", "templates/"+name),
		)
	}
	return &Server{
		daemon:   daemon,
		logger:   logger,
		pages:    pages,
		basePath: basePath,
	}
}

// RegisterRoutes adds all application routes to the mux.
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /agent", s.handleAgent)
	mux.HandleFunc("GET /advice", s.handleAdviceList)
	mux.HandleFunc("GET /advice/new", s.handleAdviceNew)
	mux.HandleFunc("POST /advice/new", s.handleAdviceCreate)
	mux.HandleFunc("GET /advice/{id}/edit", s.handleAdviceEdit)
	mux.HandleFunc("POST /advice/{id}/edit", s.handleAdviceUpdate)
	mux.HandleFunc("GET /advice/{id}", s.handleAdviceShow)
	mux.HandleFunc("GET /generate", s.handleGenerateForm)
	mux.HandleFunc("POST /generate", s.handleGenerateDispatch)
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	tmpl, ok := s.pages[name]
	if !ok {
		s.logger.Error("template not found", "template", name)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	// Inject BasePath into all template data.
	m, _ := data.(map[string]any)
	if m == nil {
		m = make(map[string]any)
	}
	m["BasePath"] = s.basePath
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "layout", m); err != nil {
		s.logger.Error("template render error", "template", name, "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

// handleIndex lists all active agents.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	agents, err := s.daemon.ListAgentBeads(r.Context())
	if err != nil {
		s.logger.Error("listing agents", "error", err)
		http.Error(w, "Failed to list agents", http.StatusInternalServerError)
		return
	}
	sort.Slice(agents, func(i, j int) bool {
		if agents[i].Project != agents[j].Project {
			return agents[i].Project < agents[j].Project
		}
		return agents[i].AgentName < agents[j].AgentName
	})
	s.render(w, "index.html", map[string]any{
		"Agents": agents,
	})
}

// handleAgent shows advice matched for a specific agent.
func (s *Server) handleAgent(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("id")
	if agentID == "" {
		http.Error(w, "Missing agent id parameter", http.StatusBadRequest)
		return
	}

	matched, subs, err := advice.ListAdviceForAgent(r.Context(), s.daemon, agentID)
	if err != nil {
		s.logger.Error("listing advice for agent", "agent", agentID, "error", err)
		http.Error(w, "Failed to list advice", http.StatusInternalServerError)
		return
	}

	// Group by scope.
	type scopeGroup struct {
		Header string
		Scope  string
		Target string
		Items  []advice.MatchedAdvice
	}
	groupMap := make(map[string]*scopeGroup)
	for _, m := range matched {
		key := m.Scope + ":" + m.ScopeTarget
		g, ok := groupMap[key]
		if !ok {
			g = &scopeGroup{
				Header: m.ScopeHeader,
				Scope:  m.Scope,
				Target: m.ScopeTarget,
			}
			groupMap[key] = g
		}
		g.Items = append(g.Items, m)
	}
	var groups []*scopeGroup
	for _, g := range groupMap {
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool {
		return advice.GroupSortKey(groups[i].Scope, groups[i].Target) <
			advice.GroupSortKey(groups[j].Scope, groups[j].Target)
	})

	s.render(w, "agent.html", map[string]any{
		"AgentID":       agentID,
		"Groups":        groups,
		"Subscriptions": subs,
		"TotalMatched":  len(matched),
	})
}

// handleAdviceList shows all advice beads.
func (s *Server) handleAdviceList(w http.ResponseWriter, r *http.Request) {
	allAdvice, err := advice.ListAllAdvice(r.Context(), s.daemon)
	if err != nil {
		s.logger.Error("listing advice", "error", err)
		http.Error(w, "Failed to list advice", http.StatusInternalServerError)
		return
	}
	s.render(w, "advice_list.html", map[string]any{
		"Advice": allAdvice,
	})
}

// handleAdviceShow shows a single advice bead.
func (s *Server) handleAdviceShow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	bead, err := s.daemon.GetBead(r.Context(), id)
	if err != nil {
		s.logger.Error("getting advice", "id", id, "error", err)
		http.Error(w, "Advice not found", http.StatusNotFound)
		return
	}
	s.render(w, "advice_show.html", map[string]any{
		"Bead": bead,
	})
}

// handleAdviceEdit shows the edit form for an advice bead.
func (s *Server) handleAdviceEdit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	bead, err := s.daemon.GetBead(r.Context(), id)
	if err != nil {
		s.logger.Error("getting advice for edit", "id", id, "error", err)
		http.Error(w, "Advice not found", http.StatusNotFound)
		return
	}
	s.render(w, "advice_edit.html", map[string]any{
		"Bead": bead,
	})
}

// handleAdviceUpdate processes the edit form submission.
func (s *Server) handleAdviceUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	title := r.FormValue("title")
	description := r.FormValue("description")
	labelsStr := r.FormValue("labels")
	hookCommand := r.FormValue("hook_command")
	hookTrigger := r.FormValue("hook_trigger")

	// Update title and description.
	if err := s.daemon.UpdateBead(r.Context(), id, beadsapi.UpdateBeadRequest{
		Title:       &title,
		Description: &description,
	}); err != nil {
		s.logger.Error("updating advice", "id", id, "error", err)
		http.Error(w, "Failed to update advice", http.StatusInternalServerError)
		return
	}

	// Update fields — only send non-empty values; the API rejects empty
	// hook_trigger (must be one of the allowed trigger names).
	fields := make(map[string]string)
	if hookCommand != "" {
		fields["hook_command"] = hookCommand
	}
	if hookTrigger != "" {
		fields["hook_trigger"] = hookTrigger
	}
	if len(fields) > 0 {
		if err := s.daemon.UpdateBeadFields(r.Context(), id, fields); err != nil {
			s.logger.Error("updating advice fields", "id", id, "error", err)
			http.Error(w, "Failed to update advice fields", http.StatusInternalServerError)
			return
		}
	}

	// Sync labels: get current labels, compute diff, add/remove.
	bead, err := s.daemon.GetBead(r.Context(), id)
	if err == nil {
		newLabels := parseLabelsString(labelsStr)
		oldSet := make(map[string]bool)
		for _, l := range bead.Labels {
			oldSet[l] = true
		}
		newSet := make(map[string]bool)
		for _, l := range newLabels {
			newSet[l] = true
		}
		for _, l := range newLabels {
			if !oldSet[l] {
				_ = s.daemon.AddLabel(r.Context(), id, l)
			}
		}
		for _, l := range bead.Labels {
			if !newSet[l] {
				_ = s.daemon.RemoveLabel(r.Context(), id, l)
			}
		}
	}

	http.Redirect(w, r, s.basePath+"/advice/"+id, http.StatusSeeOther)
}

// handleAdviceNew shows the create form.
func (s *Server) handleAdviceNew(w http.ResponseWriter, r *http.Request) {
	s.render(w, "advice_new.html", nil)
}

// handleAdviceCreate processes the create form submission.
func (s *Server) handleAdviceCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	title := r.FormValue("title")
	description := r.FormValue("description")
	labelsStr := r.FormValue("labels")
	hookCommand := r.FormValue("hook_command")
	hookTrigger := r.FormValue("hook_trigger")
	rig := r.FormValue("rig")
	role := r.FormValue("role")
	agent := r.FormValue("agent")

	labels := parseLabelsString(labelsStr)

	// Add targeting labels.
	var targeting []string
	if rig != "" {
		targeting = append(targeting, "rig:"+rig)
	}
	if role != "" {
		targeting = append(targeting, "role:"+role)
	}
	if agent != "" {
		targeting = append(targeting, "agent:"+agent)
	}
	if len(targeting) > 1 {
		for _, l := range targeting {
			labels = append(labels, "g0:"+l)
		}
	} else {
		labels = append(labels, targeting...)
	}

	if !advice.HasTargetingLabel(labels) {
		labels = append(labels, "global")
	}

	fields := make(map[string]any)
	if hookCommand != "" {
		fields["hook_command"] = hookCommand
	}
	if hookTrigger != "" {
		fields["hook_trigger"] = hookTrigger
	}

	var fieldsJSON json.RawMessage
	if len(fields) > 0 {
		b, _ := json.Marshal(fields)
		fieldsJSON = b
	}

	id, err := s.daemon.CreateBead(r.Context(), beadsapi.CreateBeadRequest{
		Title:       title,
		Description: description,
		Type:        "advice",
		Labels:      labels,
		CreatedBy:   "advice-viewer",
		Fields:      fieldsJSON,
	})
	if err != nil {
		s.logger.Error("creating advice", "error", err)
		http.Error(w, "Failed to create advice", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, s.basePath+"/advice/"+id, http.StatusSeeOther)
}

// handleGenerateForm shows the generation dispatch form.
func (s *Server) handleGenerateForm(w http.ResponseWriter, r *http.Request) {
	agents, err := s.daemon.ListAgentBeads(r.Context())
	if err != nil {
		s.logger.Error("listing agents for generate", "error", err)
	}
	s.render(w, "generate.html", map[string]any{
		"Agents": agents,
	})
}

// handleGenerateDispatch creates a task bead and spawns an agent to work on it.
func (s *Server) handleGenerateDispatch(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	topic := r.FormValue("topic")
	project := r.FormValue("project")

	if topic == "" {
		http.Error(w, "Topic is required", http.StatusBadRequest)
		return
	}
	if project == "" {
		http.Error(w, "Project is required", http.StatusBadRequest)
		return
	}

	labels := []string{"advice-generation", "project:" + project}

	title := fmt.Sprintf("Generate advice: %s", topic)
	if len(title) > 200 {
		title = title[:200]
	}

	taskID, err := s.daemon.CreateBead(r.Context(), beadsapi.CreateBeadRequest{
		Title:       title,
		Description: topic,
		Type:        "task",
		Labels:      labels,
		CreatedBy:   "advice-viewer",
	})
	if err != nil {
		s.logger.Error("creating generation task", "error", err)
		http.Error(w, "Failed to create generation task", http.StatusInternalServerError)
		return
	}

	// Derive a unique agent name from the task bead ID.
	agentName := "advgen"
	if len(taskID) > 3 {
		agentName = "advgen-" + taskID[3:] // strip "kd-" prefix
	}

	agentID, err := s.daemon.SpawnAgent(r.Context(), agentName, project, taskID, "crew", "")
	if err != nil {
		s.logger.Error("spawning agent for generation", "task", taskID, "error", err)
		s.render(w, "generate.html", map[string]any{
			"Success": fmt.Sprintf("Created task %s but failed to spawn agent: %v", taskID, err),
		})
		return
	}

	s.render(w, "generate.html", map[string]any{
		"Success": fmt.Sprintf("Created task %s and spawned agent %s (%s)", taskID, agentName, agentID),
	})
}

// parseLabelsString splits a comma-separated label string into a slice.
func parseLabelsString(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	var labels []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			labels = append(labels, p)
		}
	}
	return labels
}
