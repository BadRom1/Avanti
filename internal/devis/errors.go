package devis

import "errors"

// Les erreurs du domaine. Elles sont comparables avec errors.Is, y compris au
// travers des enveloppes que les adapters ajoutent.
var (
	// ErrEmptyLot signale une demande sans intitulé de lot de travaux. C'est le
	// seul champ qui identifie une consultation pour un humain : « Charpente »,
	// « Électricité ». Sans lui, la liste des demandes devient illisible.
	ErrEmptyLot = errors.New("devis : l'intitulé du lot de travaux est obligatoire")
	// ErrTextTooLong signale un texte au-delà de la borne de son champ.
	ErrTextTooLong = errors.New("devis : texte trop long")
	// ErrEmptyEntreprise signale un artisan sans nom d'entreprise.
	ErrEmptyEntreprise = errors.New("devis : le nom de l'entreprise est obligatoire")
	// ErrInvalidArtisanEmail signale une adresse d'artisan qui n'a pas la forme
	// d'un email. Le champ reste facultatif : c'est le renseigner mal qui est
	// refusé, pas le laisser vide.
	ErrInvalidArtisanEmail = errors.New("devis : adresse email de l'artisan invalide")

	// ErrInvalidMontant signale un montant nul, négatif ou hors des bornes. Un
	// devis à zéro n'est pas un devis, et un montant négatif est une saisie
	// retournée.
	ErrInvalidMontant = errors.New("devis : montant invalide")
	// ErrMissingDate signale une date obligatoire absente — envoi d'une demande,
	// réception d'un devis.
	ErrMissingDate = errors.New("devis : date obligatoire manquante")
	// ErrNegativeValidity signale une durée de validité négative. Zéro reste
	// permis : il vaut « durée non renseignée ».
	ErrNegativeValidity = errors.New("devis : durée de validité négative")
	// ErrMissingDemande signale un devis qu'on tente d'enregistrer sans le
	// rattacher à une demande. Un devis isolé ne se compare à rien.
	ErrMissingDemande = errors.New("devis : demande de rattachement manquante")
	// ErrMissingActor signale une action sans acteur. La traçabilité n'est pas
	// facultative : une décision anonyme ne s'explique plus six mois après.
	ErrMissingActor = errors.New("devis : acteur de l'action manquant")

	// ErrUnknownDemande signale l'absence de la demande cherchée. C'est
	// l'erreur que rend un [Repository] sur une lecture sans résultat.
	ErrUnknownDemande = errors.New("devis : demande de devis inconnue")
	// ErrUnknownDevis signale l'absence du devis cherché.
	ErrUnknownDevis = errors.New("devis : devis inconnu")

	// ErrForbiddenTransition signale un changement de statut que le cycle de vie
	// n'autorise pas — retenir un devis déjà refusé, par exemple.
	ErrForbiddenTransition = errors.New("devis : changement de statut interdit")
	// ErrDemandeClosed signale une demande dont un devis est déjà retenu. Elle
	// n'accepte plus de nouveau devis : la comparaison est close, et y ajouter
	// une offre laisserait croire qu'elle est encore en jeu.
	ErrDemandeClosed = errors.New("devis : la demande est close, un devis a déjà été retenu")
	// ErrDevisAlreadyDecided signale un devis qui n'est plus en attente de
	// décision. Un [Repository] la rend quand l'écriture ne trouve plus le devis
	// au statut « recu » — c'est ce qui fait qu'un double clic, ou deux
	// personnes qui tranchent en même temps, ne produisent pas deux décisions.
	ErrDevisAlreadyDecided = errors.New("devis : ce devis a déjà été tranché")
)
