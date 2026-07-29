package oauthlink

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"golang.org/x/oauth2"
)

const stateTTL = 10 * time.Minute

// Config configures a generic OAuth link handler.
type Config struct {
	Provider     string // e.g. "github", "atlassian" - used for cookie name/path
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Endpoint     oauth2.Endpoint
	Scopes       []string
	AuthParams   []oauth2.AuthCodeOption // extra params for AuthCodeURL
	Log          *slog.Logger
	OnSuccess    func(w http.ResponseWriter, r *http.Request, token *oauth2.Token)
}

// Handler provides HTTP handlers for OAuth2 login/callback.
type Handler struct {
	oauth      *oauth2.Config
	log        *slog.Logger
	provider   string
	authParams []oauth2.AuthCodeOption
	onSuccess  func(w http.ResponseWriter, r *http.Request, token *oauth2.Token)
}

func New(cfg Config) *Handler {
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	return &Handler{
		oauth: &oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURL,
			Scopes:       cfg.Scopes,
			Endpoint:     cfg.Endpoint,
		},
		log:        log,
		provider:   cfg.Provider,
		authParams: cfg.AuthParams,
		onSuccess:  cfg.OnSuccess,
	}
}

func (h *Handler) cookieName() string {
	return fmt.Sprintf("gritz_%s_state", h.provider)
}

func (h *Handler) cookiePath() string {
	return fmt.Sprintf("/%s/callback", h.provider)
}

// HandleLogin initiates the OAuth2 authorization flow.
func (h *Handler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	state, err := generateRandomState()
	if err != nil {
		h.log.Error("failed to generate state", "err", err)
		http.Error(w, "failed to generate state", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     h.cookieName(),
		Value:    state,
		Path:     h.cookiePath(),
		MaxAge:   int(stateTTL.Seconds()),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, h.oauth.AuthCodeURL(state, h.authParams...), http.StatusFound)
}

// HandleCallback handles the OAuth2 callback.
func (h *Handler) HandleCallback(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(h.cookieName())
	if err != nil {
		http.Error(w, "missing state cookie", http.StatusBadRequest)
		return
	}
	if r.URL.Query().Get("state") != cookie.Value {
		http.Error(w, "state mismatch", http.StatusBadRequest)
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code parameter", http.StatusBadRequest)
		return
	}
	token, err := h.oauth.Exchange(r.Context(), code)
	if err != nil {
		h.log.Error("failed to exchange code", "err", err)
		http.Error(w, "failed to exchange code", http.StatusInternalServerError)
		return
	}

	// Clear state cookie
	http.SetCookie(w, &http.Cookie{
		Name:     h.cookieName(),
		Value:    "",
		Path:     h.cookiePath(),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})

	if h.onSuccess != nil {
		h.onSuccess(w, r, token)
	} else {
		http.Redirect(w, r, "/", http.StatusFound)
	}
}

func generateRandomState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}
