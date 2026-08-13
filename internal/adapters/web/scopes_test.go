package web_test

import (
	"net/http"
	"strings"
	"testing"
)

// TestMenuFollowsScopes est le test qui compte pour la partie autorisation de
// l'interface : ce que la page montre est décidé par les scopes de la personne
// connectée, et rien d'autre.
//
// Le collaborateur — l'architecte — travaille sur les devis et le planning. Les
// finances et les pièces du dossier ne doivent pas seulement lui être refusées :
// elles ne doivent pas lui être annoncées. Montrer une porte qu'on n'a pas le
// droit de pousser est une information sur le contenu de l'application, et une
// invitation à demander pourquoi.
func TestMenuFollowsScopes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		email   string
		wanted  []string
		missing []string
	}{
		{
			name:    "le propriétaire voit les quatre domaines et l'accès agent IA",
			email:   ownerEmail,
			wanted:  []string{"Devis", "Planning", "Finances", "Documents", "Accès agent IA", "Propriétaire"},
			missing: nil,
		},
		{
			name:    "le collaborateur ne voit que devis et planning",
			email:   collaboratorEmail,
			wanted:  []string{"Devis", "Planning", "Collaborateur"},
			missing: []string{"Finances", "Documents", "Accès agent IA"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			browser := newBrowser(t, newSite(t).handler)
			browser.login(tc.email)

			result := browser.get("/")
			if result.Status != http.StatusOK {
				t.Fatalf("statut = %d, attendu 200", result.Status)
			}

			for _, wanted := range tc.wanted {
				if !strings.Contains(result.Body, wanted) {
					t.Errorf("la page ne mentionne pas %q", wanted)
				}
			}
			for _, missing := range tc.missing {
				if strings.Contains(result.Body, missing) {
					t.Errorf("la page mentionne %q, que ce rôle n'a pas le droit de voir", missing)
				}
			}
		})
	}
}

// TestUnavailableSectionsAreNotLinks : annoncer une section dans la
// navigation est acceptable, y send l'utilisateur sur un 404 ne l'est pas.
func TestUnavailableSectionsAreNotLinks(t *testing.T) {
	t.Parallel()

	browser := newBrowser(t, newSite(t).handler)
	browser.login(ownerEmail)

	result := browser.get("/")

	if strings.Contains(result.Body, `href="/devis"`) {
		t.Error("la navigation pointe vers /devis, qui n'existe pas encore")
	}
	if !strings.Contains(result.Body, "Devis") {
		t.Error("la navigation doit tout de même annoncer la section Devis")
	}
}

// TestLoginPageHasNoNavigation : sur le formulaire, il n'y a rien à
// naviguer, et surtout rien à annoncer d'une application dans laquelle on n'est
// pas encore entré.
func TestLoginPageHasNoNavigation(t *testing.T) {
	t.Parallel()

	browser := newBrowser(t, newSite(t).handler)
	result := browser.get("/connexion")

	for _, missing := range []string{"Navigation principale", "Tableau de bord", "Se déconnecter", "Finances"} {
		if strings.Contains(result.Body, missing) {
			t.Errorf("la page de connexion mentionne %q", missing)
		}
	}
}
