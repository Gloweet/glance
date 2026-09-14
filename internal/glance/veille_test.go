package glance

import (
	"testing"
	"time"
)

func TestVeilleMarkDataWidgetStale(t *testing.T) {
	future := time.Now().Add(time.Hour)
	newWidget := func(url string) *customAPIWidget {
		w := &customAPIWidget{CustomAPIRequest: &CustomAPIRequest{URL: url}}
		w.withCacheDuration(30 * time.Minute)
		w.nextUpdate = future
		return w
	}

	matching := newWidget("http://localhost:8080/veille/data")

	veilleMarkDataWidgetStale(matching)

	now := time.Now()
	if !matching.requiresUpdate(&now) {
		t.Error("expected /veille/data widget to require an update after invalidation")
	}

	nonMatching := newWidget("https://example.com/api")

	veilleMarkDataWidgetStale(nonMatching)

	if nonMatching.requiresUpdate(&now) {
		t.Error("expected unrelated widget to keep its cache schedule")
	}

	nested := newWidget("http://localhost:8080/veille/data")
	group := &groupWidget{}
	group.Widgets = widgets{nested}

	veilleMarkDataWidgetStale(group)

	if !nested.requiresUpdate(&now) {
		t.Error("expected /veille/data widget nested in a group to require an update after invalidation")
	}
}

func TestVeilleRegenTriggeredWithin(t *testing.T) {
	veilleRegenMu.Lock()
	veilleRegenTriggeredAt = time.Time{}
	veilleRegenMu.Unlock()

	if _, ok := veilleRegenTriggeredWithin(15 * time.Minute); ok {
		t.Error("expected no trigger recorded yet")
	}

	veilleNoteRegenTriggered()

	triggeredAt, ok := veilleRegenTriggeredWithin(15 * time.Minute)
	if !ok {
		t.Fatal("expected recent trigger to be reported")
	}
	if time.Since(triggeredAt) > time.Minute {
		t.Error("expected trigger timestamp to be roughly now")
	}

	if _, ok := veilleRegenTriggeredWithin(time.Nanosecond); ok {
		t.Error("expected old trigger to be outside a tiny window")
	}
}

// Without VEILLE_GITHUB_TOKEN the Actions API can't be queried, so
// veilleGeneratingSince relies solely on the in-memory trigger record.
func TestVeilleGeneratingSinceWithoutToken(t *testing.T) {
	t.Setenv("VEILLE_GITHUB_TOKEN", "")

	veilleRegenMu.Lock()
	veilleRegenTriggeredAt = time.Time{}
	veilleRegenMu.Unlock()

	if s := veilleGeneratingSince("owner/repo", "main"); s != "" {
		t.Errorf("expected empty generating_since with no trigger, got %q", s)
	}

	veilleNoteRegenTriggered()

	s := veilleGeneratingSince("owner/repo", "main")
	if s == "" {
		t.Fatal("expected generating_since after a manual trigger")
	}
	if _, err := time.Parse(veilleTimeLayout, s); err != nil {
		t.Errorf("generating_since %q doesn't match layout %s: %v", s, veilleTimeLayout, err)
	}
}
