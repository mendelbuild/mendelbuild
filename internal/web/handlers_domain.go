package web

import (
	"context"
	"log"
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

	// Reserve the address and request the certificate now, so the records are on
	// the page the user is about to be redirected to.
	s.ensureDomainInfrastructure(ctx, projectID)

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

// ensureDomainInfrastructure reserves the address and requests the certificate
// as soon as Mendel has everything it needs to do so.
//
// Doing this on the first deploy was backwards. The records have to exist before
// a deployment is reachable, so a user who deployed first got a name that
// resolved nowhere, and only then learnt what to create. Running it when the
// domain is saved -- and again when credentials arrive, since either can come
// second -- means the records are on screen before there is anything to deploy.
//
// Quiet by design: nothing here gates saving a domain, and a project whose
// channel cannot do this simply has no records yet, which the page explains.
func (s *Server) ensureDomainInfrastructure(ctx context.Context, projectID uuid.UUID) {
	pd, err := s.db.GetProjectDomain(ctx, projectID)
	if err != nil || pd == nil || pd.BaseDomain == "" {
		return
	}
	if pd.StaticIP != "" && pd.ACMERecordName != "" {
		return // Both already done; neither changes.
	}

	channel, err := s.db.GetActiveProjectDeploymentChannel(ctx, projectID)
	if err != nil || channel == nil || channel.HostingPlatform == nil ||
		channel.HostingPlatform.HostnameSource != domain.HostnameFromUser {
		return
	}

	env, err := s.deployCredentialsForChannel(ctx, projectID, channel)
	if err != nil {
		return // Credentials not in yet; this runs again when they are.
	}
	session, err := newGKESession(ctx, env)
	if err != nil {
		log.Printf("domain: could not reach the cluster to prepare %s: %v", pd.BaseDomain, err)
		return
	}
	defer session.cleanup()

	if _, err := s.ensureStaticIP(ctx, projectID, session); err != nil {
		log.Printf("domain: could not reserve an address: %v", err)
		return
	}
	// Re-read: the address was just written, and the certificate request needs a
	// record of it that includes what the reservation produced.
	if pd, err = s.db.GetProjectDomain(ctx, projectID); err != nil || pd == nil {
		return
	}
	if err := s.ensureCertificate(ctx, projectID, pd, session); err != nil {
		log.Printf("domain: could not request a certificate: %v", err)
	}
}
