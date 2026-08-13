package web

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/Romain-Badino/Avanti/internal/identity"
	"github.com/Romain-Badino/Avanti/internal/platform/server"
)

// view est le contexte que reçoit un gabarit. Ses méthodes sont l'unique
// vocabulaire disponible côté gabarit : pas de fonction globale, pas de FuncMap
// à tenir à jour en parallèle, et une traduction qui ne peut pas être oubliée
// puisqu'elle est le seul moyen d'écrire du texte.
type view struct {
	// Lang alimente l'attribut lang du document.
	Lang string
	// Version estampille le pied de page.
	Version string
	// Nav est la navigation principale, déjà traduite, située et filtrée selon les
	// scopes de la personne connectée.
	Nav []NavItem
	// RequestID permet à un utilisateur qui tombe sur une page d'erreur de citer
	// une référence que le journal du serveur contient aussi.
	RequestID string
	// User décrit la personne connectée, ou vaut nil sur les pages
	// publiques. Les gabarits s'en servent pour décider d'afficher l'en-tête de
	// session — un `{{ if .User }}` suffit.
	User *ViewUser
	// Data est la charge utile propre à la page rendue. Chaque gabarit sait quel
	// type il reçoit ; les pages qui n'en ont pas besoin le laissent nil.
	Data any
	// LogoutPath est l'action du formulaire de déconnexion.
	LogoutPath string

	actor       identity.Actor
	translator  *Translator
	assetSuffix string
}

// ViewUser est la personne connectée, telle qu'un gabarit l'affiche.
//
// Elle ne porte que ce qui s'affiche : ni empreinte, ni horodatages. Les décisions
// d'autorisation passent par [view.Can], jamais par une comparaison de rôle dans
// un gabarit.
type ViewUser struct {
	// DisplayName est le nom montré dans l'en-tête.
	DisplayName string
	// Email est l'identifiant de connexion, affiché en second.
	Email string
	// Role est le libellé traduit du rôle.
	Role string
}

// NavItem est une entrée de la navigation principale.
type NavItem struct {
	// Href est la cible du lien.
	Href string
	// Label est l'intitulé, déjà traduit.
	Label string
	// Current signale la section affichée.
	Current bool
	// Available distingue une section réellement branchée d'une section encore
	// à écrire. Une entrée indisponible s'affiche en clair mais sans lien :
	// annoncer une page qui répond 404 serait pire que de l'annoncer grisée.
	Available bool
}

// navBlueprint décrit la navigation indépendamment de la langue et de la
// requête. Passer Available à true accompagne l'arrivée du lot correspondant.
//
// Le champ scope est ce qui rend le menu dépendant du rôle : une entrée dont le
// scope n'est pas détenu n'est pas affichée grisée, elle n'est pas affichée du
// tout. Montrer « Finances » à l'architecte lui annoncerait une porte qu'il n'a
// pas le droit de pousser.
var navBlueprint = []struct {
	href      string
	messageID string
	scope     identity.Scope
	available bool
}{
	{href: "/", messageID: "nav.dashboard", scope: "", available: true},
	{href: devisPath, messageID: "nav.devis", scope: identity.ScopeDevisRead, available: true},
	{href: "/planning", messageID: "nav.planning", scope: identity.ScopePlanningRead, available: false},
	{href: "/finances", messageID: "nav.finance", scope: identity.ScopeFinanceRead, available: false},
	{href: "/documents", messageID: "nav.documents", scope: identity.ScopeDocumentRead, available: false},
}

// newView construit le contexte de rendu d'une requête.
func (h *Handler) newView(translator *Translator, r *http.Request, data any) *view {
	actor := ActorFromContext(r.Context())

	return &view{
		Lang:        translator.Lang(),
		Version:     h.version,
		Nav:         buildNav(translator, r.URL.Path, actor),
		RequestID:   server.RequestIDFromContext(r.Context()),
		User:        h.viewUser(translator, r, actor),
		Data:        data,
		LogoutPath:  logoutPath,
		actor:       actor,
		translator:  translator,
		assetSuffix: "?v=" + url.QueryEscape(h.version),
	}
}

// viewUser décrit la personne connectée, ou rend nil.
//
// Le nom d'affichage demande une lecture du compte, que l'intergiciel
// d'authentification a déjà faite pour construire l'acteur. La relire ici est un
// aller-retour de plus vers une table de trois lignes ; ce sera à revoir si la
// vue devient chère, en portant le nom d'affichage dans la session.
func (h *Handler) viewUser(translator *Translator, r *http.Request, actor identity.Actor) *ViewUser {
	if actor.Anonymous() {
		return nil
	}

	user, err := h.accounts.ByID(r.Context(), actor.UserID())
	if err != nil {
		// L'acteur existe, donc le compte existait à l'entrée de la requête. Une
		// erreur ici est une anomalie sans gravité pour l'affichage : la page se
		// rend sans en-tête de session plutôt que de basculer en erreur 500.
		h.fail(r, err)
		return nil
	}

	return &ViewUser{
		DisplayName: user.DisplayName,
		Email:       user.Email,
		Role:        translator.T("role." + string(user.Role)),
	}
}

func buildNav(translator *Translator, currentPath string, actor identity.Actor) []NavItem {
	items := make([]NavItem, 0, len(navBlueprint))
	for _, entry := range navBlueprint {
		if entry.scope != "" && !actor.Allows(entry.scope) {
			continue
		}
		items = append(items, NavItem{
			Href:      entry.href,
			Label:     translator.T(entry.messageID),
			Current:   isCurrent(entry.href, currentPath),
			Available: entry.available,
		})
	}
	return items
}

// isCurrent situe la page affichée dans la navigation. La racine est un cas à
// part : sans cela, elle serait le préfixe de toutes les autres et resterait
// éternellement surlignée.
func isCurrent(href, currentPath string) bool {
	if href == "/" {
		return currentPath == "/"
	}
	return currentPath == href || strings.HasPrefix(currentPath, href+"/")
}

// T traduit un message. C'est la seule façon d'écrire du texte dans un gabarit.
func (v *view) T(id string, pairs ...string) string {
	return v.translator.T(id, pairs...)
}

// Can dit si la personne connectée détient le scope nommé.
//
// C'est le seul test d'autorisation disponible côté gabarit, et il prend un scope
// et non un rôle : un gabarit qui écrirait « si le rôle est propriétaire »
// dupliquerait la table d'autorisation du domaine, et cesserait d'être d'accord
// avec elle au premier changement. Un scope inconnu rend faux — se tromper de nom
// fait disparaître l'élément, jamais apparaître.
func (v *view) Can(scope string) bool {
	return v.actor.Allows(identity.Scope(scope))
}

// Asset suffixe une URL d'asset de l'estampille de build, pour qu'une mise à
// jour du binaire invalide le cache du navigateur sans intervention.
func (v *view) Asset(assetPath string) string {
	return assetPath + v.assetSuffix
}
