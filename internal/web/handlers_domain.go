package web

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

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
	s.syncDomainRequest(ctx, projectID)

	http.Redirect(w, r, "/p/"+projectID.String()+"/domain?success=1", http.StatusSeeOther)
}

// handleSetNamedDemos records whether this project's demos need names.
func (s *Server) handleSetNamedDemos(w http.ResponseWriter, r *http.Request) {
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

	wanted := r.FormValue("wanted") == "yes"
	if err := s.db.SetNamedDemosWanted(ctx, projectID, wanted); err != nil {
		http.Error(w, "failed to save: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.syncDomainRequest(ctx, projectID)

	http.Redirect(w, r, "/p/"+projectID.String()+"/domain", http.StatusSeeOther)
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

	// Retry the parts Mendel owns when they are still outstanding.
	//
	// These fail for reasons outside Mendel that get fixed outside Mendel -- an
	// API not yet enabled, a role not yet granted -- and until now the retry only
	// happened on saving the domain or a credential. So the fix would be applied,
	// nothing would notice, and the page went on showing the same gap. Detached
	// from the request: it opens a session against the cluster, which is far too
	// slow to hold a page render on.
	if stored != nil && stored.BaseDomain != "" && (stored.StaticIP == "" || stored.ACMERecordName == "") {
		go s.ensureDomainInfrastructure(context.Background(), projectID)
	}
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

	// What is actually true, rather than what anyone has asserted. Cheap enough
	// to do on render: two DNS lookups, and the certificate only when a channel
	// with credentials exists.
	var steps []domain.DomainStep
	var headline string
	var waitingOnYou bool
	if shown.WantsNamedDemos() || shown.BaseDomain != "" {
		steps = shown.DomainReadiness(s.observeDomain(ctx, projectID, shown))
		headline, waitingOnYou = domain.DomainHeadline(steps)
	}

	data := map[string]interface{}{
		"Title":        "Domain: " + project.Name,
		"Steps":        steps,
		"Headline":     headline,
		"WaitingOnYou": waitingOnYou,
		"AskNamedDemos": needsDomain && shown.ShouldAskAboutNamedDemos(),
		"WantsNamed":   shown.WantsNamedDemos(),
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

// domainRequestTitle identifies the domain ask, so it is neither duplicated nor
// left open once the records resolve.
const domainRequestTitle = "Point your domain at Mendel"

// syncDomainRequest keeps the queue in step with what Mendel can see.
//
// Filed while a step is the user's move, and closed by observation rather than
// by anyone pressing a button: the records either resolve or they do not. That
// distinction is the whole reason these are not acknowledgements -- a record
// created with a typo would be acknowledged perfectly happily, and would surface
// an hour later as a certificate that never issued.
//
// Waiting on a certificate authority files nothing. It is real, and it is
// nobody's move, so it belongs in the ribbon and not in a list of things to do.
func (s *Server) syncDomainRequest(ctx context.Context, projectID uuid.UUID) {
	pd, err := s.db.GetProjectDomain(ctx, projectID)
	if err != nil {
		return
	}
	if !pd.WantsNamedDemos() {
		s.closeDomainRequest(ctx, projectID)
		return
	}

	steps := pd.DomainReadiness(s.observeDomain(ctx, projectID, pd))
	headline, waitingOnYou := domain.DomainHeadline(steps)
	if !waitingOnYou {
		s.closeDomainRequest(ctx, projectID)
		return
	}

	var detail strings.Builder
	fmt.Fprintf(&detail, "%s.\n\nWhere this stands:\n", headline)
	for _, step := range steps {
		mark := " "
		switch step.State {
		case domain.StepDone:
			mark = "x"
		case domain.StepYourMove:
			mark = ">"
		}
		fmt.Fprintf(&detail, "  [%s] %s — %s\n", mark, step.Name, step.Detail)
	}
	body := detail.String()
	link := fmt.Sprintf("/p/%s/domain", projectID)

	existing, err := s.db.FindOpenInputRequestByKind(ctx, projectID, domain.InputRequestKindManualSetup)
	if err != nil {
		return
	}
	if existing != nil && existing.Title == domainRequestTitle {
		existing.Details = &body
		existing.Link = &link
		if err := s.db.UpdateInputRequest(ctx, existing); err != nil {
			log.Printf("domain: could not update the domain request: %v", err)
		}
		return
	}
	if existing != nil {
		return // Someone else's manual-setup ask; leave it be.
	}

	now := time.Now()
	req := &domain.InputRequest{
		ID:               uuid.New(),
		ProjectID:        projectID,
		Kind:             domain.InputRequestKindManualSetup,
		Title:            domainRequestTitle,
		Details:          &body,
		Link:             &link,
		ObjectivityScore: 1.0, // The records resolve or they do not.
		ImportanceScore:  0.6, // Blocks sign-in and callbacks, not deployment.
		Status:           domain.InputRequestStatusNeedsAssignment,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := s.db.CreateInputRequest(ctx, req); err != nil {
		log.Printf("domain: could not create the domain request: %v", err)
	}
}

func (s *Server) closeDomainRequest(ctx context.Context, projectID uuid.UUID) {
	existing, err := s.db.FindOpenInputRequestByKind(ctx, projectID, domain.InputRequestKindManualSetup)
	if err != nil || existing == nil || existing.Title != domainRequestTitle {
		return
	}
	existing.Status = domain.InputRequestStatusResolved
	existing.Resolution = strPtr("approved")
	resolvedAt := time.Now()
	existing.ResolvedAt = &resolvedAt
	if err := s.db.UpdateInputRequest(ctx, existing); err != nil {
		log.Printf("domain: could not close the domain request: %v", err)
	}
}
