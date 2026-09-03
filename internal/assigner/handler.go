package assigner

import (
	"net/http"
	"sync/atomic"
)

// Handler assigns and redirects. It is not a proxy.
//
// A request only reaches here when the route found no Arm cookie to match, so
// the whole job is: work out an Arm, set the cookie, send the visitor back to
// the same URL. The second request matches a cookie rule and never comes here
// again.
type Handler struct {
	// current is swapped wholesale when the allocation file changes, so a
	// request either sees the old allocation or the new one and never a
	// half-updated mixture.
	current atomic.Pointer[Allocation]

	// Secure marks the cookie Secure, which requires the site to be served over
	// https. Off means a cookie that can be read and rewritten in transit, and
	// a visitor who can rewrite it chooses their own Arm.
	Secure bool
}

// NewHandler serves an allocation.
func NewHandler(a Allocation, secure bool) *Handler {
	h := &Handler{Secure: secure}
	h.Set(a)
	return h
}

// Set replaces the allocation being served.
func (h *Handler) Set(a Allocation) { h.current.Store(&a) }

// Allocation returns what is being served.
func (h *Handler) Allocation() Allocation {
	if a := h.current.Load(); a != nil {
		return *a
	}
	return Allocation{}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Kubernetes needs somewhere to ask whether this is up, and it must not be
	// a path that assigns -- a probe would otherwise be handed a cookie and
	// counted as a participant.
	if r.URL.Path == "/healthz" {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
		return
	}

	alloc := h.Allocation()
	arm := alloc.Assign(h.key(r))

	http.SetCookie(w, &http.Cookie{
		Name:  CookieName,
		Value: arm,
		Path:  "/",
		// The visitor keeps their Arm across the experiment rather than being
		// reassigned when the browser is closed, which for a session cookie
		// would put one person in both Arms over a few days.
		MaxAge:   int(cookieLifetime.Seconds()),
		HttpOnly: true,
		Secure:   h.Secure,
		SameSite: http.SameSiteLaxMode,
	})

	// Back to where they were going. A redirect rather than a proxied response
	// so this never sits in the data path; the next request carries the cookie
	// and is routed without touching this at all.
	//
	// 303 rather than 302: a POST that arrived without a cookie must not be
	// replayed as a POST to a URL that will now route elsewhere, and 302's
	// method handling is famously inconsistent between clients.
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, r.URL.RequestURI(), http.StatusSeeOther)
}

// key reads the participant's identity from wherever the application said it
// lives. Absent is not an error: it means mainline.
func (h *Handler) key(r *http.Request) string {
	k := h.Allocation().Key
	switch k.Source {
	case "cookie":
		if c, err := r.Cookie(k.Name); err == nil {
			return c.Value
		}
	case "header":
		return r.Header.Get(k.Name)
	}
	return ""
}
