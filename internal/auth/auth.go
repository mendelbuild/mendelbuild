package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/bhs/mendelbuild/internal/db"
	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/google/uuid"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	sessionCookieName = "mendel_session"
	sessionDuration   = 30 * 24 * time.Hour // 30 days
	stateCookieName   = "mendel_oauth_state"
)

var (
	ErrNoSession     = errors.New("no valid session")
	ErrInvalidState  = errors.New("invalid oauth state")
	ErrMissingConfig = errors.New("missing auth configuration")
)

// Config holds authentication configuration.
type Config struct {
	GoogleClientID     string
	GoogleClientSecret string
	BaseURL            string
	SessionSecret      []byte
}

// ConfigFromEnv loads auth config from environment variables.
func ConfigFromEnv() (*Config, error) {
	clientID := os.Getenv("GOOGLE_CLIENT_ID")
	clientSecret := os.Getenv("GOOGLE_CLIENT_SECRET")
	baseURL := os.Getenv("MENDEL_BASE_URL")
	sessionSecret := os.Getenv("SESSION_SECRET")

	if clientID == "" || clientSecret == "" {
		return nil, fmt.Errorf("%w: GOOGLE_CLIENT_ID and GOOGLE_CLIENT_SECRET required", ErrMissingConfig)
	}
	if baseURL == "" {
		baseURL = "http://localhost:8080"
	}
	if sessionSecret == "" {
		return nil, fmt.Errorf("%w: SESSION_SECRET required", ErrMissingConfig)
	}

	secret, err := hex.DecodeString(sessionSecret)
	if err != nil {
		return nil, fmt.Errorf("SESSION_SECRET must be hex-encoded: %w", err)
	}

	return &Config{
		GoogleClientID:     clientID,
		GoogleClientSecret: clientSecret,
		BaseURL:            baseURL,
		SessionSecret:      secret,
	}, nil
}

// Auth handles authentication and session management.
type Auth struct {
	config      *Config
	oauthConfig *oauth2.Config
	db          *db.DB
}

// New creates a new Auth instance.
func New(cfg *Config, database *db.DB) *Auth {
	oauthConfig := &oauth2.Config{
		ClientID:     cfg.GoogleClientID,
		ClientSecret: cfg.GoogleClientSecret,
		RedirectURL:  cfg.BaseURL + "/auth/callback",
		Scopes:       []string{"openid", "email", "profile"},
		Endpoint:     google.Endpoint,
	}

	return &Auth{
		config:      cfg,
		oauthConfig: oauthConfig,
		db:          database,
	}
}

// GoogleUserInfo represents the response from Google's userinfo endpoint.
type GoogleUserInfo struct {
	Sub     string `json:"sub"`
	Email   string `json:"email"`
	Name    string `json:"name"`
	Picture string `json:"picture"`
}

// GetLoginURL returns the Google OAuth login URL with a random state.
func (a *Auth) GetLoginURL() (string, string) {
	state := generateState()
	url := a.oauthConfig.AuthCodeURL(state, oauth2.AccessTypeOffline)
	return url, state
}

// ExchangeCode exchanges an authorization code for user info.
func (a *Auth) ExchangeCode(ctx context.Context, code string) (*GoogleUserInfo, error) {
	token, err := a.oauthConfig.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("exchange code: %w", err)
	}

	client := a.oauthConfig.Client(ctx, token)
	resp, err := client.Get("https://www.googleapis.com/oauth2/v3/userinfo")
	if err != nil {
		return nil, fmt.Errorf("get userinfo: %w", err)
	}
	defer resp.Body.Close()

	var userInfo GoogleUserInfo
	if err := json.NewDecoder(resp.Body).Decode(&userInfo); err != nil {
		return nil, fmt.Errorf("decode userinfo: %w", err)
	}

	return &userInfo, nil
}

// FindOrCreateUser finds an existing user by Google ID or creates a new one.
func (a *Auth) FindOrCreateUser(ctx context.Context, info *GoogleUserInfo) (*domain.User, error) {
	user, err := a.db.GetUserByGoogleID(ctx, info.Sub)
	if err == nil {
		return user, nil
	}

	user, err = a.db.GetUserByEmail(ctx, info.Email)
	if err == nil {
		if err := a.db.UpdateUserGoogleID(ctx, user.ID, info.Sub); err != nil {
			return nil, fmt.Errorf("update google id: %w", err)
		}
		user.GoogleID = info.Sub
		return user, nil
	}

	user = &domain.User{
		ID:         uuid.New(),
		Email:      info.Email,
		Name:       info.Name,
		PictureURL: info.Picture,
		GoogleID:   info.Sub,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	if err := a.db.CreateUser(ctx, user); err != nil {
		return nil, fmt.Errorf("create user: %w", err)
	}

	return user, nil
}

// CreateSession creates a new session for a user and returns the session token.
func (a *Auth) CreateSession(ctx context.Context, userID uuid.UUID) (string, error) {
	token := generateSessionToken()
	tokenHash := hashToken(token)

	session := &domain.Session{
		ID:        uuid.New(),
		UserID:    userID,
		TokenHash: tokenHash,
		ExpiresAt: time.Now().Add(sessionDuration),
		CreatedAt: time.Now(),
	}

	if err := a.db.CreateSession(ctx, session); err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}

	return token, nil
}

// ValidateSession validates a session token and returns the user.
func (a *Auth) ValidateSession(ctx context.Context, token string) (*domain.User, error) {
	tokenHash := hashToken(token)

	session, err := a.db.GetSessionByTokenHash(ctx, tokenHash)
	if err != nil {
		return nil, ErrNoSession
	}

	if time.Now().After(session.ExpiresAt) {
		a.db.DeleteSession(ctx, session.ID)
		return nil, ErrNoSession
	}

	user, err := a.db.GetUser(ctx, session.UserID)
	if err != nil {
		return nil, ErrNoSession
	}

	return user, nil
}

// DeleteSession deletes a session by token.
func (a *Auth) DeleteSession(ctx context.Context, token string) error {
	tokenHash := hashToken(token)
	session, err := a.db.GetSessionByTokenHash(ctx, tokenHash)
	if err != nil {
		return nil
	}
	return a.db.DeleteSession(ctx, session.ID)
}

// UserFromRequest extracts the user from a request's session cookie.
func (a *Auth) UserFromRequest(r *http.Request) (*domain.User, error) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return nil, ErrNoSession
	}

	return a.ValidateSession(r.Context(), cookie.Value)
}

// SetSessionCookie sets the session cookie on a response.
func (a *Auth) SetSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   a.config.BaseURL[:5] == "https",
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionDuration.Seconds()),
	})
}

// ClearSessionCookie clears the session cookie.
func (a *Auth) ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
}

// SetStateCookie sets the OAuth state cookie.
func (a *Auth) SetStateCookie(w http.ResponseWriter, state string) {
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    state,
		Path:     "/",
		HttpOnly: true,
		Secure:   a.config.BaseURL[:5] == "https",
		SameSite: http.SameSiteLaxMode,
		MaxAge:   600, // 10 minutes
	})
}

// ValidateStateCookie validates and clears the OAuth state cookie.
func (a *Auth) ValidateStateCookie(w http.ResponseWriter, r *http.Request, state string) error {
	cookie, err := r.Cookie(stateCookieName)
	if err != nil {
		return ErrInvalidState
	}

	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})

	if cookie.Value != state {
		return ErrInvalidState
	}

	return nil
}

func generateState() string {
	b := make([]byte, 16)
	rand.Read(b)
	return base64.URLEncoding.EncodeToString(b)
}

func generateSessionToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.URLEncoding.EncodeToString(b)
}

func hashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}
