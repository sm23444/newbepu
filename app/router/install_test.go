package router

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/v03413/bepusdt/app/log"
	"github.com/v03413/bepusdt/app/model"
	"golang.org/x/crypto/bcrypt"
)

var routerInstallTokenPattern = regexp.MustCompile(`一次性安装令牌:\s*([0-9A-F]{64})`)

func TestFirstVisitRequiresOneTimeInstallToken(t *testing.T) {
	token := initializeRouterTestModel(t)
	if err := log.Init(t.TempDir()); err != nil {
		t.Fatalf("initialize test logger: %v", err)
	}
	t.Cleanup(log.Close)

	handler := Handler()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("first GET status = %d, want 200", response.Code)
	}
	if model.IsInstalled() {
		t.Fatal("first GET marked the system installed")
	}

	body := response.Body.String()
	for name, value := range map[string]string{
		"install token":   token,
		"API token":       model.GetK(model.ApiAuthToken),
		"session secret":  model.GetK(model.AdminSecret),
		"secure entrance": model.GetK(model.AdminSecure),
		"password hash":   model.GetK(model.AdminPassword),
	} {
		if value != "" && strings.Contains(body, value) {
			t.Fatalf("first GET disclosed %s", name)
		}
	}
	if !strings.Contains(body, `name="install_token"`) || !strings.Contains(body, `name="password"`) {
		t.Fatal("first GET did not render the secure installation form")
	}

	invalid := url.Values{
		"install_token":    {"wrong-token"},
		"username":         {"owner"},
		"password":         {"owner-password-123"},
		"confirm_password": {"owner-password-123"},
	}
	response = postInstallForm(handler, invalid)
	if response.Code != http.StatusForbidden {
		t.Fatalf("invalid install status = %d, want 403", response.Code)
	}
	if model.IsInstalled() {
		t.Fatal("invalid install request changed the install state")
	}
	if strings.Contains(response.Body.String(), "wrong-token") {
		t.Fatal("invalid install response echoed the submitted token")
	}

	oversized := url.Values{
		"install_token":    {token},
		"username":         {strings.Repeat("x", int(maxInstallFormBytes))},
		"password":         {"owner-password-123"},
		"confirm_password": {"owner-password-123"},
	}
	response = postInstallForm(handler, oversized)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized install status = %d, want 413", response.Code)
	}
	if model.IsInstalled() {
		t.Fatal("oversized install request changed the install state")
	}

	secure := model.GetK(model.AdminSecure)
	selectedPassword := "  owner-password-123  "
	valid := url.Values{
		"install_token":    {token},
		"username":         {"owner"},
		"password":         {selectedPassword},
		"confirm_password": {selectedPassword},
	}
	response = postInstallForm(handler, valid)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("valid install status = %d, want 303", response.Code)
	}
	if location := response.Header().Get("Location"); location != secure {
		t.Fatalf("valid install redirect = %q, want %q", location, secure)
	}
	if !model.IsInstalled() {
		t.Fatal("valid install request did not persist the install state")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(model.GetK(model.AdminPassword)), []byte(selectedPassword)); err != nil {
		t.Fatalf("owner-selected password was not saved: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(model.GetK(model.AdminPassword)), []byte(strings.TrimSpace(selectedPassword))); err == nil {
		t.Fatal("owner-selected password was silently trimmed")
	}
}

func initializeRouterTestModel(t *testing.T) string {
	t.Helper()

	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	previousStdout := os.Stdout
	os.Stdout = writePipe
	initErr := model.Init(filepath.Join(t.TempDir(), "router-bootstrap.db"), "")
	os.Stdout = previousStdout
	if err := writePipe.Close(); err != nil {
		t.Fatalf("close stdout writer: %v", err)
	}
	output, readErr := io.ReadAll(readPipe)
	_ = readPipe.Close()
	if initErr != nil {
		t.Fatalf("initialize router test model: %v", initErr)
	}
	if readErr != nil {
		t.Fatalf("read initialization output: %v", readErr)
	}
	t.Cleanup(model.Close)

	match := routerInstallTokenPattern.FindStringSubmatch(string(output))
	if len(match) != 2 {
		t.Fatalf("initialization output did not contain one-time install token: %q", strings.TrimSpace(string(output)))
	}

	return match[1]
}

func postInstallForm(handler http.Handler, values url.Values) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/install", strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	return response
}
