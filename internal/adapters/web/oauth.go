package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/fosite/compose"
	"github.com/ory/fosite/handler/oauth2"
	"github.com/ory/fosite/handler/pkce"

	"github.com/Romain-Badino/Avanti/internal/identity"
)

// Chemins du serveur d'autorisation.
//
// Ils sont en anglais, contrairement à /connexion et /deconnexion, et c'est une
// exception délibérée à la règle de docs/ARCHITECTURE.md §4. Le motif tient à
// qui les lit : ces adresses ne sont pas saisies ni choisies par un humain, elles
// sont construites par un logiciel à partir du document de métadonnées, et leurs
// paramètres — client_id, code_challenge, grant_type — sont des noms propres de
// RFC qu'on ne peut pas traduire. Une adresse française menant à des paramètres
// anglais ne serait pas une interface française, seulement une traduction à
// moitié faite. Le chemin de découverte, lui, est imposé au caractère près par la
// RFC 8414.
//
// Ce que l'utilisateur voit vraiment — la page de consentement — est en français
// comme le reste, par le catalogue i18n.
const (
	oauthMetadataPath  = "/.well-known/oauth-authorization-server"
	oauthAuthorizePath = "/oauth/authorize"
	oauthTokenPath     = "/oauth/token" // #nosec G101 -- chemin d'URL, pas un secret.
	oauthRevokePath    = "/oauth/revoke"
	oauthRegisterPath  = "/oauth/register"
)

// Durées de vie des jetons.
//
// Elles vivent ici, en constantes, plutôt qu'en configuration — au même titre
// que [sessionLifetime] et pour la même raison : ce sont des décisions de
// sécurité de l'interface, pas des réglages d'exploitation. Les rendre réglables
// offrirait surtout le moyen de les affaiblir.
const (
	// accessTokenLifetime est court parce qu'un jeton d'accès circule à chaque
	// appel : une heure borne la fenêtre d'un jeton intercepté, sans imposer un
	// rafraîchissement à chaque requête.
	accessTokenLifetime = time.Hour
	// refreshTokenLifetime décide de la fréquence à laquelle un agent doit être
	// réautorisé à la main. Trente jours est le compromis courant entre « on
	// reconsent tous les matins » et « une autorisation oubliée vit une année ».
	// La rotation à chaque usage est ce qui rend cette durée acceptable.
	refreshTokenLifetime = 30 * 24 * time.Hour
	// authorizeCodeLifetime borne le temps entre le consentement et l'échange du
	// code. Le client échange dans la seconde ; cinq minutes couvrent une
	// redirection lente sans laisser traîner un code dans un journal de proxy.
	authorizeCodeLifetime = 5 * time.Minute
)

// OAuthPurgeInterval est la période de suppression des codes et jetons expirés.
//
// La constante est exportée pour la même raison que [SessionCleanupInterval] :
// c'est cmd/avanti qui déclenche le ménage, parce que c'est lui qui décide de la
// vie du processus, mais la période relève de la politique de jetons — elle se
// lit donc à côté des durées de vie qu'elle suit.
//
// Une heure est le pas de la plus courte de ces durées, celle du jeton d'accès :
// balayer plus souvent ne trouverait rien de neuf.
const OAuthPurgeInterval = time.Hour

// oauthSecretMinLength double la validation de AVANTI_OAUTH_SECRET faite au
// chargement de la configuration.
//
// Le doublon est voulu : la bibliothèque de signature refuse une clé plus courte
// à l'exécution, c'est-à-dire à la première demande de jeton. La contrôler ici
// fait échouer la *construction* de l'adapter, donc le démarrage — y compris
// quand celui-ci ne passe pas par internal/platform/config, ce qui est le cas
// des tests.
const oauthSecretMinLength = 32

// OAuthStorage est le magasin que le serveur d'autorisation exige.
//
// Il n'est pas déclaré dans un domaine, contrairement aux autres ports d'Avanti,
// et ce n'est pas un oubli : ces méthodes sont celles de fosite, mot pour mot.
// Les redéclarer dans le domaine y ferait entrer le vocabulaire d'une
// bibliothèque tierce — exactement ce que R1 de docs/ARCHITECTURE.md interdit.
// Le domaine ne voit d'OAuth que [identity.TokenVerifier], et c'est tout ce
// qu'il a besoin d'en savoir.
//
// L'implémentation vit dans adapters/postgres. Les deux familles ne s'importent
// pas pour autant : elles parlent toutes deux le vocabulaire de fosite, et c'est
// cmd/avanti qui construit l'une et l'injecte dans l'autre (R4).
type OAuthStorage interface {
	fosite.ClientManager
	oauth2.CoreStorage
	oauth2.TokenRevocationStorage
	pkce.PKCERequestStorage

	// CreateClient enregistre un client issu de l'enregistrement dynamique. Le
	// nom est à part parce que fosite ne connaît que le protocole, et le
	// protocole ne stocke pas de nom d'affichage.
	CreateClient(ctx context.Context, client *fosite.DefaultClient, name string, registeredAt time.Time) error
	// CountClients rend le nombre de clients enregistrés, pour le plafond que
	// l'enregistrement ouvert impose.
	CountClients(ctx context.Context) (int, error)

	// HasGrant dit si ce compte a déjà autorisé ce client. C'est ce qui permet à
	// la page de consentement de distinguer l'agent employé depuis des mois d'un
	// homonyme enregistré ce matin.
	//
	// Le compte est désigné par la chaîne que porte le sujet de la session
	// OAuth, et non par un identifiant du domaine : c'est le vocabulaire de
	// fosite qui circule ici, comme dans le reste de cette interface.
	HasGrant(ctx context.Context, userID, clientID string) (bool, error)
	// RecordGrant retient qu'un compte a autorisé un client. L'écriture est
	// idempotente : c'est la première fois qui est conservée.
	RecordGrant(ctx context.Context, userID, clientID string, at time.Time) error
}

// namedClient est l'extension facultative par laquelle un client enregistré
// annonce son nom.
//
// L'assertion de type est ce qui évite de faire circuler une structure partagée
// entre adapters/web et adapters/postgres, qui n'ont pas le droit de se
// connaître. Un client qui ne l'implémente pas s'affiche sous son identifiant :
// moins parlant, jamais faux.
type namedClient interface {
	GetName() string
}

// datedClient est l'extension facultative par laquelle un client enregistré
// annonce la date de son enregistrement.
//
// Elle est séparée de [namedClient] plutôt que fondue avec elle : les deux
// informations n'ont pas le même statut. Le nom est déclaré par le client et ne
// vaut que ce que vaut sa parole ; la date est constatée par le serveur. Un
// client qui n'expose pas celle-ci voit simplement la ligne disparaître de la
// page, là où un nom manquant fait afficher l'identifiant à la place.
type datedClient interface {
	GetRegisteredAt() time.Time
}

// oauthServer porte le serveur d'autorisation OAuth 2.1 de l'instance.
type oauthServer struct {
	provider fosite.OAuth2Provider
	storage  OAuthStorage

	// issuer est l'URL publique de l'instance, sans barre finale. C'est la valeur
	// que le document de métadonnées annonce et que les clients comparent
	// octet par octet : elle ne se recalcule nulle part ailleurs.
	issuer string

	limiter *registrationLimiter
	clock   func() time.Time
}

// newOAuthServer câble fosite.
//
// Le *fosite.Config est construit une fois et confié à compose, qui y accumule
// ses gestionnaires : le réutiliser pour un second fournisseur les
// dupliquerait. Il ne sort donc pas de cette fonction.
func newOAuthServer(secret []byte, storage OAuthStorage, baseURL *url.URL, clock func() time.Time) (*oauthServer, error) {
	if storage == nil {
		return nil, errors.New("web : magasin OAuth manquant")
	}
	if len(secret) < oauthSecretMinLength {
		return nil, errors.New("web : clé HMAC OAuth trop courte")
	}
	if clock == nil {
		clock = time.Now
	}

	issuer := strings.TrimSuffix(baseURL.String(), "/")

	cfg := &fosite.Config{
		GlobalSecret:          secret,
		AccessTokenLifespan:   accessTokenLifetime,
		RefreshTokenLifespan:  refreshTokenLifetime,
		AuthorizeCodeLifespan: authorizeCodeLifetime,

		// PKCE obligatoire, S256 seul. EnforcePKCE vaut pour tous les clients, y
		// compris un futur client confidentiel : PKCE protège de l'interception du
		// code, qui n'a rien à voir avec la capacité du client à garder un secret.
		// EnablePKCEPlainChallengeMethod reste faux, donc « plain » est refusé —
		// un défi en clair n'apporte rien qu'un attaquant qui voit le code ne
		// voie aussi.
		EnforcePKCE:                    true,
		EnablePKCEPlainChallengeMethod: false,

		// Comparaison exacte des scopes. Les scopes d'Avanti sont des constantes
		// (« devis:read »), pas une hiérarchie : la stratégie par défaut, qui
		// interprète les points et les jokers, donnerait à « devis » un sens que le
		// domaine ne lui donne pas.
		ScopeStrategy: fosite.ExactScopeStrategy,
		// Aucune audience n'est accordée en V1 (voir oauth_authorize.go) ; la
		// stratégie exacte fait qu'une audience demandée par erreur est refusée
		// plutôt que rapprochée par préfixe.
		AudienceMatchingStrategy: fosite.ExactAudienceMatchingStrategy,

		// Un jeton de rafraîchissement accompagne toute autorisation, sans que le
		// client ait à demander « offline_access ». La valeur par défaut de fosite
		// l'exigerait ; OAuth 2.1 et MCP attendent l'inverse pour un client public,
		// dont le jeton d'accès est court et la rotation obligatoire. La liste vide
		// est significative : nil rétablirait le comportement par défaut.
		RefreshTokenScopes: []string{},

		// Les messages de débogage restent au serveur. Ils citent des identifiants
		// internes et l'état du magasin ; l'appelant reçoit le code d'erreur du
		// protocole et sa description, qui est ce dont il peut faire quelque chose.
		SendDebugMessagesToClients: false,

		AccessTokenIssuer: issuer,
		TokenURL:          issuer + oauthTokenPath,
	}

	provider := compose.Compose(cfg, storage, compose.NewOAuth2HMACStrategy(cfg),
		// L'ordre compte : le gestionnaire PKCE lit le code que le gestionnaire
		// d'autorisation vient d'émettre, et refuse de fonctionner s'il passe
		// avant lui.
		compose.OAuth2AuthorizeExplicitFactory,
		compose.OAuth2PKCEFactory,
		compose.OAuth2RefreshTokenGrantFactory,
		compose.OAuth2TokenRevocationFactory,
		compose.OAuth2TokenIntrospectionFactory,
	)

	return &oauthServer{
		provider: provider,
		storage:  storage,
		issuer:   issuer,
		limiter:  newRegistrationLimiter(clock),
		clock:    clock,
	}, nil
}

// mountOAuth déclare les routes du serveur d'autorisation.
func (h *Handler) mountOAuth() {
	h.mux.HandleFunc("GET "+oauthMetadataPath, h.handleOAuthMetadata)
	h.mux.HandleFunc("POST "+oauthRegisterPath, h.handleOAuthRegister)
	h.mux.HandleFunc("GET "+oauthAuthorizePath, h.handleOAuthAuthorizeForm)
	h.mux.HandleFunc("POST "+oauthAuthorizePath, h.handleOAuthAuthorizeDecision)
	h.mux.HandleFunc("POST "+oauthTokenPath, h.handleOAuthToken)
	h.mux.HandleFunc("POST "+oauthRevokePath, h.handleOAuthRevoke)

	// Les points de terminaison machine acceptent la requête préalable du
	// navigateur, pour les clients MCP qui tournent dans une page plutôt que sur
	// un serveur.
	for _, path := range oauthMachinePaths() {
		h.mux.HandleFunc("OPTIONS "+path, handleOAuthPreflight)
	}
}

// oauthMachinePaths énumère les chemins qu'aucun humain ne visite : ceux qu'un
// logiciel appelle, et qui échappent donc à l'authentification par session
// comme à la protection intersites.
func oauthMachinePaths() []string {
	return []string{oauthMetadataPath, oauthRegisterPath, oauthTokenPath, oauthRevokePath}
}

// isOAuthMachinePath dit si le chemin est celui d'un point de terminaison
// machine.
//
// C'est ce qui les fait échapper à [Handler.requireAuth] : un agent qui vient
// chercher un jeton n'a pas de session, et le rediriger vers /connexion lui
// répondrait une page HTML là où il attend du JSON. /oauth/authorize n'y figure
// pas, et c'est tout l'intérêt : lui exige une session, puisque c'est là que
// l'utilisateur consent.
func isOAuthMachinePath(path string) bool {
	for _, machine := range oauthMachinePaths() {
		if path == machine {
			return true
		}
	}
	return false
}

// handleOAuthPreflight répond à la requête préalable CORS.
func handleOAuthPreflight(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

// setOAuthCORS ouvre les points de terminaison machine à toutes les origines.
//
// C'est sans danger, et il faut voir pourquoi : ces réponses ne contiennent que
// ce que l'appelant a déjà le droit de savoir, et surtout aucun cookie ne les
// accompagne — Access-Control-Allow-Credentials est absent, donc le navigateur
// n'attache pas la session d'Avanti à ces requêtes. Ce qui autorise un client
// n'est pas son origine mais son PKCE et son client_id ; refuser les origines
// tierces n'ajouterait donc aucune sécurité, et empêcherait les clients MCP qui
// tournent dans une page web de découvrir le serveur.
func setOAuthCORS(header http.Header) {
	header.Set("Access-Control-Allow-Origin", "*")
	header.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	header.Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	header.Set("Access-Control-Max-Age", "600")
}

// TokenVerifier rend le vérificateur de jetons de l'instance.
//
// C'est par lui que le futur adapter MCP obtiendra l'identité de son appelant,
// et c'est cmd/avanti qui fera la jonction : l'adapter MCP recevra un
// [identity.TokenVerifier], sans jamais savoir que fosite existe.
func (h *Handler) TokenVerifier() identity.TokenVerifier {
	return &oauthVerifier{provider: h.oauth.provider, accounts: h.accounts}
}

// scopeStrings traduit des scopes du domaine en chaînes du protocole.
func scopeStrings(scopes []identity.Scope) []string {
	out := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		out = append(out, scope.String())
	}
	return out
}
