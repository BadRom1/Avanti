package web

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/Romain-Badino/Avanti/internal/identity"
)

// Noms des champs du formulaire de connexion.
const (
	fieldEmail = "email"
	// gosec voit un identifiant contenant « passe » et suppose un secret en dur
	// (G101). C'est le nom d'un champ de formulaire HTML, celui qui figure dans
	// login.gohtml ; la valeur du champ, elle, arrive du navigateur et n'est
	// jamais écrite ici.
	//
	// L'annotation est celle de gosec et non un //nolint de golangci-lint : `make
	// sec` lance gosec seul, qui ne connaît pas les directives de golangci.
	// #nosec G101 -- nom d'un champ de formulaire, pas un secret.
	fieldPassword = "mot_de_passe"
)

// paramLogout signale à la page de connexion qu'on y arrive après une
// déconnexion volontaire, pour l'annoncer plutôt que de laisser croire à une
// expiration.
const paramLogout = "deconnexion"

// loginData est la charge utile du gabarit de connexion.
type loginData struct {
	// Email réaffiche la saisie, pour ne pas la faire retaper après un échec. Le
	// mot de passe, lui, n'est jamais renvoyé au navigateur.
	Email string
	// Next est la page à rejoindre après la connexion, déjà filtrée par
	// [internalPath].
	Next string
	// Error est le message d'échec, déjà traduit. Vide s'il n'y en a pas.
	Error string
	// LoggedOut annonce une déconnexion réussie.
	LoggedOut bool
}

// handleLoginForm affiche le formulaire.
func (h *Handler) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	// Déjà connecté : le formulaire n'a rien à proposer.
	if actor, err := h.actorFromSession(r); err == nil && !actor.Anonymous() {
		http.Redirect(w, r, homePath, http.StatusSeeOther)
		return
	}

	h.render(w, r, pageLogin, http.StatusOK, loginData{
		Next:      internalPath(r.URL.Query().Get(paramNext)),
		LoggedOut: r.URL.Query().Has(paramLogout),
	})
}

// handleLogin traite la soumission du formulaire.
func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.fail(r, fmt.Errorf("lecture du formulaire de connexion : %w", err))
		h.render(w, r, pageInternalError, http.StatusInternalServerError, nil)
		return
	}

	input := loginData{
		Email: r.PostFormValue(fieldEmail),
		Next:  internalPath(r.PostFormValue(paramNext)),
	}
	password := r.PostFormValue(fieldPassword)

	key := h.limiter.key(input.Email, r)

	if remaining, blocked := h.limiter.blocked(key); blocked {
		h.reject(w, r, input, http.StatusTooManyRequests,
			"connexion.erreur.trop_de_tentatives", "Minutes", minutesLeft(remaining))
		return
	}

	actor, err := h.accounts.Authenticate(r.Context(), input.Email, password)
	switch {
	case err == nil:
	case errors.Is(err, identity.ErrInvalidCredentials):
		h.limiter.failure(key)
		h.reject(w, r, input, http.StatusUnauthorized, "connexion.erreur.identifiants")
		return
	case errors.Is(err, identity.ErrAccountDisabled):
		h.limiter.failure(key)
		h.reject(w, r, input, http.StatusForbidden, "connexion.erreur.compte_desactive")
		return
	default:
		// Ni un refus ni une faute de frappe : une panne. Elle se journalise et
		// s'affiche comme telle, plutôt que de se déguiser en mauvais mot de passe.
		h.fail(r, fmt.Errorf("authentification : %w", err))
		h.render(w, r, pageInternalError, http.StatusInternalServerError, nil)
		return
	}

	if err := h.openSession(r, actor); err != nil {
		h.fail(r, err)
		h.render(w, r, pageInternalError, http.StatusInternalServerError, nil)
		return
	}
	h.limiter.success(key)

	target := homePath
	if input.Next != "" {
		target = input.Next
	}

	// gosec suit la valeur depuis le formulaire et signale une redirection ouverte.
	// Elle est déjà filtrée : input.Next sort de internalPath, qui n'accepte
	// qu'un chemin local et le reconstruit au lieu de le recopier. L'analyse de
	// gosec ne reconnaît pas ce nettoyage ; TestOpenRedirectRejected, lui,
	// vérifie le résultat sur cinq formes d'attaque.
	http.Redirect(w, r, target, http.StatusSeeOther) //nolint:gosec // cible filtrée par internalPath, voir TestOpenRedirectRejected.
}

// openSession installe la session de l'acteur qui vient de s'authentifier.
//
// Le renouvellement du jeton est la précaution contre la fixation de session : si
// un attaquant a réussi à faire adopter au navigateur un identifiant de session
// qu'il connaît — par un lien, par un cookie posé depuis un sous-domaine — ce
// jeton est remplacé à l'instant où la session gagne des droits, et celui qu'il
// détient ne vaut plus rien.
func (h *Handler) openSession(r *http.Request, actor identity.Actor) error {
	if err := h.sessions.RenewToken(r.Context()); err != nil {
		return fmt.Errorf("renouvellement du jeton de session : %w", err)
	}

	h.sessions.Put(r.Context(), sessionKeyUserID, string(actor.UserID()))

	return nil
}

// handleLogout détruit la session.
//
// La route est en POST : une déconnexion est un changement d'état, et une image
// ou un lien préchargé pointant sur une URL en GET suffirait à déconnecter
// quelqu'un sans qu'il l'ait demandé.
func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	if err := h.dropSession(r); err != nil {
		h.fail(r, err)
		h.render(w, r, pageInternalError, http.StatusInternalServerError, nil)
		return
	}

	http.Redirect(w, r, loginPath+"?"+paramLogout, http.StatusSeeOther)
}

// reject réaffiche le formulaire avec un message d'échec.
//
// Les messages sont volontairement peu bavards : « identifiants invalides » ne dit
// pas si l'adresse existe, ce qui est le pendant côté interface de
// l'indistinguabilité que le domaine garantit côté service. En dire davantage
// referait du formulaire l'outil d'énumération que le domaine s'applique à ne pas
// être.
func (h *Handler) reject(w http.ResponseWriter, r *http.Request, input loginData, status int, messageID string, substitutions ...string) {
	translator := h.catalog.Translator(r.Header.Get("Accept-Language"))
	input.Error = translator.T(messageID, substitutions...)

	h.render(w, r, pageLogin, status, input)
}

// minutesLeft arrondit une durée d'attente à la minute supérieure, pour un
// message qui ne dise jamais « réessayez dans 0 minute ».
func minutesLeft(remaining time.Duration) string {
	minutes := int(math.Ceil(remaining.Minutes()))
	if minutes < 1 {
		minutes = 1
	}
	return strconv.Itoa(minutes)
}
