package router

import (
	"html/template"
	"io/fs"
	"strings"
	"testing"

	"github.com/v03413/bepusdt/static"
)

func TestSMCheckoutTemplateIsEmbedded(t *testing.T) {
	checkout, err := readCheckoutInfoFromFS(static.Checkout, "checkout/sm")
	if err != nil {
		t.Fatalf("read sm checkout info: %v", err)
	}

	if checkout.Name != "sm" {
		t.Fatalf("unexpected checkout name: %q", checkout.Name)
	}
	if checkout.Author == "" {
		t.Fatal("checkout author is required")
	}
	if checkout.Desc == "" {
		t.Fatal("checkout desc is required")
	}

	view, err := fs.ReadFile(static.Checkout, "checkout/sm/views/checkout.html")
	if err != nil {
		t.Fatalf("read sm checkout template: %v", err)
	}
	viewText := string(view)
	if !strings.Contains(viewText, "<!DOCTYPE html>") {
		t.Fatal("sm checkout template must be a complete HTML document")
	}
	if strings.Contains(viewText, "{{") {
		t.Fatal("sm checkout template must not contain unresolved Go template actions")
	}

	tmpl := template.New("default")
	if !registerTemplatesFromFS(tmpl, static.Checkout, "checkout/sm", "sm") {
		t.Fatal("sm checkout template was not registered")
	}
	if tmpl.Lookup("sm/checkout.html") == nil {
		t.Fatal("sm checkout template was not registered under expected name")
	}
}

func TestCheckoutScriptsUseHTTPSOnlyRedirects(t *testing.T) {
	for _, checkout := range []string{"sm"} {
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
