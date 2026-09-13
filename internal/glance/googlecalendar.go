package glance

// Google Calendar OAuth connect flow, ahead of a future calendar-sync
// widget. This project's Google OAuth consent screen is stuck in "Testing"
// publishing status (the user's Google account can't publish it), which
// means refresh tokens expire after 7 days - the "paste into a SOPS secret
// + redeploy" pattern used elsewhere in this fork (see veille.go) would
// mean a weekly redeploy just to rotate a token. So instead:
// GOOGLE_CLIENT_ID/GOOGLE_CLIENT_SECRET (rarely change) stay in the SOPS
// secret as usual, but the refresh token is minted through a full
// server-side OAuth flow reachable from the browser and persisted to a
// small writable volume (GOOGLE_CALENDAR_TOKEN_PATH) - reconnecting is one
// click, no redeploy.
//
// This rides on whatever auth already protects this Glance instance (e.g.
// an Authentik forward-auth Traefik middleware in front of the whole host)
// - no auth code here, same as veille.go.

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const googleCalendarScope = "https://www.googleapis.com/auth/calendar.readonly"
const googleCalendarDefaultTokenPath = "/data/calendar/token.json"
const googleCalendarStateCookie = "gcal_oauth_state"

func googleCalendarTokenPath() string {
	return veilleEnv("GOOGLE_CALENDAR_TOKEN_PATH", googleCalendarDefaultTokenPath)
}

type googleCalendarToken struct {
	RefreshToken string    `json:"refresh_token"`
	ObtainedAt   time.Time `json:"obtained_at"`
}

func readGoogleCalendarToken() (*googleCalendarToken, error) {
	data, err := os.ReadFile(googleCalendarTokenPath())
	if err != nil {
		return nil, err
	}
	var tok googleCalendarToken
	if err := json.Unmarshal(data, &tok); err != nil {
		return nil, err
	}
	return &tok, nil
}

func writeGoogleCalendarToken(tok *googleCalendarToken) error {
	path := googleCalendarTokenPath()
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	data, err := json.Marshal(tok)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func randomState() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// googleCalendarRedirectURL must exactly match an "Authorized redirect URI"
// registered on the Google Cloud OAuth client (Web application type).
// Derived from the request rather than a config value so /connect and
// /callback always agree - this Glance instance is only ever reached over
// https (Cloudflare + Traefik force it), so that part is safe to assume.
func googleCalendarRedirectURL(r *http.Request) string {
	return fmt.Sprintf("https://%s/settings/calendar/callback", r.Host)
}

// --- /settings/calendar ----------------------------------------------------

func (a *application) handleGoogleCalendarSettings(w http.ResponseWriter, r *http.Request) {
	clientID := os.Getenv("GOOGLE_CLIENT_ID")
	clientSecret := os.Getenv("GOOGLE_CLIENT_SECRET")

	a.veillePageHead(w, "Google Calendar — Paramètres")
	fmt.Fprintf(w, `<a class="veille-back" href="%s/">&larr; Retour</a>`, a.Config.Server.BaseURL)
	fmt.Fprint(w, `<h1 class="size-h3">Google Calendar</h1>`)

	if clientID == "" || clientSecret == "" {
		fmt.Fprint(w, `<p class="color-base">Non configuré : GOOGLE_CLIENT_ID / GOOGLE_CLIENT_SECRET manquants.</p>`)
		veillePageFoot(w)
		return
	}

	switch r.URL.Query().Get("status") {
	case "connected":
		fmt.Fprint(w, `<p style="color:var(--color-positive)">Connexion Google réussie.</p>`)
	case "error":
		fmt.Fprintf(w, `<p style="color:var(--color-negative)">Échec de la connexion : %s</p>`,
			template.HTMLEscapeString(r.URL.Query().Get("message")))
	}

	buttonLabel := "Se connecter à Google"
	if tok, err := readGoogleCalendarToken(); err == nil {
		fmt.Fprintf(w, `<p class="color-base">Connecté depuis le %s.</p>`,
			template.HTMLEscapeString(tok.ObtainedAt.Local().Format("2006-01-02 15:04")))
		buttonLabel = "Se reconnecter à Google"
	} else {
		fmt.Fprint(w, `<p class="color-base">Non connecté.</p>`)
	}

	fmt.Fprintf(w, `<a href="%s/settings/calendar/connect" style="display:inline-block;padding:8px 16px;border-radius:6px;background:var(--color-primary);color:var(--color-background);text-decoration:none">%s</a>`,
		a.Config.Server.BaseURL, template.HTMLEscapeString(buttonLabel))
	veillePageFoot(w)
}

// --- /settings/calendar/connect ---------------------------------------------

func (a *application) handleGoogleCalendarConnect(w http.ResponseWriter, r *http.Request) {
	clientID := os.Getenv("GOOGLE_CLIENT_ID")
	if clientID == "" {
		http.Error(w, "Google Calendar is not configured (GOOGLE_CLIENT_ID unset)", http.StatusServiceUnavailable)
		return
	}

	state, err := randomState()
	if err != nil {
		http.Error(w, "generating state: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     googleCalendarStateCookie,
		Value:    state,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   300,
	})

	q := url.Values{
		"client_id":     {clientID},
		"redirect_uri":  {googleCalendarRedirectURL(r)},
		"response_type": {"code"},
		"scope":         {googleCalendarScope},
		"access_type":   {"offline"},
		// Forces Google to always hand back a fresh refresh_token, even if
		// this app was already authorized before - required since every
		// click of the reconnect button needs to yield a usable token.
		"prompt": {"consent"},
		"state":  {state},
	}
	http.Redirect(w, r, "https://accounts.google.com/o/oauth2/v2/auth?"+q.Encode(), http.StatusFound)
}

// --- /settings/calendar/callback --------------------------------------------

func (a *application) handleGoogleCalendarCallback(w http.ResponseWriter, r *http.Request) {
	settingsURL := a.Config.Server.BaseURL + "/settings/calendar"
	fail := func(message string) {
		http.Redirect(w, r, settingsURL+"?status=error&message="+url.QueryEscape(message), http.StatusFound)
	}

	if errParam := r.URL.Query().Get("error"); errParam != "" {
		fail(errParam)
		return
	}

	cookie, err := r.Cookie(googleCalendarStateCookie)
	if err != nil || cookie.Value == "" || cookie.Value != r.URL.Query().Get("state") {
		fail("invalid or expired state")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: googleCalendarStateCookie, Value: "", Path: "/", MaxAge: -1})

	code := r.URL.Query().Get("code")
	if code == "" {
		fail("missing code")
		return
	}

	clientID := os.Getenv("GOOGLE_CLIENT_ID")
	clientSecret := os.Getenv("GOOGLE_CLIENT_SECRET")
	if clientID == "" || clientSecret == "" {
		fail("Google Calendar is not configured")
		return
	}

	form := url.Values{
		"code":          {code},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"redirect_uri":  {googleCalendarRedirectURL(r)},
		"grant_type":    {"authorization_code"},
	}
	resp, err := veilleHTTPClient.PostForm("https://oauth2.googleapis.com/token", form)
	if err != nil {
		fail("exchanging code: " + err.Error())
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		fail(fmt.Sprintf("token exchange failed: status %d", resp.StatusCode))
		return
	}

	var payload struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.RefreshToken == "" {
		fail("Google did not return a refresh token")
		return
	}

	if err := writeGoogleCalendarToken(&googleCalendarToken{
		RefreshToken: payload.RefreshToken,
		ObtainedAt:   time.Now().UTC(),
	}); err != nil {
		fail("saving token: " + err.Error())
		return
	}

	http.Redirect(w, r, settingsURL+"?status=connected", http.StatusFound)
}

// --- Calendar API calls, used by the Sorties à Strasbourg widget's -------
// --- /sorties/data route (see sorties.go) to merge in the connected -------
// --- account's own events. ------------------------------------------------

const googleCalendarMaxEvents = 250

// googleCalendarAccessToken exchanges the stored refresh token for a
// short-lived access token. Called on every /sorties/data request rather
// than cached - that route is itself only hit as often as the widget's
// `cache: 1h`, so this is at most one extra round-trip per hour.
func googleCalendarAccessToken(refreshToken string) (string, error) {
	clientID := os.Getenv("GOOGLE_CLIENT_ID")
	clientSecret := os.Getenv("GOOGLE_CLIENT_SECRET")
	if clientID == "" || clientSecret == "" {
		return "", fmt.Errorf("Google Calendar is not configured")
	}
	form := url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"refresh_token": {refreshToken},
		"grant_type":    {"refresh_token"},
	}
	resp, err := veilleHTTPClient.PostForm("https://oauth2.googleapis.com/token", form)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("refreshing access token: status %d: %s", resp.StatusCode, string(body))
	}
	var payload struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	if payload.AccessToken == "" {
		return "", fmt.Errorf("no access token in refresh response")
	}
	return payload.AccessToken, nil
}

// googleCalendarProfile returns the connected account's email address - the
// primary calendar's "id" field, which Google always sets to the account's
// email. Avoids needing a separate userinfo/email OAuth scope just for the
// pill label.
func googleCalendarProfile(accessToken string) (string, error) {
	req, _ := http.NewRequest("GET", "https://www.googleapis.com/calendar/v3/calendars/primary", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := veilleHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetching calendar profile: status %d", resp.StatusCode)
	}
	var payload struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	return payload.ID, nil
}

type googleCalendarEvent struct {
	Summary  string `json:"summary"`
	Location string `json:"location"`
	HTMLLink string `json:"htmlLink"`
	Start    struct {
		Date     string `json:"date"`     // set for all-day events
		DateTime string `json:"dateTime"` // set for timed events
	} `json:"start"`
}

// isoAndTime returns the event's day (YYYY-MM-DD) and, for timed events, a
// "15:04" local time - "" for all-day events, matching the convention
// already used by the Ticketmaster/agenda-culturel events in events.json.
func (e googleCalendarEvent) isoAndTime(loc *time.Location) (iso string, timeStr string) {
	if e.Start.DateTime != "" {
		t, err := time.Parse(time.RFC3339, e.Start.DateTime)
		if err != nil {
			return "", ""
		}
		t = t.In(loc)
		return t.Format("2006-01-02"), t.Format("15:04")
	}
	return e.Start.Date, ""
}

func googleCalendarListEvents(accessToken string, timeMin, timeMax time.Time) ([]googleCalendarEvent, error) {
	q := url.Values{
		"timeMin":      {timeMin.Format(time.RFC3339)},
		"timeMax":      {timeMax.Format(time.RFC3339)},
		"singleEvents": {"true"},
		"orderBy":      {"startTime"},
		"maxResults":   {strconv.Itoa(googleCalendarMaxEvents)},
	}
	req, _ := http.NewRequest("GET", "https://www.googleapis.com/calendar/v3/calendars/primary/events?"+q.Encode(), nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := veilleHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("listing events: status %d: %s", resp.StatusCode, string(body))
	}
	var payload struct {
		Items []googleCalendarEvent `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return payload.Items, nil
}
