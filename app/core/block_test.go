package core

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetBoundaryHeights(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		wantStart  int64
		wantEnd    int64
		wantErr    bool
	}{
		{name: "valid", statusCode: http.StatusOK, body: `{"start":100,"end":200}`, wantStart: 100, wantEnd: 200},
		{name: "missing field", statusCode: http.StatusOK, body: `{"start":100}`, wantErr: true},
		{name: "reversed range", statusCode: http.StatusOK, body: `{"start":200,"end":100}`, wantErr: true},
		{name: "upstream failure", statusCode: http.StatusBadGateway, body: `{"error":"unavailable"}`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			start, end, err := (Api{api: server.URL}).GetBoundaryHeights(1, 2, "test")
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got start=%d end=%d", start, end)
				}
				return
			}
			if err != nil {
				t.Fatalf("GetBoundaryHeights: %v", err)
			}
			if start != tt.wantStart || end != tt.wantEnd {
				t.Fatalf("got (%d,%d), want (%d,%d)", start, end, tt.wantStart, tt.wantEnd)
			}
		})
	}
}
