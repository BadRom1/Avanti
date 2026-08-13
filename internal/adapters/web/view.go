package web

import (
	"net/http"
	"net/url"
	"strings"

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
	// Nav est la navigation principale, déjà traduite et située.
	Nav []NavItem
	// RequestID permet à un utilisateur qui tombe sur une page d'erreur de citer
	// une référence que le journal du serveur contient aussi.
	RequestID string

	translator  *Translator
	assetSuffix string
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
var navBlueprint = []struct {
	href      string
	messageID string
	available bool
}{
	{href: "/", messageID: "nav.dashboard", available: true},
	{href: "/devis", messageID: "nav.devis", available: false},
	{href: "/planning", messageID: "nav.planning", available: false},
	{href: "/finances", messageID: "nav.finance", available: false},
	{href: "/documents", messageID: "nav.documents", available: false},
}

// newView construit le contexte de rendu d'une requête.
func (h *Handler) newView(translator *Translator, r *http.Request) *view {
	return &view{
		Lang:        translator.Lang(),
		Version:     h.version,
		Nav:         buildNav(translator, r.URL.Path),
		RequestID:   server.RequestIDFromContext(r.Context()),
		translator:  translator,
		assetSuffix: "?v=" + url.QueryEscape(h.version),
	}
}

func buildNav(translator *Translator, currentPath string) []NavItem {
	items := make([]NavItem, 0, len(navBlueprint))
	for _, entry := range navBlueprint {
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

// Asset suffixe une URL d'asset de l'estampille de build, pour qu'une mise à
// jour du binaire invalide le cache du navigateur sans intervention.
func (v *view) Asset(assetPath string) string {
	return assetPath + v.assetSuffix
}
