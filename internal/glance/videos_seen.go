package glance

// Self-tracked "seen" state for the Vidéos page's "Non regardées
// uniquement" toggle. There is no YouTube API for real watch history
// (the watchHistory playlist has been an empty placeholder since ~2016
// and there's no OAuth scope for it), so this only knows about videos
// clicked through this dashboard - not ones watched directly on
// YouTube/mobile/TV. Every video link on the Vidéos page routes through
// /videos/seen/{id} first (marks seen, then redirects to the real URL);
// the toggle is pure CSS (:has()), hiding elements the templates below
// tag with the video-seen class.

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
)

const videosSeenDefaultPath = "/data/seen/videos.json"

func videosSeenPath() string {
	if v := os.Getenv("VIDEOS_SEEN_PATH"); v != "" {
		return v
	}
	return videosSeenDefaultPath
}

var videosSeenStore = struct {
	mu  sync.RWMutex
	ids map[string]bool
}{ids: map[string]bool{}}

func videosSeenLoad() {
	data, err := os.ReadFile(videosSeenPath())
	if err != nil {
		return
	}
	var ids []string
	if json.Unmarshal(data, &ids) != nil {
		return
	}
	videosSeenStore.mu.Lock()
	defer videosSeenStore.mu.Unlock()
	for _, id := range ids {
		videosSeenStore.ids[id] = true
	}
}

func videosSeenSave() {
	videosSeenStore.mu.RLock()
	ids := make([]string, 0, len(videosSeenStore.ids))
	for id := range videosSeenStore.ids {
		ids = append(ids, id)
	}
	videosSeenStore.mu.RUnlock()

	data, err := json.Marshal(ids)
	if err != nil {
		return
	}
	_ = os.WriteFile(videosSeenPath(), data, 0o644)
}

func isVideoSeen(id string) bool {
	if id == "" {
		return false
	}
	videosSeenStore.mu.RLock()
	defer videosSeenStore.mu.RUnlock()
	return videosSeenStore.ids[id]
}

func markVideoSeen(id string) {
	if id == "" {
		return
	}
	videosSeenStore.mu.Lock()
	videosSeenStore.ids[id] = true
	videosSeenStore.mu.Unlock()
	videosSeenSave()
}

// GET /videos/seen/{id}?to=<url> - marks the video seen, then redirects
// to the real video URL. This is the href on every video card, so a
// normal click (no JS) is what populates the seen set.
func (a *application) handleVideoSeen(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	to := r.URL.Query().Get("to")
	if to == "" || (!strings.HasPrefix(to, "http://") && !strings.HasPrefix(to, "https://")) {
		http.Error(w, "invalid redirect target", http.StatusBadRequest)
		return
	}
	markVideoSeen(id)
	http.Redirect(w, r, to, http.StatusFound)
}
