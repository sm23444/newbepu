package epusdt

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestSignVerifyRejectsOversizedBodyBeforeParsing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/create-order", bytes.NewReader(make([]byte, (1<<20)+1)))

	(Epusdt{}).SignVerify(ctx)

	if !ctx.IsAborted() {
		t.Fatal("oversized request was not aborted")
	}
	if !strings.Contains(recorder.Body.String(), "request body too large") {
		t.Fatalf("unexpected response: %s", recorder.Body.String())
	}
}
