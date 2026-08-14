package mcp_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Romain-Badino/Avanti/internal/adapters/mcp"
)

// wantResourceMetadata est ce que l'en-tête WWW-Authenticate doit désigner :
// le document RFC 9728 de l'instance, par lequel un client MCP refusé découvre
// le serveur d'autorisation.
const wantResourceMetadata = `resource_metadata="` + baseURLTest + `/.well-known/oauth-protected-resource"`

// doRaw joue une requête HTTP brute contre le serveur de test.
func (tb *testbed) doRaw(t *testing.T, method, path, token string) (status int, header http.Header, body string) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), method, tb.server.URL+path, http.NoBody)
	if err != nil {
		t.Fatalf("construction de la requête : %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	result, err := tb.server.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s : %v", method, path, err)
	}
	defer func() {
		if closeErr := result.Body.Close(); closeErr != nil {
			t.Errorf("fermeture du corps de réponse : %v", closeErr)
		}
	}()

	raw, err := io.ReadAll(result.Body)
	if err != nil {
		t.Fatalf("lecture du corps de réponse : %v", err)
	}

	return result.StatusCode, result.Header, string(raw)
}

// TestBearerRejects vérifie la porte d'entrée : qui n'a pas de jeton valable
// portant le scope mcp ne parle pas au serveur, et le refus dit où se procurer
// un jeton (RFC 9728) sans dire pourquoi celui-ci a échoué.
func TestBearerRejects(t *testing.T) {
	t.Parallel()

	tb := newTestbed(t)

	cases := []struct {
		name       string
		token      string
		wantStatus int
	}{
		{name: "jeton absent", token: "", wantStatus: http.StatusUnauthorized},
		{name: "jeton inconnu", token: tokenUnknown, wantStatus: http.StatusUnauthorized},
		// Le rôle collaborateur ne porte pas le scope mcp : le jeton est
		// valable, le canal lui est fermé — 403, insufficient_scope (RFC 6750),
		// distinct du 401 d'un jeton invalide.
		{name: "collaborateur sans scope mcp", token: tokenNoMCP, wantStatus: http.StatusForbidden},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			status, header, _ := tb.doRaw(t, http.MethodPost, mcp.ServerPath, tc.token)

			if status != tc.wantStatus {
				t.Fatalf("statut = %d, attendu %d", status, tc.wantStatus)
			}

			authenticate := header.Get("WWW-Authenticate")
			if !strings.HasPrefix(authenticate, "Bearer ") {
				t.Fatalf("WWW-Authenticate = %q, attendu un défi Bearer", authenticate)
			}
			if !strings.Contains(authenticate, wantResourceMetadata) {
				t.Errorf("WWW-Authenticate = %q, ne désigne pas %s", authenticate, wantResourceMetadata)
			}
		})
	}
}

// initializeBody est une requête initialize minimale, telle qu'un client MCP
// l'envoie en premier.
const initializeBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":` +
	`{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}`

// postInitialize joue un initialize brut, avec un en-tête Host imposé — celui
// qu'un reverse proxy transmettrait.
func (tb *testbed) postInitialize(t *testing.T, token, host string) (status int, body string) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		tb.server.URL+mcp.ServerPath, strings.NewReader(initializeBody))
	if err != nil {
		t.Fatalf("construction de la requête : %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if host != "" {
		req.Host = host
	}

	result, err := tb.server.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /mcp : %v", err)
	}
	defer func() {
		if closeErr := result.Body.Close(); closeErr != nil {
			t.Errorf("fermeture du corps de réponse : %v", closeErr)
		}
	}()

	raw, err := io.ReadAll(result.Body)
	if err != nil {
		t.Fatalf("lecture du corps de réponse : %v", err)
	}

	return result.StatusCode, string(raw)
}

// TestReverseProxyHost vérifie que l'adapter sert la configuration de
// déploiement documentée — écoute sur la boucle locale, reverse proxy devant,
// en-tête Host public : la protection anti-DNS-rebinding du SDK est débranchée
// (voir New), et ce qui garde la porte est le Bearer, pas l'en-tête Host.
func TestReverseProxyHost(t *testing.T) {
	t.Parallel()

	tb := newTestbed(t)
	const publicHost = "chantier.exemple.fr"

	// Sans jeton : le refus est celui du Bearer — 401, jamais un 403 rebinding.
	status, _ := tb.postInitialize(t, "", publicHost)
	if status != http.StatusUnauthorized {
		t.Fatalf("sans jeton, Host public : statut = %d, attendu 401", status)
	}

	// Avec jeton : la session s'ouvre, Host public ou pas.
	status, body := tb.postInitialize(t, tokenOwner, publicHost)
	if status != http.StatusOK {
		t.Fatalf("initialize avec Host public : statut = %d, attendu 200 — corps : %s", status, body)
	}
	if !strings.Contains(body, "serverInfo") {
		t.Errorf("initialize n'a pas rendu de serverInfo : %s", body)
	}
}

// TestProtectedResourceMetadata vérifie le document RFC 9728 : public, servi
// aux DEUX adresses well-known — sans chemin, et avec le chemin de la ressource
// insérée comme la norme (§3.1) le forme — et désignant l'URL canonique du
// serveur MCP et le serveur d'autorisation.
func TestProtectedResourceMetadata(t *testing.T) {
	t.Parallel()

	tb := newTestbed(t)

	status, header, body := tb.doRaw(t, http.MethodGet, mcp.ProtectedResourceMetadataPath, "")

	if status != http.StatusOK {
		t.Fatalf("statut = %d, attendu 200 — corps : %s", status, body)
	}
	if contentType := header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Errorf("Content-Type = %q, attendu application/json", contentType)
	}

	var metadata struct {
		Resource               string   `json:"resource"`
		AuthorizationServers   []string `json:"authorization_servers"`
		ScopesSupported        []string `json:"scopes_supported"`
		BearerMethodsSupported []string `json:"bearer_methods_supported"`
	}
	if err := json.Unmarshal([]byte(body), &metadata); err != nil {
		t.Fatalf("document illisible : %v — corps : %s", err, body)
	}

	if want := baseURLTest + "/mcp"; metadata.Resource != want {
		t.Errorf("resource = %q, attendu %q", metadata.Resource, want)
	}
	if len(metadata.AuthorizationServers) != 1 || metadata.AuthorizationServers[0] != baseURLTest {
		t.Errorf("authorization_servers = %v, attendu [%s]", metadata.AuthorizationServers, baseURLTest)
	}
	if len(metadata.BearerMethodsSupported) != 1 || metadata.BearerMethodsSupported[0] != "header" {
		t.Errorf("bearer_methods_supported = %v, attendu [header]", metadata.BearerMethodsSupported)
	}
	if !contains(metadata.ScopesSupported, "mcp") || !contains(metadata.ScopesSupported, "devis:read") {
		t.Errorf("scopes_supported = %v, attendu au moins mcp et devis:read", metadata.ScopesSupported)
	}

	// La forme normative avec chemin sert le MÊME document.
	statusMCP, _, bodyMCP := tb.doRaw(t, http.MethodGet, mcp.ProtectedResourceMetadataPathMCP, "")
	if statusMCP != http.StatusOK {
		t.Fatalf("forme avec chemin : statut = %d, attendu 200 — corps : %s", statusMCP, bodyMCP)
	}
	if bodyMCP != body {
		t.Errorf("les deux adresses well-known rendent des documents différents :\n%s\n%s", body, bodyMCP)
	}
}

// TestCanonicalServerURL fige la forme de l'URL canonique : base sans ou avec
// barre finale, même résultat.
func TestCanonicalServerURL(t *testing.T) {
	t.Parallel()

	for raw, want := range map[string]string{
		"https://chantier.exemple.fr":  "https://chantier.exemple.fr/mcp",
		"https://chantier.exemple.fr/": "https://chantier.exemple.fr/mcp",
	} {
		base, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("URL de test illisible : %v", err)
		}
		if got := mcp.CanonicalServerURL(base); got != want {
			t.Errorf("CanonicalServerURL(%q) = %q, attendu %q", raw, got, want)
		}
	}
}

func contains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}
