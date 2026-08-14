package finance

import "errors"

// Les erreurs du domaine. Elles sont comparables avec errors.Is, y compris au
// travers des enveloppes que les adapters ajoutent.
var (
	// ErrEmptyEntreprise signale une pièce sans nom d'entreprise. C'est le champ
	// qui dit à qui l'argent est dû ou versé : sans lui, ni le rapprochement ni
	// le dossier d'assurance ne veulent rien dire.
	ErrEmptyEntreprise = errors.New("finance : le nom de l'entreprise est obligatoire")
	// ErrTextTooLong signale un texte au-delà de la borne de son champ.
	ErrTextTooLong = errors.New("finance : texte trop long")
	// ErrInvalidMontant signale un montant nul, négatif ou hors des bornes. Une
	// facture à zéro n'est pas une facture, et un montant négatif est une saisie
	// retournée.
	ErrInvalidMontant = errors.New("finance : montant invalide")
	// ErrMissingDate signale une date obligatoire absente — date de la facture,
	// date du versement, date d'une transition.
	ErrMissingDate = errors.New("finance : date obligatoire manquante")
	// ErrMissingActor signale une action sans acteur. La traçabilité n'est pas
	// facultative : une dépense anonyme ne s'explique plus six mois après.
	ErrMissingActor = errors.New("finance : acteur de l'action manquant")
	// ErrInvalidDevisID signale une référence de devis qui ne peut pas être
	// stockée. Le domaine ne sait pas vérifier que le devis existe — c'est une
	// référence faible (R2) — mais il borne ce qu'un POST forgé peut y glisser.
	ErrInvalidDevisID = errors.New("finance : référence de devis invalide")
	// ErrUnknownMoyenPaiement signale un moyen de paiement hors de la liste que
	// le domaine reconnaît.
	ErrUnknownMoyenPaiement = errors.New("finance : moyen de paiement inconnu")

	// ErrUnknownFacture signale l'absence de la facture cherchée. C'est l'erreur
	// que rend un [Repository] sur une lecture ou une réécriture sans résultat.
	ErrUnknownFacture = errors.New("finance : facture inconnue")
	// ErrUnknownAcompte signale l'absence de l'acompte cherché.
	ErrUnknownAcompte = errors.New("finance : acompte inconnu")
	// ErrConcurrentUpdate signale une réécriture doublée par une autre : la
	// pièce a changé entre la lecture et l'écriture — deux personnes qui
	// jouent une transition en même temps. Le perdant relit et recommence ;
	// écraser en silence ferait régresser l'état posé par le gagnant.
	ErrConcurrentUpdate = errors.New("finance : la pièce a été modifiée entre-temps")

	// ErrFactureAlreadyPaid signale une facture qu'on tente de payer deux fois.
	// Le paiement ne se reprend pas : une facture payée le reste.
	ErrFactureAlreadyPaid = errors.New("finance : cette facture est déjà payée")
	// ErrForbiddenAssuranceTransition signale un changement de statut assurance
	// que le cycle de vie n'autorise pas — rembourser une pièce jamais envoyée,
	// renvoyer une pièce déjà remboursée. Le cycle ne va que dans un sens :
	// non_envoyee → envoyee → remboursee.
	ErrForbiddenAssuranceTransition = errors.New("finance : changement de statut assurance interdit")
	// ErrInvalidRemboursement signale un montant remboursé nul, négatif ou
	// supérieur au montant de la pièce : l'assurance ne rembourse pas plus que
	// ce qui a été dépensé.
	ErrInvalidRemboursement = errors.New("finance : montant remboursé invalide")

	// ErrMissingEngagement signale un acompte rattaché à un devis sans que le
	// montant engagé ait été fourni. Le domaine ne peut pas le lire lui-même
	// (R1/R2) : c'est l'adapter appelant qui interroge le domaine devis et le
	// passe en valeur — l'oublier est un bug de l'appelant, pas une saisie.
	ErrMissingEngagement = errors.New("finance : montant engagé manquant pour un acompte rattaché à un devis")
	// ErrAcomptesExceedEngagement signale l'invariant central du domaine : le
	// cumul des acomptes d'un devis ne dépasse pas le montant engagé. Un
	// [Repository] la rend aussi quand la vérification refaite sous
	// sérialisation échoue — c'est elle qui fait foi, la vérification du service
	// ne sert qu'à répondre tôt et clairement.
	ErrAcomptesExceedEngagement = errors.New("finance : le cumul des acomptes dépasserait le montant engagé du devis")
)
