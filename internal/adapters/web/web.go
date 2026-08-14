package web

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/alexedwards/scs/v2"

	"github.com/Romain-Badino/Avanti/internal/devis"
	"github.com/Romain-Badino/Avanti/internal/document"
	"github.com/Romain-Badino/Avanti/internal/identity"
	"github.com/Romain-Badino/Avanti/internal/platform"
	"github.com/Romain-Badino/Avanti/internal/platform/server"
)

// templatesFS et staticFS embarquent tout ce que le navigateur reçoit. Le
// binaire se suffit à lui-même : pas de répertoire à déployer à côté, pas de
// chaîne de build front, pas de CDN à joindre à l'exécution.
//
//go:embed templates/layout.gohtml templates/pages/*.gohtml
var templatesFS embed.FS

//go:embed static
var staticFS embed.FS

// staticPrefix est le préfixe d'URL sous lequel les assets sont servis.
const staticPrefix = "/static/"

// Options rassemble les dépendances de l'adapter web.
type Options struct {
	// Logger reçoit les erreurs de rendu. Obligatoire.
	Logger *slog.Logger
	// Build estampille le pied de page.
	Build platform.BuildInfo
	// Accounts porte les cas d'usage de l'identité : authentification et lecture du
	// compte connecté. Obligatoire.
	Accounts *identity.AccountService
	// Sessions est le magasin de sessions. Obligatoire.
	//
	// C'est l'interface de scs, pas une implémentation : cmd/avanti choisit le
	// magasin PostgreSQL, et cet adapter n'a donc jamais à connaître pgx. La
	// politique du cookie, elle, est décidée ici — voir
	// [newSessionManager].
	Sessions scs.Store
	// BaseURL est l'URL publique de l'instance. Obligatoire. Elle sert à trois
	// choses : décider du drapeau Secure du cookie, déclarer l'origine de
	// confiance de la protection contre les requêtes intersites, et servir
	// d'issuer au serveur d'autorisation OAuth.
	BaseURL *url.URL
	// OAuthStorage est le magasin du serveur d'autorisation. Obligatoire.
	//
	// Comme Sessions, c'est une interface — celles de fosite — et non une
	// implémentation : cmd/avanti choisit le magasin PostgreSQL et l'injecte,
	// ce qui laisse cet adapter ignorant de pgx.
	OAuthStorage OAuthStorage
	// OAuthSecret est la clé HMAC qui signe codes et jetons. Obligatoire, et au
	// moins [oauthSecretMinLength] octets.
	OAuthSecret []byte
	// Devis porte les cas d'usage de la consultation des artisans. Obligatoire.
	//
	// Le service est construit par cmd/avanti sur le dépôt PostgreSQL : cet
	// adapter ne voit que le domaine, jamais sa persistance (R4).
	Devis *devis.Service
	// Documents porte les cas d'usage des pièces du dossier. Obligatoire.
	//
	// Comme Devis, c'est le domaine que cet adapter voit — jamais le dépôt
	// PostgreSQL des métadonnées ni le stockage du contenu, que cmd/avanti
	// choisit et injecte dans le service (R4).
	Documents *document.Service
	// Clock sert d'horloge au serveur d'autorisation et aux dates proposées par
	// les formulaires. nil signifie time.Now ; les tests en injectent une pour ne
	// pas avoir à attendre l'expiration d'un jeton, et pour que les valeurs
	// pré-remplies soient prévisibles.
	Clock func() time.Time
}

// Handler sert l'interface humaine d'Avanti.
type Handler struct {
	root      http.Handler
	mux       *http.ServeMux
	logger    *slog.Logger
	catalog   *Catalog
	pages     map[string]*template.Template
	version   string
	accounts  *identity.AccountService
	sessions  *scs.SessionManager
	limiter   *loginLimiter
	oauth     *oauthServer
	devis     *devis.Service
	documents *document.Service
	clock     func() time.Time
}

// New construit l'adapter : catalogue de traductions, gabarits compilés une
// fois pour toutes, sessions, routes et service des assets.
//
// Toute erreur de gabarit ou de catalogue est détectée ici, au démarrage, plutôt
// qu'à la première requête d'un utilisateur.
func New(opts Options) (*Handler, error) {
	switch {
	case opts.Logger == nil:
		return nil, errors.New("web : journal manquant")
	case opts.Accounts == nil:
		return nil, errors.New("web : service de comptes manquant")
	case opts.Sessions == nil:
		return nil, errors.New("web : magasin de sessions manquant")
	case opts.BaseURL == nil:
		return nil, errors.New("web : URL publique manquante")
	case opts.OAuthStorage == nil:
		return nil, errors.New("web : magasin OAuth manquant")
	case opts.Devis == nil:
		return nil, errors.New("web : service des devis manquant")
	case opts.Documents == nil:
		return nil, errors.New("web : service des documents manquant")
	}

	catalog, err := NewCatalog()
	if err != nil {
		return nil, err
	}

	pages, err := parsePages()
	if err != nil {
		return nil, err
	}

	oauth, err := newOAuthServer(opts.OAuthSecret, opts.OAuthStorage, opts.BaseURL, opts.Clock)
	if err != nil {
		return nil, err
	}

	h := &Handler{
		mux:       http.NewServeMux(),
		logger:    opts.Logger,
		catalog:   catalog,
		pages:     pages,
		version:   opts.Build.Version,
		accounts:  opts.Accounts,
		sessions:  newSessionManager(opts.Sessions, opts.BaseURL),
		limiter:   newLimiter(nil),
		oauth:     oauth,
		devis:     opts.Devis,
		documents: opts.Documents,
		clock:     opts.Clock,
	}

	h.mux.HandleFunc("GET /{$}", h.handleHome)
	h.mux.HandleFunc("GET "+loginPath, h.handleLoginForm)
	h.mux.HandleFunc("POST "+loginPath, h.handleLogin)
	h.mux.HandleFunc("POST "+logoutPath, h.handleLogout)
	h.mux.Handle("GET "+staticPrefix, http.StripPrefix(staticPrefix, staticFileServer()))
	h.mountDevis()
	h.mountDocuments()
	h.mountOAuth()
	h.mux.HandleFunc("/", h.handleNotFound)

	root, err := h.mountMiddleware(opts.BaseURL)
	if err != nil {
		return nil, err
	}
	h.root = root

	return h, nil
}

// mountMiddleware empile les intergiciels autour du routeur.
//
// L'ordre va de l'extérieur vers l'intérieur, et il n'est pas indifférent :
//
//  1. la protection intersites d'abord, pour qu'une requête refusée le soit avant
//     qu'on ait touché à la session ;
//  2. le chargement de session ensuite, parce que tout ce qui suit en a besoin ;
//  3. l'authentification enfin, qui lit cette session et pose l'acteur dans le
//     contexte de la requête.
func (h *Handler) mountMiddleware(baseURL *url.URL) (http.Handler, error) {
	protection, err := crossOriginProtection(baseURL)
	if err != nil {
		return nil, err
	}

	var root http.Handler = h.mux
	root = h.requireAuth(root)
	root = h.sessions.LoadAndSave(root)
	root = protection.Handler(root)

	return root, nil
}

// crossOriginProtection construit la défense CSRF de la bibliothèque standard.
//
// Go 1.25 a ajouté [http.CrossOriginProtection], et c'est ce qui est utilisé ici
// plutôt qu'un jeton synchronisé maison. Son principe : refuser toute requête non
// sûre — POST, PUT, DELETE — dont l'en-tête Sec-Fetch-Site annonce une origine
// tierce, ou dont l'Origin ne correspond pas à l'hôte. Sec-Fetch-Site est présent
// dans tous les navigateurs depuis 2023.
//
// Ce que cela remplace : un jeton CSRF dans chaque formulaire, avec sa réserve
// côté session, sa rotation et ses cas particuliers. Ce que cela ne remplace pas :
// SameSite=Lax sur le cookie, qui reste posé — les deux mécanismes couvrent la
// même attaque par des chemins différents, et un client qui échapperait à l'un
// devrait encore passer l'autre.
//
// L'URL publique est déclarée origine de confiance : derrière un reverse proxy,
// l'en-tête Host vu par le processus n'est pas toujours celui que le navigateur a
// mis dans Origin, et sans cette déclaration la comparaison échouerait sur des
// requêtes parfaitement légitimes.
func crossOriginProtection(baseURL *url.URL) (*http.CrossOriginProtection, error) {
	protection := http.NewCrossOriginProtection()

	origin := baseURL.Scheme + "://" + baseURL.Host
	if err := protection.AddTrustedOrigin(origin); err != nil {
		return nil, fmt.Errorf("web : origine de confiance %q refusée : %w", origin, err)
	}

	// Les points de terminaison machine du serveur d'autorisation en sont
	// dispensés, et c'est un raisonnement à faire explicitement plutôt qu'une
	// commodité.
	//
	// La protection intersites défend une session contre un site tiers qui la
	// ferait agir à l'insu de son porteur. Ces trois routes n'ont pas de session
	// à défendre : elles n'en lisent aucune, n'en posent aucune, et ce qui les
	// autorise est le vérificateur PKCE ou l'identifiant du client. Les protéger
	// n'ajouterait donc rien, tandis que les laisser sous la règle casserait le
	// cas normal — un agent qui appelle depuis son serveur, ou une page web d'un
	// autre domaine, se verrait refuser sa demande de jeton.
	//
	// /oauth/authorize n'y figure pas : c'est un formulaire, soumis par un
	// navigateur porteur de session, et il a exactement besoin de cette
	// protection.
	for _, path := range oauthMachinePaths() {
		protection.AddInsecureBypassPattern(path)
	}

	return protection, nil
}

// ServeHTTP applique les en-têtes de sécurité communs puis route la requête au
// travers de la chaîne d'intergiciels.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w.Header())
	h.root.ServeHTTP(w, r)
}

func (h *Handler) handleHome(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, pageHome, http.StatusOK, nil)
}

func (h *Handler) handleNotFound(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, pageNotFound, http.StatusNotFound, nil)
}

// Les gabarits de page, désignés par leur nom de fichier.
const (
	pageHome          = "home.gohtml"
	pageLogin         = "login.gohtml"
	pageNotFound      = "not_found.gohtml"
	pageInternalError = "internal_error.gohtml"
	pageOAuthConsent  = "oauth_consent.gohtml"
	pageOAuthRefused  = "oauth_refused.gohtml"
	pageForbidden     = "acces_refuse.gohtml"

	pageDevisIndex           = "devis_index.gohtml"
	pageDevisComparaison     = "devis_comparaison.gohtml"
	pageDevisNouvelleDemande = "devis_nouvelle_demande.gohtml"

	pageDocumentsIndex = "documents_index.gohtml"
)

// render écrit la page demandée, et bascule sur la page d'erreur si le rendu
// échoue. Le dernier recours est un texte brut : si même la page d'erreur ne se
// rend pas, insister ne ferait que boucler.
//
// donnees est la charge utile propre à la page ; nil pour celles qui n'en ont pas.
func (h *Handler) render(w http.ResponseWriter, r *http.Request, page string, status int, data any) {
	if err := h.write(w, r, page, status, data); err != nil {
		h.fail(r, err)

		if err := h.write(w, r, pageInternalError, http.StatusInternalServerError, nil); err != nil {
			h.fail(r, err)
			http.Error(w, "Erreur interne du serveur.", http.StatusInternalServerError)
		}
	}
}

// write rend la page dans un tampon avant d'écrire quoi que ce soit au client.
// Sans ce détour, une erreur de gabarit survenant au milieu du document
// produirait une page tronquée servie avec un code 200 — et rien ne permettrait
// plus, l'en-tête étant parti, de la remplacer par une page d'erreur.
func (h *Handler) write(w http.ResponseWriter, r *http.Request, page string, status int, data any) error {
	tmpl, ok := h.pages[page]
	if !ok {
		return fmt.Errorf("gabarit inconnu : %s", page)
	}

	translator := h.catalog.Translator(r.Header.Get("Accept-Language"))

	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, h.newView(translator, r, data)); err != nil {
		return fmt.Errorf("rendu du gabarit %s : %w", page, err)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Aucune page HTML d'Avanti n'est cachable : toutes dépendent de qui est
	// connecté. Un cache intermédiaire — ou le simple bouton « précédent » —
	// pourrait sinon montrer à la personne suivante le tableau de bord de la
	// précédente.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)

	if _, err := rendered.WriteTo(w); err != nil {
		// L'en-tête est parti : il n'y a plus de page de repli possible, seule
		// reste la trace au journal.
		h.fail(r, fmt.Errorf("écriture de la réponse : %w", err))
	}

	return nil
}

func (h *Handler) fail(r *http.Request, err error) {
	h.logger.ErrorContext(r.Context(), "échec du rendu web",
		slog.String("path", r.URL.Path),
		slog.String("request_id", server.RequestIDFromContext(r.Context())),
		slog.String("error", err.Error()))
}

// parsePages compile un jeu de gabarits par page : chacun combine le gabarit
// commun et la page elle-même. Les compiler ensemble ferait entrer en collision
// les blocs « content » que chaque page définit.
func parsePages() (map[string]*template.Template, error) {
	files, err := fs.Glob(templatesFS, "templates/pages/*.gohtml")
	if err != nil {
		return nil, fmt.Errorf("lecture des gabarits de page : %w", err)
	}
	if len(files) == 0 {
		return nil, errors.New("web : aucun gabarit de page embarqué")
	}

	pages := make(map[string]*template.Template, len(files))
	for _, file := range files {
		tmpl, err := template.New("layout.gohtml").ParseFS(templatesFS, "templates/layout.gohtml", file)
		if err != nil {
			return nil, fmt.Errorf("compilation du gabarit %s : %w", path.Base(file), err)
		}
		pages[path.Base(file)] = tmpl
	}

	return pages, nil
}

// staticFileServer sert les assets embarqués. La mise en cache est courte et
// franche : les URLs portent l'estampille de build en paramètre, donc un
// nouveau binaire invalide les anciennes entrées sans qu'on ait à raisonner sur
// des durées de vie.
func staticFileServer() http.Handler {
	assets, err := fs.Sub(staticFS, "static")
	if err != nil {
		// Inatteignable : le chemin est une constante de compilation, vérifiée
		// par la directive //go:embed juste au-dessus.
		panic(fmt.Sprintf("web : assets embarqués introuvables : %v", err))
	}

	files := http.FileServerFS(assets)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		files.ServeHTTP(w, r)
	})
}

// setSecurityHeaders pose les garde-fous que le navigateur sait appliquer. La
// politique de sécurité du contenu est stricte et le reste : Avanti n'a ni
// script en ligne, ni ressource distante, ce qui rend « 'self' » suffisant
// partout et interdit d'avance l'injection d'un script tiers.
func setSecurityHeaders(header http.Header) {
	header.Set("Content-Security-Policy", strings.Join([]string{
		"default-src 'self'",
		"script-src 'self'",
		"style-src 'self'",
		"img-src 'self' data:",
		"form-action 'self'",
		"frame-ancestors 'none'",
		"base-uri 'none'",
	}, "; "))
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", "same-origin")
	header.Set("Cross-Origin-Opener-Policy", "same-origin")
}
