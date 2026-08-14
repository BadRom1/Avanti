package mcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"github.com/Romain-Badino/Avanti/internal/devis"
	"github.com/Romain-Badino/Avanti/internal/document"
	"github.com/Romain-Badino/Avanti/internal/finance"
	"github.com/Romain-Badino/Avanti/internal/identity"
	"github.com/Romain-Badino/Avanti/internal/planning"
	"github.com/Romain-Badino/Avanti/internal/platform"
)

// Chemins servis par cet adapter. Ils sont exportés parce que c'est cmd/avanti
// qui compose le routage racine (R4) : lui seul décide quels chemins arrivent
// ici plutôt qu'à l'adapter web.
const (
	// ServerPath est le point de terminaison MCP : transport HTTP streamable,
	// tout passe par POST — le serveur est sans session (voir [New]), donc GET
	// et DELETE rendent 405. En anglais comme les chemins OAuth, et pour la
	// même raison (voir adapters/web/oauth.go) : cette adresse est construite
	// par un logiciel, jamais saisie par un humain.
	ServerPath = "/mcp"
	// ProtectedResourceMetadataPath est le document de découverte de la
	// RFC 9728, public, imposé au caractère près par la norme. C'est lui que
	// l'en-tête WWW-Authenticate des refus désigne : un client MCP qui reçoit
	// 401 y lit quel serveur d'autorisation joindre.
	ProtectedResourceMetadataPath = "/.well-known/oauth-protected-resource"
	// ProtectedResourceMetadataPathMCP est la seconde adresse du MÊME document,
	// exigée par la RFC 9728 §3.1 pour une ressource dont l'URL porte un
	// chemin : le préfixe well-known s'insère AVANT le chemin de la ressource —
	// pour <base>/mcp, c'est /.well-known/oauth-protected-resource/mcp. Un
	// client qui forme l'URI par la règle de la norme doit y trouver le
	// document, pas un 404.
	ProtectedResourceMetadataPathMCP = ProtectedResourceMetadataPath + ServerPath
)

// CanonicalServerURL rend l'URL canonique du serveur MCP d'une instance : sa
// BaseURL suivie de [ServerPath], sans barre finale intermédiaire.
//
// C'est la valeur que le document RFC 9728 annonce comme « resource », et la
// seule que l'indicateur de ressource (RFC 8707) du serveur d'autorisation
// accepte. Elle est calculée ici — et transmise à l'adapter web par cmd/avanti,
// les deux familles ne s'important pas (R4) — pour qu'il n'existe qu'une
// définition de cette URL dans le dépôt.
func CanonicalServerURL(base *url.URL) string {
	return strings.TrimSuffix(base.String(), "/") + ServerPath
}

// serverInstructions est le texte d'accueil rendu au client MCP à
// l'initialisation. En français, comme tout l'user-visible de ce canal.
const serverInstructions = "Avanti suit la reconstruction d'une maison : consultations d'artisans " +
	"(devis), planning du chantier, finances et pièces du dossier. Les montants sont des centimes " +
	"d'euro entiers, les dates au format AAAA-MM-JJ. Chaque tool exige un scope de domaine ; " +
	"aucun envoi (mail, transmission à l'assurance) n'est jamais effectué par ce serveur."

// Options rassemble les dépendances de l'adapter MCP.
type Options struct {
	// Logger reçoit les pannes techniques des tools et de la vérification de
	// jeton. Obligatoire.
	Logger *slog.Logger
	// Build estampille la version annoncée au client MCP.
	Build platform.BuildInfo
	// BaseURL est l'URL publique de l'instance. Obligatoire. Elle fixe l'URL
	// canonique du serveur ([CanonicalServerURL]) et l'adresse du serveur
	// d'autorisation que le document RFC 9728 annonce.
	BaseURL *url.URL
	// Verifier traduit un jeton d'accès en acteur. Obligatoire.
	//
	// C'est la SEULE porte de cet adapter vers l'identité : l'implémentation
	// vit dans adapters/web, où le serveur d'autorisation est monté, et c'est
	// cmd/avanti qui la fait circuler (R4). Cet adapter ne sait pas que fosite
	// existe.
	Verifier identity.TokenVerifier
	// Devis porte les cas d'usage de la consultation des artisans. Obligatoire.
	Devis *devis.Service
	// Documents porte les cas d'usage des pièces du dossier. Obligatoire.
	Documents *document.Service
	// Finance porte les cas d'usage de l'argent du chantier. Obligatoire.
	Finance *finance.Service
	// Planning porte les cas d'usage de l'ordonnancement. Obligatoire.
	Planning *planning.Service
	// Clock donne l'heure courante — statuts dérivés du planning, date du
	// dossier d'assurance. Nil signifie time.Now.
	Clock func() time.Time
}

// Handler sert l'interface agent d'Avanti : le point de terminaison MCP et le
// document Protected Resource Metadata.
type Handler struct {
	mux       *http.ServeMux
	logger    *slog.Logger
	verifier  identity.TokenVerifier
	devis     *devis.Service
	documents *document.Service
	finance   *finance.Service
	planning  *planning.Service
	clock     func() time.Time
	// baseHost est l'hôte de l'URL publique — la donnée qui nomme l'instance
	// dans le dossier d'assurance, comme dans l'export web.
	baseHost string
}

// New construit l'adapter : serveur MCP, tools, vérification du jeton et
// document de découverte, tous câblés au démarrage.
func New(opts Options) (*Handler, error) {
	if err := checkOptions(opts); err != nil {
		return nil, err
	}

	h := &Handler{
		mux:       http.NewServeMux(),
		logger:    opts.Logger,
		verifier:  opts.Verifier,
		devis:     opts.Devis,
		documents: opts.Documents,
		finance:   opts.Finance,
		planning:  opts.Planning,
		clock:     opts.Clock,
		baseHost:  opts.BaseURL.Host,
	}
	if h.clock == nil {
		h.clock = time.Now
	}

	server := sdk.NewServer(&sdk.Implementation{
		Name:    "avanti",
		Title:   "Avanti",
		Version: opts.Build.Version,
	}, &sdk.ServerOptions{
		Instructions: serverInstructions,
		Logger:       opts.Logger,
	})
	h.addTools(server)

	// Le transport est sans session MCP (stateless) : chaque requête porte son
	// jeton et se suffit à elle-même, ce qui est exactement le modèle d'une API
	// machine — rien d'autre que le jeton n'authentifie, rien n'est retenu
	// entre deux requêtes. C'est aussi la direction de la spécification MCP
	// (SEP-2567). JSONResponse rend des réponses application/json plutôt qu'un
	// flux d'événements, ce qu'un client requête-réponse préfère.
	streamable := sdk.NewStreamableHTTPHandler(
		func(*http.Request) *sdk.Server { return server },
		&sdk.StreamableHTTPOptions{
			Stateless:    true,
			JSONResponse: true,
			Logger:       opts.Logger,
			// La protection anti-DNS-rebinding du SDK refuse toute requête
			// arrivée par une adresse de boucle locale avec un en-tête Host
			// public — exactement la forme d'un déploiement derrière reverse
			// proxy (.env.example recommande d'écouter sur 127.0.0.1, nginx ou
			// Caddy transmettent le Host public) : chaque requête /mcp rendrait
			// 403 avant même la vérification du jeton. Elle est débranchée en
			// raisonnant, pas par commodité : le rebinding DNS permet à un site
			// visité d'atteindre un serveur local SANS credentials — or ici
			// rien ne répond sans jeton Bearer, qu'un attaquant par rebinding
			// n'a pas. La protection ne défendrait rien que le Bearer ne
			// défende déjà, et casserait le déploiement documenté.
			DisableLocalhostProtection: true,
		},
	)

	issuer := strings.TrimSuffix(opts.BaseURL.String(), "/")

	// La vérification du Bearer est celle du SDK (RFC 6750 + RFC 9728) : jeton
	// absent ou invalide → 401, scope mcp manquant → 403, les deux avec
	// l'en-tête WWW-Authenticate qui pointe vers le document de découverte.
	requireBearer := auth.RequireBearerToken(h.verifyBearer, &auth.RequireBearerTokenOptions{
		ResourceMetadataURL: issuer + ProtectedResourceMetadataPath,
		Scopes:              []string{identity.ScopeMCP.String()},
		// L'expiration n'est pas portée par l'acteur : c'est fosite qui la
		// vérifie — parmi le reste — à CHAQUE appel de VerifyToken, et un jeton
		// expiré ne produit jamais d'acteur. Exiger en plus une date ici ferait
		// refuser tous les jetons pour une information que le port, à dessein,
		// ne divulgue pas.
		AllowMissingExpiration: true,
	})

	h.mux.Handle(ServerPath, requireBearer(streamable))

	// Le document RFC 9728 est servi aux DEUX adresses : la forme sans chemin,
	// que WWW-Authenticate désigne, et la forme normative avec chemin (§3.1),
	// qu'un client peut former lui-même depuis l'URL de la ressource.
	metadata := auth.ProtectedResourceMetadataHandler(
		&oauthex.ProtectedResourceMetadata{
			Resource:               CanonicalServerURL(opts.BaseURL),
			AuthorizationServers:   []string{issuer},
			ScopesSupported:        scopeStrings(identity.AllScopes()),
			BearerMethodsSupported: []string{"header"},
			ResourceName:           "Avanti — serveur MCP",
		})
	h.mux.Handle(ProtectedResourceMetadataPath, metadata)
	h.mux.Handle(ProtectedResourceMetadataPathMCP, metadata)

	return h, nil
}

// checkOptions vérifie les dépendances obligatoires au démarrage plutôt qu'à la
// première requête qui en aurait besoin.
func checkOptions(opts Options) error {
	switch {
	case opts.Logger == nil:
		return errors.New("mcp : journal manquant")
	case opts.BaseURL == nil:
		return errors.New("mcp : URL publique manquante")
	case opts.Verifier == nil:
		return errors.New("mcp : vérificateur de jetons manquant")
	case opts.Devis == nil:
		return errors.New("mcp : service des devis manquant")
	case opts.Documents == nil:
		return errors.New("mcp : service des documents manquant")
	case opts.Finance == nil:
		return errors.New("mcp : service des finances manquant")
	case opts.Planning == nil:
		return errors.New("mcp : service du planning manquant")
	default:
		return nil
	}
}

// ServeHTTP route la requête vers le point de terminaison MCP ou le document de
// découverte. Tout autre chemin rend 404 — c'est cmd/avanti qui décide de ce
// qui arrive ici, et il n'envoie que ces deux chemins.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

// extraKeyActor est la clé sous laquelle l'acteur voyage dans le TokenInfo du
// SDK, de la vérification du jeton jusqu'aux tools.
const extraKeyActor = "actor"

// verifyBearer adapte [identity.TokenVerifier] au contrat du SDK.
//
// Un jeton refusé par le port rend l'erreur sentinelle du SDK, donc 401 — sans
// détail, comme le port lui-même : distinguer expiré, révoqué ou inconnu
// renseignerait qui essaie des jetons. Une panne technique (base injoignable)
// se journalise et rend 500 : ce n'est pas un refus d'authentification, et le
// client ne doit pas repartir demander un jeton neuf.
func (h *Handler) verifyBearer(ctx context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
	actor, err := h.verifier.VerifyToken(ctx, token)
	switch {
	case errors.Is(err, identity.ErrInvalidToken):
		return nil, auth.ErrInvalidToken
	case err != nil:
		h.logger.ErrorContext(ctx, "vérification d'un jeton MCP",
			slog.String("error", err.Error()))
		return nil, errors.New("vérification du jeton impossible")
	}

	return &auth.TokenInfo{
		Scopes: scopeStrings(actor.Scopes()),
		UserID: actor.UserID().String(),
		// L'acteur entier voyage jusqu'aux tools : c'est lui qui décide, par
		// scope, ce que chaque tool accepte de faire.
		Extra: map[string]any{extraKeyActor: actor},
	}, nil
}

// actorFrom extrait l'acteur que verifyBearer a posé dans la requête.
//
// L'absence est une incohérence interne — le middleware garantit qu'aucune
// requête n'atteint un tool sans jeton vérifié — traitée comme telle : erreur
// générique, jamais un acteur anonyme qui « échouerait poliment ».
func actorFrom(req *sdk.CallToolRequest) (identity.Actor, error) {
	if req == nil || req.Extra == nil || req.Extra.TokenInfo == nil {
		return identity.Actor{}, errors.New("mcp : requête sans jeton vérifié")
	}

	actor, ok := req.Extra.TokenInfo.Extra[extraKeyActor].(identity.Actor)
	if !ok {
		return identity.Actor{}, errors.New("mcp : acteur absent du jeton vérifié")
	}

	return actor, nil
}

// requireScope rend l'acteur de la requête s'il détient le scope, et sinon une
// erreur de tool qui NOMME le scope manquant : un agent doit lire pourquoi il
// est refusé, jamais recevoir un résultat vide qui ressemblerait à un chantier
// sans données.
func (h *Handler) requireScope(ctx context.Context, req *sdk.CallToolRequest, scope identity.Scope) (identity.Actor, error) {
	actor, err := actorFrom(req)
	if err != nil {
		h.logger.ErrorContext(ctx, "acteur introuvable dans une requête MCP",
			slog.String("error", err.Error()))
		return identity.Actor{}, errInternal
	}

	if !actor.Allows(scope) {
		return identity.Actor{}, fmt.Errorf(
			"scope %s requis : le jeton ne porte pas ce droit", scope)
	}

	return actor, nil
}

// scopeStrings traduit des scopes du domaine en chaînes du protocole.
func scopeStrings(scopes []identity.Scope) []string {
	out := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		out = append(out, scope.String())
	}
	return out
}

// dateLayout est le format des dates saisies et rendues par les tools : la
// forme ISO du jour, sans heure — la granularité de tout ce qui se saisit dans
// Avanti.
const dateLayout = "2006-01-02"

// parseDate lit une date AAAA-MM-JJ et rend une erreur en français — c'est un
// refus que l'agent peut corriger — quand la forme n'y est pas.
func parseDate(raw, label string) (time.Time, error) {
	date, err := time.Parse(dateLayout, strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}, fmt.Errorf("date %s illisible : %q — format attendu AAAA-MM-JJ", label, raw)
	}
	return date, nil
}

// formatDate rend une date au format AAAA-MM-JJ, ou la chaîne vide pour la
// valeur zéro — « pas encore », dans le vocabulaire des domaines.
func formatDate(date time.Time) string {
	if date.IsZero() {
		return ""
	}
	return date.UTC().Format(dateLayout)
}
