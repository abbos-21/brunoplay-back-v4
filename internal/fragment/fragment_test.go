package fragment

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStarsPriceNano(t *testing.T) {
	var gotKey, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-API-Key")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		if gotKey != "k" {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"ok":false,"message":"bad key","code":"VALIDATION_ERROR"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"message":"x","result":{"amount":110,"price":{"ton":"0.6932","usd":"1.65","selected":"1.65"}}}`))
	}))
	defer srv.Close()

	got, err := New(srv.URL, "k").StarsPriceNano(context.Background(), 110)
	if err != nil || got != 693_200_000 {
		t.Fatalf("price: %d %v", got, err)
	}
	if gotKey != "k" || !strings.Contains(gotBody, `"amount":110`) {
		t.Fatalf("request: %s %s", gotKey, gotBody)
	}
	if _, err := New(srv.URL, "wrong").StarsPriceNano(context.Background(), 110); err == nil || !strings.Contains(err.Error(), "VALIDATION_ERROR") || strings.Contains(err.Error(), "wrong") {
		t.Fatalf("a refused key is an error that does not echo the key: %v", err)
	}
}
