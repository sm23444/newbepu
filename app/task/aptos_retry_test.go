package task

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/v03413/bepusdt/app/model"
)

func TestAptosVersionParseInvalidJSONRequeues(t *testing.T) {
	setupEVMReliabilityTestDB(t)
	setEVMTestTaskLogger(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "not-json")
	}))
	defer server.Close()
	setEVMReliabilityTestConf(t, model.RpcEndpointAptos, server.URL+"/")

	a := newAptos()
	a.client = server.Client()
	want := version{Start: 100, Limit: 25}
	a.versionParse(want)

	select {
	case got := <-a.versionQueue.Out:
		if got != want {
			t.Fatalf("requeued version = %#v, want %#v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("invalid JSON version was not requeued")
	}
}

func TestAptosVersionParseHTTPErrorDoesNotRequeue(t *testing.T) {
	setupEVMReliabilityTestDB(t)
	setEVMTestTaskLogger(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "temporary failure", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	setEVMReliabilityTestConf(t, model.RpcEndpointAptos, server.URL+"/")

	a := newAptos()
	a.client = server.Client()
	a.versionParse(version{Start: 100, Limit: 25})

	if got := a.versionQueue.Len(); got != 0 {
		t.Fatalf("queued retries = %d, want 0 for HTTP status failure", got)
	}
}
