package glance

// GET /sorties/data merges the connected Google Calendar's own events (via
// googlecalendar.go) into the Sorties à Strasbourg month-grid produced by
// scripts/events/aggregate.py (Ticketmaster + culture.strasbourg.eu) and
// served statically by the data-server sidecar at localhost:8099/events.json.
// Doing the merge here rather than in the Python aggregator keeps the OAuth
// refresh token where it already lives (this pod's writable volume, never
// distributed to CI) and gives a third pill that updates live without
// waiting for the aggregator's own 6h schedule.
//
// Degrades silently to the sidecar's own data (no Google events, no email
// in the pill) if not connected or if any Calendar API call fails - same
// "one source failing doesn't break the others" resilience as the Python
// side already has for Ticketmaster vs. culture.strasbourg.eu.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const sortiesEventsSourceURL = "http://127.0.0.1:8099/events.json"
const googleCalendarHorizonDays = 120

type sortiesCell struct {
	Day     int              `json:"day"`
	ISO     string           `json:"iso"`
	InMonth bool             `json:"in_month"`
	IsToday bool             `json:"is_today"`
	NTM     int              `json:"n_tm"`
	NAG     int              `json:"n_ag"`
	NGC     int              `json:"n_gc"`
	Events  []map[string]any `json:"events"`
}

type sortiesMonth struct {
	Key   string          `json:"key"`
	Label string          `json:"label"`
	Weeks [][]sortiesCell `json:"weeks"`
}

type sortiesEventsData struct {
	GeneratedAt string           `json:"generated_at"`
	Counts      map[string]int   `json:"counts"`
	Days        []map[string]any `json:"days"`
	Months      []sortiesMonth   `json:"months"`
	GoogleEmail string           `json:"google_email,omitempty"`
}

func (a *application) handleSortiesData(w http.ResponseWriter, r *http.Request) {
	resp, err := veilleHTTPClient.Get(sortiesEventsSourceURL)
	if err != nil {
		http.Error(w, "fetching events: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		http.Error(w, fmt.Sprintf("fetching events: status %d", resp.StatusCode), http.StatusBadGateway)
		return
	}
	var data sortiesEventsData
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		http.Error(w, "decoding events: "+err.Error(), http.StatusBadGateway)
		return
	}

	mergeGoogleCalendarEvents(&data)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(data)
}

// mergeGoogleCalendarEvents injects the connected Google Calendar's events
// into the matching day cells, in place, and sets GoogleEmail for the
// pill's label. No-op if not connected or if any step fails.
func mergeGoogleCalendarEvents(data *sortiesEventsData) {
	tok, err := readGoogleCalendarToken()
	if err != nil {
		return
	}

	accessToken, err := googleCalendarAccessToken(tok.RefreshToken)
	if err != nil {
		return
	}

	email, err := googleCalendarProfile(accessToken)
	if err != nil {
		return
	}

	now := time.Now()
	events, err := googleCalendarListEvents(accessToken, now, now.AddDate(0, 0, googleCalendarHorizonDays))
	if err != nil {
		return
	}

	cellsByISO := make(map[string]*sortiesCell)
	for mi := range data.Months {
		for wi := range data.Months[mi].Weeks {
			for ci := range data.Months[mi].Weeks[wi] {
				cellsByISO[data.Months[mi].Weeks[wi][ci].ISO] = &data.Months[mi].Weeks[wi][ci]
			}
		}
	}

	loc, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		loc = time.UTC
	}

	for _, ev := range events {
		iso, timeStr := ev.isoAndTime(loc)
		cell, ok := cellsByISO[iso]
		if !ok {
			continue
		}
		cell.NGC++
		cell.Events = append(cell.Events, map[string]any{
			"title":    ev.Summary,
			"time":     timeStr,
			"venue":    ev.Location,
			"city":     "",
			"category": "",
			"price":    "",
			"url":      ev.HTMLLink,
			"source":   "gc",
			"gcal_url": ev.HTMLLink,
		})
	}

	data.GoogleEmail = email
}
