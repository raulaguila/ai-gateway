package gauss_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	gateway "github.com/raulaguila/ai-gateway"
	"github.com/raulaguila/ai-gateway/providers/gauss"
)

func TestTypedCredentialsAndExplicitRefresh(t *testing.T) {
	var fail atomic.Bool
	var calls atomic.Int32
	s := server(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		headers(t, r, false)
		if fail.Load() {
			w.WriteHeader(500)
			return
		}
		io.WriteString(w, catalogJSON)
	})
	p, err := gauss.New(gauss.Config{BaseURL: s.URL, Credentials: gauss.Credentials{OpenAPIToken: " Bearer openapi-secret ", ClientToken: "client-secret"}})
	if err != nil {
		t.Fatal(err)
	}
	defer p.CloseIdleConnections()
	status := p.CatalogStatus()
	if status.Loaded || !status.Stale || !status.UpdatedAt.IsZero() || calls.Load() != 0 {
		t.Fatalf("initial status=%+v calls=%d", status, calls.Load())
	}
	if _, err = gateway.NewSingle(p, "guid-1"); !gateway.IsCode(err, gateway.InvalidConfiguration) {
		t.Fatal("missing snapshot not diagnosed locally", err)
	}
	models, err := p.RefreshModels(context.Background())
	if err != nil || len(models) != 2 {
		t.Fatalf("models=%+v err=%v", models, err)
	}
	status = p.CatalogStatus()
	if !status.Loaded || status.Stale || status.ModelCount != 2 || !status.ExpiresAt.After(status.UpdatedAt) || calls.Load() != 1 {
		t.Fatalf("refreshed status=%+v calls=%d", status, calls.Load())
	}
	if _, err := gateway.NewSingle(p, "guid-1"); err != nil || calls.Load() != 1 {
		t.Fatal("construction performed I/O", err)
	}
	fail.Store(true)
	if _, err := p.RefreshModels(context.Background()); err == nil || p.CatalogStatus() != status {
		t.Fatal("failed refresh changed catalog status", err)
	}
	stale := provider(t, gauss.Config{CatalogUpdatedAt: time.Now().Add(-time.Hour)})
	if !stale.CatalogStatus().Stale {
		t.Fatal("expired catalog reported fresh")
	}
}

func TestCredentialValidationAndGatewayPrerequisites(t *testing.T) {
	for _, cfg := range []gauss.Config{
		{Credentials: gauss.Credentials{OpenAPIToken: "one"}},
		{Credentials: gauss.Credentials{OpenAPIToken: "one", ClientToken: "two\nthree"}},
		{APIKey: credentials, Credentials: gauss.Credentials{OpenAPIToken: "one", ClientToken: "two"}},
	} {
		if _, err := gauss.New(cfg); !gateway.IsCode(err, gateway.InvalidConfiguration) {
			t.Fatal("invalid typed credentials accepted", err)
		}
	}
	p, err := gauss.New(gauss.Config{APIKey: credentials})
	if err != nil {
		t.Fatal(err)
	}
	defer p.CloseIdleConnections()
	g, err := gateway.New(gateway.Config{Deployments: map[string]gateway.Provider{"gauss": p}, Models: map[string]gateway.ModelRoute{"chat": {Deployment: "gauss", Model: "guid-1"}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = g.Chat(context.Background(), gateway.ChatRequest{Model: "chat", Messages: []gateway.Message{gateway.User("hello")}})
	var detail *gateway.Error
	if !errors.As(err, &detail) || detail.Code != gateway.InvalidConfiguration || detail.Field != "Models" || detail.ModelAlias != "chat" || detail.Model != "guid-1" {
		t.Fatalf("missing actionable routing details: %+v", err)
	}
}
