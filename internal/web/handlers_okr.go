package web

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/bhs/mendelbuild/internal/agent"
	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// OKREditorView holds data for the OKR editor page.
type OKREditorView struct {
	Project      *domain.Project
	Strategy     *domain.Strategy
	Objectives   []ObjectiveTreeView
	Breadcrumbs  []domain.Objective   // Ancestor chain for nested views
	ParentID     *uuid.UUID           // Current parent (nil for root view)
	AvailableKRs []domain.KeyResult   // KRs available to link (for objective detail view)
}

// ObjectiveTreeView holds an objective with its key results for display.
type ObjectiveTreeView struct {
	Objective    domain.Objective
	KeyResults   []domain.KeyResult
	ChildCount   int  // Number of child objectives
	HasUntuned   bool // Has any untuned items
}

func (s *Server) handleOKREditor(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	project, err := s.db.GetProject(ctx, projectID)
	if err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}

	strategies, err := s.db.GetStrategiesByProject(ctx, projectID)
	if err != nil || len(strategies) == 0 {
		http.Error(w, "no strategy found", http.StatusNotFound)
		return
	}
	strategy := strategies[0]

	// Get root objectives
	objectives, err := s.db.GetRootObjectives(ctx, strategy.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Build objective views
	objViews := make([]ObjectiveTreeView, 0, len(objectives))
	for _, obj := range objectives {
		krs, _ := s.db.GetKeyResultsByObjective(ctx, obj.ID)
		children, _ := s.db.GetObjectivesByParent(ctx, obj.ID)

		hasUntuned := obj.TuneScore == nil
		for _, kr := range krs {
			if kr.TuneScore == nil {
				hasUntuned = true
				break
			}
		}

		objViews = append(objViews, ObjectiveTreeView{
			Objective:  obj,
			KeyResults: krs,
			ChildCount: len(children),
			HasUntuned: hasUntuned,
		})
	}

	data := map[string]interface{}{
		"Title":     "Edit OKRs",
		"ProjectID": projectID,
		"OKR": OKREditorView{
			Project:    project,
			Strategy:   &strategy,
			Objectives: objViews,
			ParentID:   nil,
		},
	}

	if err := renderPage(w, "okr_editor.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleObjectiveDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	objectiveID, err := uuid.Parse(chi.URLParam(r, "objectiveID"))
	if err != nil {
		http.Error(w, "invalid objective ID", http.StatusBadRequest)
		return
	}

	project, err := s.db.GetProject(ctx, projectID)
	if err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}

	objective, err := s.db.GetObjective(ctx, objectiveID)
	if err != nil {
		http.Error(w, "objective not found", http.StatusNotFound)
		return
	}

	strategy, err := s.db.GetStrategy(ctx, objective.StrategyID)
	if err != nil {
		http.Error(w, "strategy not found", http.StatusNotFound)
		return
	}

	// Get breadcrumbs (ancestors)
	ancestors, _ := s.db.GetObjectiveAncestors(ctx, objectiveID)

	// Get child objectives
	children, err := s.db.GetObjectivesByParent(ctx, objectiveID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Build child objective views
	objViews := make([]ObjectiveTreeView, 0, len(children))
	for _, obj := range children {
		krs, _ := s.db.GetKeyResultsByObjective(ctx, obj.ID)
		grandchildren, _ := s.db.GetObjectivesByParent(ctx, obj.ID)

		hasUntuned := obj.TuneScore == nil
		for _, kr := range krs {
			if kr.TuneScore == nil {
				hasUntuned = true
				break
			}
		}

		objViews = append(objViews, ObjectiveTreeView{
			Objective:  obj,
			KeyResults: krs,
			ChildCount: len(grandchildren),
			HasUntuned: hasUntuned,
		})
	}

	// Get key results for this objective
	krs, _ := s.db.GetKeyResultsByObjective(ctx, objectiveID)

	// Build the current objective view (for display)
	currentObjView := ObjectiveTreeView{
		Objective:  *objective,
		KeyResults: krs,
		ChildCount: len(children),
		HasUntuned: objective.TuneScore == nil,
	}
	for _, kr := range krs {
		if kr.TuneScore == nil {
			currentObjView.HasUntuned = true
			break
		}
	}

	// Get available KRs to link (same strategy, not already linked to this objective)
	availableKRs, _ := s.db.GetAvailableKeyResultsForObjective(ctx, objectiveID)

	data := map[string]interface{}{
		"Title":            "Edit OKRs - " + truncateString(objective.Description, 50),
		"ProjectID":        projectID,
		"CurrentObjective": currentObjView,
		"OKR": OKREditorView{
			Project:      project,
			Strategy:     strategy,
			Objectives:   objViews,
			Breadcrumbs:  ancestors,
			ParentID:     &objectiveID,
			AvailableKRs: availableKRs,
		},
	}

	if err := renderPage(w, "okr_editor.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// truncateString truncates a string to maxLen characters, adding "..." if truncated.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func (s *Server) handleCreateObjective(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form data", http.StatusBadRequest)
		return
	}

	description := r.FormValue("description")
	if description == "" {
		http.Error(w, "description is required", http.StatusBadRequest)
		return
	}

	// Get strategy ID
	strategies, err := s.db.GetStrategiesByProject(ctx, projectID)
	if err != nil || len(strategies) == 0 {
		http.Error(w, "no strategy found", http.StatusNotFound)
		return
	}
	strategyID := strategies[0].ID

	// Parse optional parent ID
	var parentID *uuid.UUID
	if pid := r.FormValue("parent_id"); pid != "" {
		parsed, err := uuid.Parse(pid)
		if err == nil {
			parentID = &parsed
		}
	}

	obj := &domain.Objective{
		StrategyID:  strategyID,
		ParentID:    parentID,
		Description: description,
	}

	if err := s.db.CreateObjective(ctx, obj); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Redirect back to parent view or root
	if parentID != nil {
		http.Redirect(w, r, "/p/"+projectID.String()+"/okr/objectives/"+parentID.String(), http.StatusFound)
	} else {
		http.Redirect(w, r, "/p/"+projectID.String()+"/okr", http.StatusFound)
	}
}

func (s *Server) handleUpdateObjective(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	objectiveID, err := uuid.Parse(chi.URLParam(r, "objectiveID"))
	if err != nil {
		http.Error(w, "invalid objective ID", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form data", http.StatusBadRequest)
		return
	}

	description := r.FormValue("description")
	if description == "" {
		http.Error(w, "description is required", http.StatusBadRequest)
		return
	}

	obj, err := s.db.GetObjective(ctx, objectiveID)
	if err != nil {
		http.Error(w, "objective not found", http.StatusNotFound)
		return
	}

	obj.Description = description

	if err := s.db.UpdateObjective(ctx, obj); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Redirect back to parent view or root
	if obj.ParentID != nil {
		http.Redirect(w, r, "/p/"+projectID.String()+"/okr/objectives/"+obj.ParentID.String(), http.StatusFound)
	} else {
		http.Redirect(w, r, "/p/"+projectID.String()+"/okr", http.StatusFound)
	}
}

func (s *Server) handleDeleteObjective(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	objectiveID, err := uuid.Parse(chi.URLParam(r, "objectiveID"))
	if err != nil {
		http.Error(w, "invalid objective ID", http.StatusBadRequest)
		return
	}

	obj, err := s.db.GetObjective(ctx, objectiveID)
	if err != nil {
		http.Error(w, "objective not found", http.StatusNotFound)
		return
	}

	parentID := obj.ParentID

	if err := s.db.SoftDeleteObjective(ctx, objectiveID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Redirect back to parent view or root
	if parentID != nil {
		http.Redirect(w, r, "/p/"+projectID.String()+"/okr/objectives/"+parentID.String(), http.StatusFound)
	} else {
		http.Redirect(w, r, "/p/"+projectID.String()+"/okr", http.StatusFound)
	}
}

func (s *Server) handleCreateKeyResult(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form data", http.StatusBadRequest)
		return
	}

	description := r.FormValue("description")
	targetUnits := r.FormValue("target_units")
	objectiveIDStr := r.FormValue("objective_id")

	if description == "" || targetUnits == "" {
		http.Error(w, "description and target_units are required", http.StatusBadRequest)
		return
	}

	// Get strategy ID
	strategies, err := s.db.GetStrategiesByProject(ctx, projectID)
	if err != nil || len(strategies) == 0 {
		http.Error(w, "no strategy found", http.StatusNotFound)
		return
	}
	strategyID := strategies[0].ID

	// Parse optional target date
	var targetDate *time.Time
	if td := r.FormValue("target_date"); td != "" {
		parsed, err := time.Parse("2006-01-02", td)
		if err == nil {
			targetDate = &parsed
		}
	}

	kr := &domain.KeyResult{
		StrategyID:  strategyID,
		Description: description,
		TargetUnits: targetUnits,
		TargetDate:  targetDate,
	}

	if err := s.db.CreateKeyResult(ctx, kr); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Link to objective if provided
	if objectiveIDStr != "" {
		objectiveID, err := uuid.Parse(objectiveIDStr)
		if err == nil {
			s.db.LinkKeyResultToObjective(ctx, objectiveID, kr.ID)
		}
	}

	// Redirect back
	if objectiveIDStr != "" {
		http.Redirect(w, r, "/p/"+projectID.String()+"/okr/objectives/"+objectiveIDStr, http.StatusFound)
	} else {
		http.Redirect(w, r, "/p/"+projectID.String()+"/okr", http.StatusFound)
	}
}

func (s *Server) handleUpdateKeyResult(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	keyResultID, err := uuid.Parse(chi.URLParam(r, "keyResultID"))
	if err != nil {
		http.Error(w, "invalid key result ID", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form data", http.StatusBadRequest)
		return
	}

	description := r.FormValue("description")
	targetUnits := r.FormValue("target_units")

	if description == "" || targetUnits == "" {
		http.Error(w, "description and target_units are required", http.StatusBadRequest)
		return
	}

	kr, err := s.db.GetKeyResult(ctx, keyResultID)
	if err != nil {
		http.Error(w, "key result not found", http.StatusNotFound)
		return
	}

	kr.Description = description
	kr.TargetUnits = targetUnits

	// Parse optional target date
	if td := r.FormValue("target_date"); td != "" {
		parsed, err := time.Parse("2006-01-02", td)
		if err == nil {
			kr.TargetDate = &parsed
		}
	} else {
		kr.TargetDate = nil
	}

	if err := s.db.UpdateKeyResult(ctx, kr); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Get redirect target from referrer or default to root
	referer := r.FormValue("redirect")
	if referer == "" {
		referer = "/p/" + projectID.String() + "/okr"
	}
	http.Redirect(w, r, referer, http.StatusFound)
}

func (s *Server) handleDeleteKeyResult(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	keyResultID, err := uuid.Parse(chi.URLParam(r, "keyResultID"))
	if err != nil {
		http.Error(w, "invalid key result ID", http.StatusBadRequest)
		return
	}

	if err := s.db.SoftDeleteKeyResult(ctx, keyResultID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Get redirect target from referrer or default to root
	referer := r.FormValue("redirect")
	if referer == "" {
		referer = "/p/" + projectID.String() + "/okr"
	}
	http.Redirect(w, r, referer, http.StatusFound)
}

func (s *Server) handleLinkKeyResult(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	objectiveID, err := uuid.Parse(chi.URLParam(r, "objectiveID"))
	if err != nil {
		http.Error(w, "invalid objective ID", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form data", http.StatusBadRequest)
		return
	}

	keyResultIDStr := r.FormValue("key_result_id")
	keyResultID, err := uuid.Parse(keyResultIDStr)
	if err != nil {
		http.Error(w, "invalid key result ID", http.StatusBadRequest)
		return
	}

	if err := s.db.LinkKeyResultToObjective(ctx, objectiveID, keyResultID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/p/"+projectID.String()+"/okr/objectives/"+objectiveID.String(), http.StatusFound)
}

func (s *Server) handleUnlinkKeyResult(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	objectiveID, err := uuid.Parse(chi.URLParam(r, "objectiveID"))
	if err != nil {
		http.Error(w, "invalid objective ID", http.StatusBadRequest)
		return
	}

	keyResultID, err := uuid.Parse(chi.URLParam(r, "keyResultID"))
	if err != nil {
		http.Error(w, "invalid key result ID", http.StatusBadRequest)
		return
	}

	if err := s.db.UnlinkKeyResultFromObjective(ctx, objectiveID, keyResultID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/p/"+projectID.String()+"/okr/objectives/"+objectiveID.String(), http.StatusFound)
}

// jsonError writes a JSON error response.
func jsonError(w http.ResponseWriter, message string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error": message,
	})
}

// apiTuneOKRs handles the async OKR tuning API endpoint.
func (s *Server) apiTuneOKRs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		jsonError(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	// Get strategy
	strategies, err := s.db.GetStrategiesByProject(ctx, projectID)
	if err != nil || len(strategies) == 0 {
		jsonError(w, "no strategy found", http.StatusNotFound)
		return
	}
	strategyID := strategies[0].ID

	// Get untuned items
	untunedObjs, err := s.db.GetUntunedObjectives(ctx, strategyID)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	untunedKRs, err := s.db.GetUntunedKeyResults(ctx, strategyID)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if len(untunedObjs) == 0 && len(untunedKRs) == 0 {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"message": "No untuned items to process",
			"tuned":   0,
		})
		return
	}

	// Build tuning input
	input := agent.OKRTuneInput{
		Objectives: make([]agent.ObjectiveForTuning, 0, len(untunedObjs)),
		KeyResults: make([]agent.KeyResultForTuning, 0, len(untunedKRs)),
	}

	for _, obj := range untunedObjs {
		input.Objectives = append(input.Objectives, agent.ObjectiveForTuning{
			ID:          obj.ID.String(),
			Description: obj.Description,
		})
	}

	for _, kr := range untunedKRs {
		input.KeyResults = append(input.KeyResults, agent.KeyResultForTuning{
			ID:          kr.ID.String(),
			Description: kr.Description,
			TargetUnits: kr.TargetUnits,
		})
	}

	// Call tuner
	client, err := agent.NewClient("")
	if err != nil {
		jsonError(w, "failed to create agent client: "+err.Error(), http.StatusInternalServerError)
		return
	}

	tuner := agent.NewOKRTuner(client)
	result, _, err := tuner.TuneOKRs(ctx, input)
	if err != nil {
		jsonError(w, "tuning failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Update database with tuning results
	tunedCount := 0
	for _, score := range result.ObjectiveScores {
		id, err := uuid.Parse(score.ID)
		if err != nil {
			continue
		}
		if err := s.db.UpdateObjectiveTuning(ctx, id, score.Score, score.Feedback); err == nil {
			tunedCount++
		}
	}

	for _, score := range result.KeyResultScores {
		id, err := uuid.Parse(score.ID)
		if err != nil {
			continue
		}
		if err := s.db.UpdateKeyResultTuning(ctx, id, score.Score, score.Feedback); err == nil {
			tunedCount++
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message": "Tuning complete",
		"tuned":   tunedCount,
		"results": result,
	})
}
