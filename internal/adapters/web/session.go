package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/alexedwards/scs/v2"

	"github.com/Romain-Badino/Avanti/internal/identity"
)

// Politique du cookie de session.
const (
	// sessionCookieName est distinctif exprès : « session » tout court entrerait en
	// collision avec le cookie d'une autre application servie sur le même hôte.
	sessionCookieName = "avanti_session"
	// sessionLifetime est la durée de vie absolue d'une session : au-delà, il faut se
	// reconnecter, même en ayant travaillé sans interruption.
	sessionLifetime = 12 * time.Hour
	// sessionIdleTimeout est le délai au bout duquel une session inutilisée
	// s'éteint. Douze heures pour la durée totale et deux heures d'inactivité
	// conviennent à un usage de bureau : on ouvre le matin, on referme le soir, et
	// un portable oublié dans un train ne reste pas connecté toute la journée.
	sessionIdleTimeout = 2 * time.Hour
)

// SessionCleanupInterval est la période de suppression des sessions
// expirées en base.
//
// La constante est exportée parce que c'est cmd/avanti qui construit le magasin
// concret, mais que la durée relève de la politique de session — elle appartient
// donc au même fichier que [sessionLifetime] et [sessionIdleTimeout], où on la relit
// en les relisant.
const SessionCleanupInterval = 10 * time.Minute

// sessionKeyUserID est la seule donnée que porte une session : l'identifiant du
// compte connecté.
//
// Le rôle et les scopes n'y sont pas, et c'est délibéré. Ils sont relus depuis la
// base à chaque requête, de sorte qu'une désactivation ou un changement de rôle
// prenne effet immédiatement au lieu d'attendre l'expiration des sessions déjà
// ouvertes. Le prix est une lecture par requête sur une table de trois lignes.
const sessionKeyUserID = "user_id"

// newSessionManager configure scs.
//
// Les drapeaux du cookie sont posés ici parce que ce sont des décisions de
// l'interface web, pas du magasin : c'est cet adapter qui sait qu'il sert des
// formulaires à un navigateur.
func newSessionManager(store scs.Store, baseURL *url.URL) *scs.SessionManager {
	sessions := scs.New()
	sessions.Store = store
	sessions.Lifetime = sessionLifetime
	sessions.IdleTimeout = sessionIdleTimeout

	sessions.Cookie.Name = sessionCookieName
	sessions.Cookie.Path = "/"
	// HttpOnly : le cookie est invisible de JavaScript, donc hors d'atteinte d'une
	// injection de script. La CSP interdit déjà tout script tiers ; les deux
	// mesures se recouvrent, et c'est ce qu'on veut d'une défense en profondeur.
	sessions.Cookie.HttpOnly = true
	// SameSite=Lax : le cookie ne part pas sur une requête POST venue d'un autre
	// site, ce qui coupe la forme la plus courante de CSRF. Lax plutôt que Strict
	// pour qu'un lien reçu par courriel amène sur une page déjà connectée.
	sessions.Cookie.SameSite = http.SameSiteLaxMode
	// Secure suit le schéma de l'URL publique : l'imposer en développement sur
	// http://localhost empêcherait toute connexion, et le désactiver en production
	// laisserait le cookie voyager en clair. Le déduire évite d'avoir à choisir.
	sessions.Cookie.Secure = baseURL.Scheme == "https"
	// Persist : le cookie survit à la fermeture du navigateur, dans la limite de
	// Lifetime. Sans cela, chaque redémarrage du navigateur demanderait le mot de
	// passe, ce qui pousse à en choisir un court.
	sessions.Cookie.Persist = true

	return sessions
}

// Chemins de l'authentification. Ils sont en français, comme toutes les URLs
// visibles d'Avanti.
const (
	loginPath  = "/connexion"
	logoutPath = "/deconnexion"
	homePath   = "/"
)

// paramNext porte la page demandée avant la redirection vers le formulaire,
// pour y revenir après la connexion.
const paramNext = "suite"

// actorKey est la clé de l'acteur dans le contexte de requête. Un type privé
// plutôt qu'une chaîne : aucun autre paquet ne peut fabriquer la même clé, donc
// ni lire ni écrire l'acteur par accident.
type actorKey struct{}

// withActor attache l'acteur au contexte de la requête.
func withActor(ctx context.Context, actor identity.Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, actor)
}

// ActorFromContext rend l'acteur de la requête en cours.
//
// Une requête non authentifiée rend l'acteur nul, qui n'autorise rien : le code
// appelant n'a donc pas à distinguer « pas d'acteur » de « acteur sans droits »,
// les deux se traitent en interrogeant [identity.Actor.Allows].
func ActorFromContext(ctx context.Context) identity.Actor {
	actor, ok := ctx.Value(actorKey{}).(identity.Actor)
	if !ok {
		return identity.Actor{}
	}
	return actor
}

// requireAuth est l'intergiciel qui protège l'application.
//
// Il est écrit à l'envers de ce qu'on ferait spontanément : au lieu de marquer les
// routes à protéger, il protège tout et énumère les exceptions. La différence est
// le sens de l'erreur. Oublier d'inscrire une nouvelle route dans la liste des
// exceptions la rend protégée — visible tout de suite, corrigé en une ligne.
// Oublier de poser un décorateur « protégé » sur une nouvelle route l'ouvrirait à
// tout le monde, en silence.
func (h *Handler) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPublicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		actor, err := h.actorFromSession(r)
		if err != nil {
			h.fail(r, err)
			h.render(w, r, pageInternalError, http.StatusInternalServerError, nil)
			return
		}
		if actor.Anonymous() {
			h.redirectToLogin(w, r)
			return
		}

		next.ServeHTTP(w, r.WithContext(withActor(r.Context(), actor)))
	})
}

// isPublicPath énumère ce qui échappe à l'authentification.
//
// /healthz et /readyz n'y figurent pas : ils sont montés par internal/platform en
// amont de cet adapter et ne lui parviennent jamais.
func isPublicPath(path string) bool {
	return path == loginPath || strings.HasPrefix(path, staticPrefix)
}

// actorFromSession reconstruit l'acteur depuis la session.
//
// Une session qui désigne un compte disparu ou désactivé est détruite au passage :
// sans cela, le navigateur reviendrait indéfiniment avec un cookie qui ne peut
// plus rien ouvrir.
func (h *Handler) actorFromSession(r *http.Request) (identity.Actor, error) {
	id := h.sessions.GetString(r.Context(), sessionKeyUserID)
	if id == "" {
		return identity.Actor{}, nil
	}

	user, err := h.accounts.ByID(r.Context(), identity.ID(id))
	switch {
	case errors.Is(err, identity.ErrUnknownUser):
		return identity.Actor{}, h.dropSession(r)
	case err != nil:
		return identity.Actor{}, fmt.Errorf("lecture du compte de la session : %w", err)
	}

	// Actor() est anonyme pour un compte désactivé : la désactivation vaut donc
	// déconnexion, au tour de requête suivant.
	actor := user.Actor()
	if actor.Anonymous() {
		return identity.Actor{}, h.dropSession(r)
	}

	return actor, nil
}

// dropSession détruit la session en cours.
func (h *Handler) dropSession(r *http.Request) error {
	if err := h.sessions.Destroy(r.Context()); err != nil {
		return fmt.Errorf("destruction de la session : %w", err)
	}
	return nil
}

// redirectToLogin envoie l'utilisateur au formulaire, en gardant la page
// qu'il demandait pour l'y ramener ensuite.
func (h *Handler) redirectToLogin(w http.ResponseWriter, r *http.Request) {
	target := loginPath

	// Seules les navigations GET valent la peine d'être reprises : rejouer un POST
	// après connexion n'aurait pas de sens, l'utilisateur n'aurait rien confirmé.
	if r.Method == http.MethodGet {
		if next := internalPath(r.URL.RequestURI()); next != "" && next != homePath {
			target += "?" + paramNext + "=" + url.QueryEscape(next)
		}
	}

	http.Redirect(w, r, target, http.StatusSeeOther)
}

// internalPath filtre une cible de redirection pour qu'elle reste dans
// l'application, et rend la chaîne vide sinon.
//
// C'est la protection contre la redirection ouverte : sans elle, un lien du genre
// /connexion?suite=https://site-de-phishing.example produirait, après une
// connexion réussie, une redirection depuis le domaine légitime vers celui de
// l'attaquant — la moitié du travail d'un hameçonnage. Seuls le chemin et la
// requête sont conservés, reconstruits plutôt que recopiés, de sorte qu'un schéma
// ou un hôte glissé dans la valeur ne survive pas au filtre.
func internalPath(target string) string {
	if !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") {
		return ""
	}
	// « /\ailleurs » est interprété comme « //ailleurs » par certains navigateurs.
	if strings.HasPrefix(target, `/\`) {
		return ""
	}

	parsed, err := url.Parse(target)
	if err != nil || parsed.Scheme != "" || parsed.Host != "" {
		return ""
	}
	if !strings.HasPrefix(parsed.Path, "/") || strings.HasPrefix(parsed.Path, "//") {
		return ""
	}

	cleaned := url.URL{Path: parsed.Path, RawQuery: parsed.RawQuery}

	return cleaned.String()
}
