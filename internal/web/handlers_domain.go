package web

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/domain"
)

// handleProjectDomain shows where a project's deployments are reachable, and
// exactly which DNS records make that true.
//
// The records are the point of the page. Mendel knows the address it reserved
// and the names it will invent, so it can state the rows a provider asks for
// rather than describing them and leaving the reader to work out that "a
// wildcard A record" means typing an asterisk into a box.
func (s *Server) handleProjectDomain(w http.ResponseWriter, r *http.Request) {
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

	s.renderDomainPage(w, r, projectID, project, nil, "", r.URL.Query().Get("success") == "1")
}

// handleSaveProjectDomain stores the parts of the domain the user chooses.
func (s *Server) handleSaveProjectDomain(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	project, err := s.db.GetProject(ctx, projectID)
	if err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}

	// Normalise before validating, so a pasted URL is accepted as the domain it
	// obviously means rather than rejected on a technicality.
	base := domain.NormalizeDomain(r.FormValue("base_domain"))
	demo := domain.NormalizeLabel(r.FormValue("demo_subdomain"))
	prod := domain.NormalizeLabel(r.FormValue("prod_subdomain"))
	if demo == "" {
		demo = domain.DefaultDemoSubdomain
	}

	if msg := domain.ValidateDomain(base, r.FormValue("demo_subdomain"), r.FormValue("prod_subdomain")); msg != "" {
		draft := &domain.ProjectDomain{
			ProjectID: projectID, BaseDomain: base,
			DemoSubdomain: demo, ProdSubdomain: prod,
		}
		s.renderDomainPage(w, r, projectID, project, draft, msg, false)
		return
	}

	if err := s.db.UpsertProjectDomain(ctx, &domain.ProjectDomain{
		ProjectID: projectID, BaseDomain: base,
		DemoSubdomain: demo, ProdSubdomain: prod,
	}); err != nil {
		http.Error(w, "failed to save domain: "+err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/p/"+projectID.String()+"/domain?success=1", http.StatusSeeOther)
}

// renderDomainPage draws the page from stored settings, or from a rejected
// draft so a correction starts from what was typed rather than from blank
// fields.
func (s *Server) renderDomainPage(
	w http.ResponseWriter,
	r *http.Request,
	projectID uuid.UUID,
	project *domain.Project,
	draft *domain.ProjectDomain,
	errMsg string,
	success bool,
) {
	ctx := r.Context()

	stored, _ := s.db.GetProjectDomain(ctx, projectID)
	shown := stored
	if draft != nil {
		// Keep the address, which the user did not type and must not lose by
		// making a mistake in a field.
		if stored != nil {
			draft.StaticIP, draft.StaticIPName = stored.StaticIP, stored.StaticIPName
		}
		shown = draft
	}
	if shown == nil {
		shown = &domain.ProjectDomain{ProjectID: projectID, DemoSubdomain: domain.DefaultDemoSubdomain}
	}

	// Whether an address is even expected depends on the channel: platforms that
	// issue their own names never need one, and saying "Mendel has not reserved
	// an address" on such a project would describe a wait that is not happening.
	channel, _ := s.db.GetActiveProjectDeploymentChannel(ctx, projectID)
	needsDomain := channel != nil && channel.HostingPlatform != nil &&
		channel.HostingPlatform.HostnameSource == domain.HostnameFromUser

	data := map[string]interface{}{
		"Title":        "Domain: " + project.Name,
		"SettingsTab":  "domain",
		"ProjectID":    projectID.String(),
		"Project":      project,
		"Domain":       shown,
		"Records":      shown.DNSRecords(),
		"Blocker":      shown.DomainBlocker(needsDomain),
		"NeedsDomain":  needsDomain,
		"Channel":      channel,
		"DemoWildcard": shown.DemoWildcard(),
		"ProdHost":     shown.ProdHost(),
		"ExampleHost":  shown.DemoHost("pong-abc123"),
		"Error":        errMsg,
		"Success":      success,
	}
	s.addOpenInputCount(ctx, data)

	if err := s.renderPageFor(w, r, "project_domain.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
