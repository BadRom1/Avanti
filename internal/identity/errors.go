package identity

import "errors"

// Les erreurs du domaine. Elles sont comparables avec errors.Is, y compris au
// travers des enveloppes que les adapters ajoutent.
var (
	// ErrInvalidEmail signale une adresse qui n'a pas la forme d'un email.
	ErrInvalidEmail = errors.New("identity : adresse email invalide")
	// ErrEmailTaken signale qu'un compte porte déjà cette adresse. Cette
	// erreur ne sort jamais du chemin d'authentification — seulement de celui de
	// création, où l'appelant est déjà un propriétaire.
	ErrEmailTaken = errors.New("identity : un compte utilise déjà cette adresse email")
	// ErrEmptyDisplayName signale un nom d'affichage absent ou fait d'espaces.
	ErrEmptyDisplayName = errors.New("identity : le nom d'affichage est obligatoire")
	// ErrUnknownRole signale un rôle qui n'est pas au catalogue.
	ErrUnknownRole = errors.New("identity : rôle inconnu")
	// ErrPasswordTooShort signale un mot de passe sous la longueur minimale.
	ErrPasswordTooShort = errors.New("identity : mot de passe trop court")
	// ErrPasswordTooLong signale un mot de passe dont la taille sert
	// visiblement à faire travailler le serveur pour rien.
	ErrPasswordTooLong = errors.New("identity : mot de passe trop long")

	// ErrInvalidCredentials est la seule erreur que rend une authentification
	// ratée, que l'email soit inconnu ou le mot de passe faux. C'est ce qui
	// empêche d'énumérer les comptes existants.
	ErrInvalidCredentials = errors.New("identity : identifiants invalides")
	// ErrAccountDisabled signale un compte désactivé dont le mot de passe est
	// pourtant le bon.
	//
	// Distinguer ce cas n'ouvre aucune énumération : pour l'obtenir il faut déjà
	// avoir prouvé qu'on connaît le mot de passe du compte. En échange, la
	// personne comprend qu'elle doit demander une réactivation plutôt que
	// chercher indéfiniment une faute de frappe.
	ErrAccountDisabled = errors.New("identity : compte désactivé")

	// ErrUnknownUser signale l'absence du compte demandé. C'est l'erreur
	// que rend un [UserRepository] sur une lecture sans résultat.
	ErrUnknownUser = errors.New("identity : compte inconnu")
)
