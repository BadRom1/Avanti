package web

import (
	"net/http"

	"github.com/Romain-Badino/Avanti/internal/identity"
)

// requireScope garde une route derrière un scope.
//
// C'est le pendant, route par route, de ce que [Handler.requireAuth] fait pour
// l'application entière : l'intergiciel dit *qui* est là, ce décorateur dit ce
// que cette personne a le droit de faire. Les deux sont séparés parce qu'ils
// répondent à deux questions différentes, et qu'une route oubliée doit tomber du
// bon côté dans les deux cas — sans session on est redirigé vers /connexion,
// sans le scope on reçoit un refus.
//
// Le décorateur prend un scope et jamais un rôle. Un test « si le rôle est
// propriétaire » dupliquerait la table d'autorisation du domaine, et cesserait
// d'être d'accord avec elle au premier changement ; c'est aussi la règle que
// [view.Can] applique côté gabarit, de sorte que ce qui s'affiche et ce qui
// s'exécute obéissent à la même source de vérité.
//
// Le refus est une page, pas une redirection : la personne est bien connectée,
// l'envoyer au formulaire de connexion lui ferait retaper son mot de passe pour
// revenir au même refus.
func (h *Handler) requireScope(scope identity.Scope, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !ActorFromContext(r.Context()).Allows(scope) {
			h.renderForbidden(w, r)
			return
		}

		next(w, r)
	}
}

// renderForbidden sert la page de refus.
//
// Elle ne dit pas ce qui manque exactement : la personne connectée ne peut rien
// y faire elle-même — les scopes viennent du rôle, qu'un propriétaire seul
// change — et détailler l'autorisation absente renseignerait surtout qui
// essaierait des URLs.
func (h *Handler) renderForbidden(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, pageForbidden, http.StatusForbidden, nil)
}
