package notify

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFormat(t *testing.T) {
	at := time.Date(2026, 11, 10, 18, 0, 0, 0, time.UTC)
	got := Format("New <msg>", "waybill7-x.com-hostkit-contact", at, Field("Name", "A & B"), "line1\nline2_Cx")
	want := "<b>New &lt;msg&gt;</b>_C2026-11-10 21:30:00 IRST_Cwaybill7-x.com-hostkit-contact_C<b>Name:</b> A &amp; B_Cline1_Cline2_ Cx"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestSendRetries(t *testing.T) {
	calls := make(chan string, 5)
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		calls <- r.URL.Query().Get("m")
		if n < 2 {
			http.Error(w, "busy", 500)
			return
		}
		w.Write([]byte("telegramResult=1"))
	}))
	defer srv.Close()
	tg := &Telegram{URL: srv.URL + "/sendMsg?r=monitoring", Server: "test", Pause: time.Millisecond}
	tg.Send("Hello", "unit", "body")
	for i := 0; i < 2; i++ {
		select {
		case m := <-calls:
			if !strings.Contains(m, "_Ctest-unit_Cbody") {
				t.Errorf("message %q", m)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("no retry")
		}
	}
}
