package router

import (
	"html/template"
	"io/fs"
	"strings"
	"testing"

	"github.com/v03413/bepusdt/static"
)

func TestLangGeCheckoutTemplateIsEmbedded(t *testing.T) {
	checkout, err := readCheckoutInfoFromFS(static.Checkout, "checkout/langge")
	if err != nil {
		t.Fatalf("read langge checkout info: %v", err)
	}

	if checkout.Name != "LangGe design" {
		t.Fatalf("unexpected checkout name: %q", checkout.Name)
	}
	if checkout.Author == "" {
		t.Fatal("checkout author is required")
	}
	if checkout.Desc == "" {
		t.Fatal("checkout desc is required")
	}

	view, err := fs.ReadFile(static.Checkout, "checkout/langge/views/checkout.html")
	if err != nil {
		t.Fatalf("read langge checkout template: %v", err)
	}
	if !strings.Contains(string(view), "{{ .trade_id }}") {
		t.Fatal("langge checkout template must only depend on trade_id server injection")
	}

	tmpl := template.New("default")
	if !registerTemplatesFromFS(tmpl, static.Checkout, "checkout/langge", "langge") {
		t.Fatal("langge checkout template was not registered")
	}
	if tmpl.Lookup("langge/checkout.html") == nil {
		t.Fatal("langge checkout template was not registered under expected name")
	}
}

func TestCheckoutScriptsUseHTTPSOnlyRedirects(t *testing.T) {
	for _, checkout := range []string{"official", "sm", "langge"} {
		t.Run(checkout, func(t *testing.T) {
			path := "checkout/" + checkout + "/assets/js/checkout.js"
			data, err := fs.ReadFile(static.Checkout, path)
			if err != nil {
				t.Fatalf("read checkout script: %v", err)
			}

			script := string(data)
			if !strings.Contains(script, "function safeHttpsUrl") {
				t.Fatal("checkout script must validate external URLs")
			}
			if !strings.Contains(script, "parsed.protocol !== 'https:'") {
				t.Fatal("checkout script must only allow HTTPS URLs")
			}
			for _, unsafe := range []string{
				`'<a href="' + ret`,
				`el.href = url;`,
				`safeSupportUrl`,
			} {
				if strings.Contains(script, unsafe) {
					t.Fatalf("checkout script contains unsafe URL handling: %s", unsafe)
				}
			}
		})
	}
}
