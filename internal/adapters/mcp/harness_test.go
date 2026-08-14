// Harnais des tests de l'adapter MCP.
//
// Les tests exercent le gestionnaire complet — vérification du Bearer comprise —
// au travers d'un vrai serveur HTTP et du client MCP du SDK : c'est la seule
// façon de vérifier que l'authentification, le transport streamable et les
// tools sont empilés dans le bon ordre.
//
// Les services de domaine sont réels, montés sur des dépôts en mémoire — le
// modèle des fakes de l'adapter web : un fake plus permissif que le dépôt
// PostgreSQL ne prouverait rien sur les refus que les tools distinguent.
package mcp_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Romain-Badino/Avanti/internal/adapters/mcp"
	"github.com/Romain-Badino/Avanti/internal/devis"
	"github.com/Romain-Badino/Avanti/internal/document"
	"github.com/Romain-Badino/Avanti/internal/finance"
	"github.com/Romain-Badino/Avanti/internal/identity"
	"github.com/Romain-Badino/Avanti/internal/planning"
	"github.com/Romain-Badino/Avanti/internal/platform"
	"github.com/Romain-Badino/Avanti/internal/platform/logging"
)

// baseURLTest est l'URL publique déclarée à la construction. Elle ne sert
// qu'aux documents que l'adapter annonce (WWW-Authenticate, RFC 9728) : les
// requêtes, elles, passent par le serveur de test.
const baseURLTest = "http://avanti.test"

// Jetons du vérificateur factice.
const (
	tokenOwner       = "jeton-proprietaire-de-test"
	tokenMCPOnly     = "jeton-scope-mcp-seul"
	tokenNoMCP       = "jeton-collaborateur-sans-mcp"
	tokenUnknown     = "jeton-inconnu"
	testOwnerUserID  = "compte-proprietaire"
	testCollabUserID = "compte-collaborateur"
)

// fakeVerifier est un [identity.TokenVerifier] à table fixe : les tests de cet
// adapter vérifient ce qu'il FAIT d'un acteur, pas comment le jeton est signé —
// ça, c'est le travail de l'adapter web et du bout-en-bout de cmd/avanti.
type fakeVerifier struct {
	actors map[string]identity.Actor
}

func (v *fakeVerifier) VerifyToken(_ context.Context, token string) (identity.Actor, error) {
	actor, ok := v.actors[token]
	if !ok {
		return identity.Actor{}, identity.ErrInvalidToken
	}
	return actor, nil
}

// testbed est l'adapter sous test et ses dépôts, pour semer et vérifier.
type testbed struct {
	server   *httptest.Server
	devis    *memDevisRepo
	finance  *memFinanceRepo
	planning *memPlanningRepo
	document *memDocumentRepo

	devisService    *devis.Service
	financeService  *finance.Service
	planningService *planning.Service
	documentService *document.Service
}

// newTestbed monte l'adapter complet derrière un serveur HTTP de test.
//
// Trois jetons sont câblés :
//   - tokenOwner : un propriétaire, tous les scopes de son rôle ;
//   - tokenMCPOnly : le scope mcp SEUL — un acteur forgé par
//     [identity.NewActorWithScopes], parce qu'aucun rôle réel ne porte mcp sans
//     scope de domaine : c'est le seul moyen de tester le refus par scope de
//     domaine, et c'est assumé ;
//   - tokenNoMCP : un collaborateur — son rôle ne porte pas mcp, la connexion
//     même est refusée.
func newTestbed(t *testing.T) *testbed {
	t.Helper()

	devisRepo := newMemDevisRepo()
	devisService, err := devis.NewService(devis.ServiceOptions{Repo: devisRepo})
	if err != nil {
		t.Fatalf("devis.NewService() échoué : %v", err)
	}

	financeRepo := newMemFinanceRepo()
	financeService, err := finance.NewService(finance.ServiceOptions{Repo: financeRepo})
	if err != nil {
		t.Fatalf("finance.NewService() échoué : %v", err)
	}

	planningRepo := newMemPlanningRepo()
	planningService, err := planning.NewService(planning.ServiceOptions{Repo: planningRepo})
	if err != nil {
		t.Fatalf("planning.NewService() échoué : %v", err)
	}

	documentRepo := newMemDocumentRepo()
	documentService, err := document.NewService(document.ServiceOptions{
		Repo:    documentRepo,
		Storage: newMemDocumentStorage(),
	})
	if err != nil {
		t.Fatalf("document.NewService() échoué : %v", err)
	}

	baseURL, err := url.Parse(baseURLTest)
	if err != nil {
		t.Fatalf("URL de test illisible : %v", err)
	}

	verifier := &fakeVerifier{actors: map[string]identity.Actor{
		tokenOwner: identity.NewActor(testOwnerUserID, identity.RoleProprietaire),
		tokenMCPOnly: identity.NewActorWithScopes(testOwnerUserID, identity.RoleProprietaire,
			[]identity.Scope{identity.ScopeMCP}),
		tokenNoMCP: identity.NewActor(testCollabUserID, identity.RoleCollaborateur),
	}}

	handler, err := mcp.New(mcp.Options{
		Logger:    logging.Discard(),
		Build:     platform.BuildInfo{Version: "v0.0.0-test"},
		BaseURL:   baseURL,
		Verifier:  verifier,
		Devis:     devisService,
		Documents: documentService,
		Finance:   financeService,
		Planning:  planningService,
	})
	if err != nil {
		t.Fatalf("mcp.New() échoué : %v", err)
	}

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return &testbed{
		server:          server,
		devis:           devisRepo,
		finance:         financeRepo,
		planning:        planningRepo,
		document:        documentRepo,
		devisService:    devisService,
		financeService:  financeService,
		planningService: planningService,
		documentService: documentService,
	}
}

// bearerTransport ajoute le jeton à chaque requête du client MCP.
type bearerTransport struct {
	token string
}

func (t bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return http.DefaultTransport.RoundTrip(clone)
}

// connect ouvre une session MCP avec le jeton donné, par le client du SDK.
func (tb *testbed) connect(t *testing.T, token string) *sdk.ClientSession {
	t.Helper()

	client := sdk.NewClient(&sdk.Implementation{Name: "test-agent", Version: "v0.0.0"}, nil)
	session, err := client.Connect(t.Context(), &sdk.StreamableClientTransport{
		Endpoint:   tb.server.URL + mcp.ServerPath,
		HTTPClient: &http.Client{Transport: bearerTransport{token: token}, Timeout: 30 * time.Second},
		// Le serveur est sans session MCP : pas de flux d'événements autonome à
		// établir, un GET rendrait 405.
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connexion MCP : %v", err)
	}
	t.Cleanup(func() {
		if closeErr := session.Close(); closeErr != nil {
			t.Errorf("fermeture de la session MCP : %v", closeErr)
		}
	})

	return session
}

// callTool appelle un tool et rend son résultat, erreurs de protocole exclues.
func callTool(t *testing.T, session *sdk.ClientSession, name string, args map[string]any) *sdk.CallToolResult {
	t.Helper()

	result, err := session.CallTool(t.Context(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("tools/call %s : erreur de protocole : %v", name, err)
	}

	return result
}

// resultText rassemble le texte du résultat, où vivent les messages d'erreur
// des tools.
func resultText(result *sdk.CallToolResult) string {
	var out strings.Builder
	for _, content := range result.Content {
		if text, ok := content.(*sdk.TextContent); ok {
			out.WriteString(text.Text)
		}
	}
	return out.String()
}

// seedComparaison sème une demande avec un devis reçu et rend les deux.
func (tb *testbed) seedComparaison(t *testing.T) (devis.DemandeDevis, devis.Devis) {
	t.Helper()

	demande, err := tb.devisService.CreateDemande(t.Context(), devis.DemandeInput{
		Lot:    "Charpente",
		SentAt: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		By:     "seed",
	})
	if err != nil {
		t.Fatalf("création de la demande : %v", err)
	}

	proposition, err := tb.devisService.RecordDevis(t.Context(), devis.DevisInput{
		DemandeID:  demande.ID,
		Artisan:    devis.Artisan{Entreprise: "Charpentes du Val"},
		Montant:    1_180_050, // 11 800,50 €
		ReceivedAt: time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC),
		By:         "seed",
	})
	if err != nil {
		t.Fatalf("enregistrement du devis : %v", err)
	}

	return demande, proposition
}

// seedEtape sème une étape avec les prérequis donnés.
func (tb *testbed) seedEtape(t *testing.T, name string, dependsOn ...planning.ID) planning.Etape {
	t.Helper()

	etape, err := tb.planningService.CreateEtape(t.Context(), planning.EtapeInput{
		Name:         name,
		PlannedStart: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		PlannedEnd:   time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC),
		DependsOn:    dependsOn,
		By:           "seed",
	})
	if err != nil {
		t.Fatalf("création de l'étape %s : %v", name, err)
	}

	return etape
}
