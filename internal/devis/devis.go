package devis

import (
	"fmt"
	"slices"
	"time"
)

// Statut est l'état d'un devis dans le cycle de la comparaison.
//
// Les valeurs sont en français parce qu'elles sont stockées telles quelles en
// base et affichées telles quelles : c'est le même vocabulaire des deux côtés,
// et la correspondance se lit sans table de traduction.
type Statut string

// Les trois états d'un devis, dans l'ordre du cycle de vie.
const (
	// StatutRecu est l'état de naissance : le devis est arrivé, il entre dans la
	// comparaison, rien n'est tranché.
	StatutRecu Statut = "recu"
	// StatutRetenu est le devis choisi. Une demande n'en porte qu'un.
	StatutRetenu Statut = "retenu"
	// StatutRefuse est un devis écarté, soit explicitement, soit par ricochet du
	// choix d'un concurrent.
	StatutRefuse Statut = "refuse"
)

// allStatuts énumère les statuts reconnus, dans l'ordre du cycle de vie.
var allStatuts = []Statut{StatutRecu, StatutRetenu, StatutRefuse}

// transitions est la table du cycle de vie, et la seule source de vérité sur ce
// qui est permis.
//
// Elle est écrite en table plutôt qu'en suite de `if` pour que le cycle se lise
// d'un coup d'œil : un devis reçu se tranche dans un sens ou dans l'autre, et
// une décision ne se reprend pas. Rouvrir un devis refusé demanderait de
// rouvrir aussi la demande — c'est un cas d'usage qui devra être nommé et
// écrit, pas un effet de bord d'une transition permissive.
var transitions = map[Statut][]Statut{
	StatutRecu:   {StatutRetenu, StatutRefuse},
	StatutRetenu: nil,
	StatutRefuse: nil,
}

// AllStatuts renvoie les statuts reconnus, dans un ordre stable.
//
// La tranche renvoyée est une copie : la modifier ne change rien au domaine.
func AllStatuts() []Statut {
	return slices.Clone(allStatuts)
}

// Known indique si le statut fait partie de ceux que le domaine reconnaît.
func (s Statut) Known() bool {
	return slices.Contains(allStatuts, s)
}

// Pending dit que le devis attend encore une décision.
func (s Statut) Pending() bool {
	return s == StatutRecu
}

// Decided dit que le sort du devis est scellé.
func (s Statut) Decided() bool {
	return s == StatutRetenu || s == StatutRefuse
}

// CanBecome dit si le passage vers target est permis par le cycle de vie.
func (s Statut) CanBecome(target Statut) bool {
	return slices.Contains(transitions[s], target)
}

// String rend le statut tel qu'il est stocké.
func (s Statut) String() string {
	return string(s)
}

// Devis est une proposition chiffrée reçue d'un artisan en réponse à une
// demande.
//
// L'entité se manipule par valeur et ses transitions rendent un nouveau Devis
// plutôt que de muter le récepteur : un devis passé dans une fonction ne peut
// pas revenir changé, et l'appelant décide explicitement de ce qu'il persiste.
type Devis struct {
	// ID identifie le devis.
	ID ID
	// DemandeID rattache le devis à la demande qu'il concurrence. Sans ce
	// rattachement, un devis ne se compare à rien.
	DemandeID ID
	// Artisan est l'entreprise qui a chiffré. Elle est recopiée dans le devis
	// plutôt que référencée : ce qui figure sur le papier reçu ne doit pas
	// changer quand la liste des artisans sollicités est corrigée.
	Artisan Artisan
	// Montant est le prix proposé, en centimes. Toujours strictement positif.
	Montant Montant
	// ReceivedAt est la date de réception du devis.
	ReceivedAt time.Time
	// Validity est la durée de validité annoncée par l'artisan, à compter de
	// [Devis.ReceivedAt]. Zéro vaut « non renseignée », le cas le plus courant.
	Validity time.Duration
	// Notes porte ce que le devis ne dit pas : une réserve orale, un délai
	// annoncé au téléphone, une remise promise.
	Notes string
	// Statut est l'état du devis dans la comparaison.
	Statut Statut
	// RecordedBy est l'acteur qui a saisi le devis.
	RecordedBy ActeurID
	// DecidedBy est l'acteur qui l'a tranché, vide tant qu'il ne l'est pas.
	DecidedBy ActeurID
	// DecidedAt est la date de la décision, nulle tant qu'il n'y en a pas.
	DecidedAt time.Time
	// CreatedAt est la date d'enregistrement dans Avanti, distincte de
	// [Devis.ReceivedAt] : un devis reçu par courrier se saisit après coup.
	CreatedAt time.Time
	// UpdatedAt est la date de la dernière modification.
	UpdatedAt time.Time
}

// Retain marque le devis comme retenu.
//
// La méthode ne connaît que ce devis : refuser les concurrents est la décision
// de [Service.Retain], qui voit la demande entière, et l'écriture indivisible
// des deux est celle du [Repository]. Découper ainsi laisse la règle de
// transition testable sans rien monter à côté.
func (d Devis) Retain(by ActeurID, at time.Time) (Devis, error) {
	return d.decide(StatutRetenu, by, at)
}

// Reject marque le devis comme refusé, sans rien retenir. C'est le cas d'un
// devis hors sujet ou hors budget qu'on écarte avant même d'avoir reçu les
// autres.
func (d Devis) Reject(by ActeurID, at time.Time) (Devis, error) {
	return d.decide(StatutRefuse, by, at)
}

// decide applique une transition et consigne qui l'a prise.
func (d Devis) decide(target Statut, by ActeurID, at time.Time) (Devis, error) {
	if by == "" {
		return Devis{}, ErrMissingActor
	}
	if at.IsZero() {
		return Devis{}, fmt.Errorf("%w : date de décision", ErrMissingDate)
	}
	if !d.Statut.CanBecome(target) {
		return Devis{}, fmt.Errorf("%w : %s → %s", ErrForbiddenTransition, d.Statut, target)
	}

	decided := d
	decided.Statut = target
	decided.DecidedBy = by
	decided.DecidedAt = at.UTC()
	decided.UpdatedAt = at.UTC()

	return decided, nil
}

// ValidUntil rend la date d'expiration du devis, et faux si l'artisan n'a annoncé
// aucune durée de validité.
func (d Devis) ValidUntil() (time.Time, bool) {
	if d.Validity <= 0 {
		return time.Time{}, false
	}
	return d.ReceivedAt.Add(d.Validity), true
}

// Expired dit si la validité annoncée est dépassée à la date donnée.
//
// Un devis sans durée annoncée n'expire jamais : c'est l'artisan qui dira le
// contraire, et Avanti n'a pas à inventer une échéance qui ferait écarter une
// offre encore valable.
func (d Devis) Expired(now time.Time) bool {
	limit, known := d.ValidUntil()
	return known && now.After(limit)
}
