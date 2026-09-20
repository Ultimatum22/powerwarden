package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNtfySendsTitlePriorityAndAuth(t *testing.T) {
	var gotTitle, gotPriority, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTitle = r.Header.Get("Title")
		gotPriority = r.Header.Get("Priority")
		gotAuth = r.Header.Get("Authorization")
		buf := make([]byte, 1024)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := Ntfy{URL: srv.URL, Token: "secret-token"}
	err := n.Notify(context.Background(), Notification{
		Title: "Host shutdown", Body: "pve01 is shutting down", Priority: PriorityUrgent,
	})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}

	if gotTitle != "Host shutdown" {
		t.Errorf("Title = %q", gotTitle)
	}
	if gotPriority != "5" {
		t.Errorf("Priority = %q, want 5 (urgent)", gotPriority)
	}
	if gotAuth != "Bearer secret-token" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotBody != "pve01 is shutting down" {
		t.Errorf("Body = %q", gotBody)
	}
}

func TestNtfyErrorsOnNonSuccessStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	n := Ntfy{URL: srv.URL}
	if err := n.Notify(context.Background(), Notification{Body: "x"}); err == nil {
		t.Fatal("expected an error for a 403 response")
	}
}

func TestFakeRecordsNotifications(t *testing.T) {
	f := NewFake()
	if err := f.Notify(context.Background(), Notification{Title: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := f.Notify(context.Background(), Notification{Title: "b"}); err != nil {
		t.Fatal(err)
	}
	sent := f.Sent()
	if len(sent) != 2 || sent[0].Title != "a" || sent[1].Title != "b" {
		t.Fatalf("Sent() = %+v", sent)
	}
}
