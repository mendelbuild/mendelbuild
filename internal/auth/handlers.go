package auth

import (
	"fmt"
	"net/http"
)

// HandleLogin initiates the Google OAuth flow.
func (a *Auth) HandleLogin(w http.ResponseWriter, r *http.Request) {
	url, state := a.GetLoginURL()
	a.SetStateCookie(w, state)
	http.Redirect(w, r, url, http.StatusSeeOther)
}

// HandleCallback handles the OAuth callback from Google.
func (a *Auth) HandleCallback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	if err := a.ValidateStateCookie(w, r, state); err != nil {
		http.Error(w, "Invalid state parameter", http.StatusBadRequest)
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "Missing authorization code", http.StatusBadRequest)
		return
	}

	userInfo, err := a.ExchangeCode(r.Context(), code)
	if err != nil {
		fmt.Printf("[auth] exchange code error: %v\n", err)
		http.Error(w, "Authentication failed", http.StatusInternalServerError)
		return
	}

	user, err := a.FindOrCreateUser(r.Context(), userInfo)
	if err != nil {
		fmt.Printf("[auth] find/create user error: %v\n", err)
		http.Error(w, "Failed to create user", http.StatusInternalServerError)
		return
	}

	token, err := a.CreateSession(r.Context(), user.ID)
	if err != nil {
		fmt.Printf("[auth] create session error: %v\n", err)
		http.Error(w, "Failed to create session", http.StatusInternalServerError)
		return
	}

	a.SetSessionCookie(w, token)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// HandleLogout logs the user out.
func (a *Auth) HandleLogout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(sessionCookieName)
	if err == nil {
		a.DeleteSession(r.Context(), cookie.Value)
	}
	a.ClearSessionCookie(w)
	http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
}
