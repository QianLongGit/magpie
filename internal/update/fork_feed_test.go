package update

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// This fork serves its own releases, so its builds take no default update
// feed: the official one would offer the official releases and pull a fork
// build back onto them, losing the fork's features. MAGPIE_UPDATE_FEED (and
// MAGPIE_NOTES_FEED) point at a server of one's own and work as before.

// TestForkTakesNoDefaultFeed: without the env, no feed is named at all; with
// it, the feed named is the one the user gave.
func TestForkTakesNoDefaultFeed(t *testing.T) {
	t.Setenv("MAGPIE_UPDATE_FEED", "")
	t.Setenv("MAGPIE_NOTES_FEED", "")
	if f := Feed(); f != "" {
		t.Fatalf("default feed: %q", f)
	}
	if f := NotesFeed(); f != "" {
		t.Fatalf("default notes feed: %q", f)
	}
	t.Setenv("MAGPIE_UPDATE_FEED", "http://updates.example.test/api/latest")
	if f := Feed(); f != "http://updates.example.test/api/latest" {
		t.Fatalf("feed from env: %q", f)
	}
	t.Setenv("MAGPIE_NOTES_FEED", "http://updates.example.test/api/notes")
	if f := NotesFeed(); f != "http://updates.example.test/api/notes" {
		t.Fatalf("notes feed from env: %q", f)
	}
}

// TestForkLatestAsksNothingWithoutFeed: an update check with no feed answers
// ErrNoFeed before any request is made — the official site, and GitHub as
// its fallback, are never asked.
func TestForkLatestAsksNothingWithoutFeed(t *testing.T) {
	t.Setenv("MAGPIE_UPDATE_FEED", "")
	for name, ask := range map[string]func(context.Context) (*Release, error){
		"Latest":       Latest,
		"LatestStable": LatestStable,
	} {
		if _, err := ask(context.Background()); !errors.Is(err, ErrNoFeed) {
			t.Errorf("%s without a feed: %v", name, err)
		}
	}
	if fallsBack(context.Background()) {
		t.Error("the GitHub fallback is on without a feed")
	}
	if githubOnly() {
		t.Error("the GitHub-only path is on without a feed")
	}
}

// TestForkFeedFromEnvStillAsked: a feed the user set is asked and its
// release read as the site's always was.
func TestForkFeedFromEnvStillAsked(t *testing.T) {
	var asked int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked++
		w.Write([]byte(`{"version": "0.9.9", "notes": "fork release", "assets": {}}`))
	}))
	defer srv.Close()
	t.Setenv("MAGPIE_UPDATE_FEED", srv.URL)
	rel, err := Latest(context.Background())
	if err != nil {
		t.Fatalf("a feed from the env: %v", err)
	}
	if rel.Version != "0.9.9" || asked != 1 {
		t.Fatalf("the feed's release: %+v, asked %d times", rel, asked)
	}
	if fallsBack(context.Background()) {
		t.Error("a feed of one's own must not fall back onto GitHub")
	}
}

// TestForkNotesAskNothingWithoutFeed: the What's-new list with no notes feed
// is empty, not an error, and nothing is asked for it.
func TestForkNotesAskNothingWithoutFeed(t *testing.T) {
	t.Setenv("MAGPIE_NOTES_FEED", "")
	notes, err := NotesBack(context.Background(), "0.9.9", 5, "")
	if err != nil {
		t.Fatalf("notes without a feed: %v", err)
	}
	if len(notes) != 0 {
		t.Fatalf("notes without a feed: %+v", notes)
	}
}
