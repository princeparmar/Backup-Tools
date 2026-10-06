package outlook

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func serveSizeEstimateGraph(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestEstimateMailboxBytes(t *testing.T) {
	base := serveSizeEstimateGraph(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/users/u1/mailFolders" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"value":[{"totalItemCount":10},{"totalItemCount":30}]}`))
	})

	got, err := EstimateMailboxBytes(context.Background(), "tok", base+"/users/u1")
	if err != nil {
		t.Fatal(err)
	}
	if want := 40 * averageMailMessageBytes; got != want {
		t.Fatalf("estimate = %d, want %d", got, want)
	}
}

func TestEstimateMailboxBytes_graphError(t *testing.T) {
	base := serveSizeEstimateGraph(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})

	if _, err := EstimateMailboxBytes(context.Background(), "tok", base+"/me"); err == nil {
		t.Fatal("expected error on 403")
	}
}

func TestEstimateOneDriveBytes(t *testing.T) {
	base := serveSizeEstimateGraph(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/users/u1/drive":
			_, _ = w.Write([]byte(`{"quota":{"used":12345,"total":99999}}`))
		default:
			http.NotFound(w, r)
		}
	})

	got, err := EstimateOneDriveBytes(context.Background(), "tok", base+"/users/u1")
	if err != nil {
		t.Fatal(err)
	}
	if got != 12345 {
		t.Fatalf("estimate = %d, want 12345", got)
	}

	got, err = EstimateOneDriveBytes(context.Background(), "tok", base+"/users/no-drive")
	if err != nil {
		t.Fatalf("missing drive should not error: %v", err)
	}
	if got != 0 {
		t.Fatalf("missing drive estimate = %d, want 0", got)
	}
}
