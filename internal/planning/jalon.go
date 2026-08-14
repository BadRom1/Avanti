package planning

import (
	"fmt"
	"time"
)

// Jalon est une échéance contractuelle du chantier : une date attendue, et le
// moment où elle a réellement été atteinte.
//
// Comme [Etape], le jalon se manipule par valeur et sa transition rend un
// nouveau Jalon plutôt que de muter le récepteur.
type Jalon struct {
	// ID identifie le jalon.
	ID ID
	// Name est l'intitulé affiché — « Hors d'eau », « Réception ».
	// Obligatoire.
	Name string
	// Date est l'échéance prévue. Obligatoire, en UTC.
	Date time.Time
	// ReachedAt est le moment où le jalon a été atteint. La valeur zéro
	// signifie « pas encore » — le même modèle que les dates réelles des
	// étapes.
	ReachedAt time.Time
	// CreatedBy est l'acteur qui a créé le jalon.
	CreatedBy ActeurID
	// CreatedAt est la date de création dans Avanti.
	CreatedAt time.Time
	// UpdatedAt est la date de la dernière modification, comparée par la garde
	// optimiste du [Repository].
	UpdatedAt time.Time
}

// Atteint dit si le jalon a été atteint. Comme le statut d'une étape, c'est un
// dérivé de la date — jamais une colonne à part qui pourrait la contredire.
func (j Jalon) Atteint() bool {
	return !j.ReachedAt.IsZero()
}

// Reach marque le jalon comme atteint à la date donnée. La transition ne va
// que dans un sens : un jalon atteint le reste.
func (j Jalon) Reach(at time.Time) (Jalon, error) {
	if at.IsZero() {
		return Jalon{}, fmt.Errorf("%w : date d'atteinte", ErrMissingDate)
	}
	if j.Atteint() {
		return Jalon{}, fmt.Errorf("%w : %s", ErrJalonAlreadyReached, j.Name)
	}

	reached := j
	reached.ReachedAt = at.UTC()
	reached.UpdatedAt = at.UTC()

	return reached, nil
}

// EnRetard dit si le jalon est en retard au jour donné : non atteint alors que
// sa date est passée. Même règle de bord que pour les étapes : le jour même
// n'est pas un retard.
func (j Jalon) EnRetard(today time.Time) bool {
	return !j.Atteint() && dayOf(j.Date).Before(dayOf(today))
}

// RetardConstate rend le nombre de jours de retard au jour donné — zéro quand
// [Jalon.EnRetard] est faux. Arithmétique entière, comme pour les étapes.
func (j Jalon) RetardConstate(today time.Time) int {
	if !j.EnRetard(today) {
		return 0
	}

	return daysBetween(j.Date, today)
}
