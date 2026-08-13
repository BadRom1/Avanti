package devis_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Romain-Badino/Avanti/internal/devis"
)

// TestStatutTransitions énumère les neuf passages possibles entre statuts, et
// non les seuls permis. C'est la table du cycle de vie qui est vérifiée, pas un
// échantillon d'exemples heureux : un statut qui deviendrait permis par erreur
// se verrait ici.
func TestStatutTransitions(t *testing.T) {
	t.Parallel()

	permis := map[devis.Statut][]devis.Statut{
		devis.StatutRecu:   {devis.StatutRetenu, devis.StatutRefuse},
		devis.StatutRetenu: {},
		devis.StatutRefuse: {},
	}

	for _, from := range devis.AllStatuts() {
		for _, to := range devis.AllStatuts() {
			want := slices.Contains(permis[from], to)
			if got := from.CanBecome(to); got != want {
				t.Errorf("%s.CanBecome(%s) = %t, attendu %t", from, to, got, want)
			}
		}
	}
}

func TestStatutClassification(t *testing.T) {
	t.Parallel()

	cases := map[devis.Statut]struct {
		known   bool
		pending bool
		decided bool
	}{
		devis.StatutRecu:          {known: true, pending: true, decided: false},
		devis.StatutRetenu:        {known: true, pending: false, decided: true},
		devis.StatutRefuse:        {known: true, pending: false, decided: true},
		devis.Statut("brouillon"): {known: false, pending: false, decided: false},
		devis.Statut(""):          {known: false, pending: false, decided: false},
	}

	for statut, want := range cases {
		if got := statut.Known(); got != want.known {
			t.Errorf("Statut(%q).Known() = %t, attendu %t", statut, got, want.known)
		}
		if got := statut.Pending(); got != want.pending {
			t.Errorf("Statut(%q).Pending() = %t, attendu %t", statut, got, want.pending)
		}
		if got := statut.Decided(); got != want.decided {
			t.Errorf("Statut(%q).Decided() = %t, attendu %t", statut, got, want.decided)
		}
		if got := statut.String(); got != string(statut) {
			t.Errorf("Statut(%q).String() = %q", statut, got)
		}
	}
}

// TestAllStatutsIsACopy : la liste rendue est une copie, sans quoi un appelant
// pourrait vider le cycle de vie du domaine depuis l'extérieur.
func TestAllStatutsIsACopy(t *testing.T) {
	t.Parallel()

	first := devis.AllStatuts()
	first[0] = devis.Statut("saboté")

	if second := devis.AllStatuts(); second[0] != devis.StatutRecu {
		t.Errorf("AllStatuts()[0] = %q après modification de la copie, attendu %q", second[0], devis.StatutRecu)
	}
}

func TestDevisRetainAndReject(t *testing.T) {
	t.Parallel()

	decision := time.Date(2026, time.March, 20, 8, 0, 0, 0, time.UTC)
	recu := devis.Devis{ID: "d1", Statut: devis.StatutRecu, UpdatedAt: instantSaisie}

	retenu, err := recu.Retain(acteur, decision)
	if err != nil {
		t.Fatalf("Retain() échoué : %v", err)
	}
	if retenu.Statut != devis.StatutRetenu {
		t.Errorf("statut après Retain() = %q, attendu %q", retenu.Statut, devis.StatutRetenu)
	}
	if retenu.DecidedBy != acteur {
		t.Errorf("DecidedBy = %q, attendu %q", retenu.DecidedBy, acteur)
	}
	if !retenu.DecidedAt.Equal(decision) || !retenu.UpdatedAt.Equal(decision) {
		t.Errorf("DecidedAt = %s, UpdatedAt = %s, attendu %s pour les deux", retenu.DecidedAt, retenu.UpdatedAt, decision)
	}

	// L'entité se manipule par valeur : la décision ne revient pas changer le
	// devis d'origine dans le dos de son appelant.
	if recu.Statut != devis.StatutRecu {
		t.Errorf("le devis d'origine a été muté : statut = %q", recu.Statut)
	}

	refuse, err := recu.Reject(acteur, decision)
	if err != nil {
		t.Fatalf("Reject() échoué : %v", err)
	}
	if refuse.Statut != devis.StatutRefuse {
		t.Errorf("statut après Reject() = %q, attendu %q", refuse.Statut, devis.StatutRefuse)
	}
}

// TestDevisDecisionStoresUTC : une décision prise dans un autre fuseau est
// consignée en UTC, comme tout ce que le domaine date.
func TestDevisDecisionStoresUTC(t *testing.T) {
	t.Parallel()

	paris := time.FixedZone("CET", 3600)
	local := time.Date(2026, time.March, 20, 9, 0, 0, 0, paris)

	retenu, err := devis.Devis{Statut: devis.StatutRecu}.Retain(acteur, local)
	if err != nil {
		t.Fatalf("Retain() échoué : %v", err)
	}
	if zone, _ := retenu.DecidedAt.Zone(); zone != "UTC" {
		t.Errorf("DecidedAt est en %s, attendu UTC", zone)
	}
	if !retenu.DecidedAt.Equal(local) {
		t.Errorf("DecidedAt = %s, l'instant doit être conservé (%s)", retenu.DecidedAt, local)
	}
}

func TestDevisDecisionRefused(t *testing.T) {
	t.Parallel()

	decision := time.Date(2026, time.March, 20, 8, 0, 0, 0, time.UTC)

	cases := map[string]struct {
		devis devis.Devis
		by    devis.ActeurID
		at    time.Time
		want  error
	}{
		"un devis retenu ne se retient pas deux fois": {
			devis: devis.Devis{Statut: devis.StatutRetenu}, by: acteur, at: decision, want: devis.ErrForbiddenTransition,
		},
		"un devis refusé ne se retient pas": {
			devis: devis.Devis{Statut: devis.StatutRefuse}, by: acteur, at: decision, want: devis.ErrForbiddenTransition,
		},
		"un statut inconnu n'ouvre aucune transition": {
			devis: devis.Devis{Statut: devis.Statut("brouillon")}, by: acteur, at: decision, want: devis.ErrForbiddenTransition,
		},
		"sans acteur": {
			devis: devis.Devis{Statut: devis.StatutRecu}, by: "", at: decision, want: devis.ErrMissingActor,
		},
		"sans date": {
			devis: devis.Devis{Statut: devis.StatutRecu}, by: acteur, at: time.Time{}, want: devis.ErrMissingDate,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := tc.devis.Retain(tc.by, tc.at); !errors.Is(err, tc.want) {
				t.Errorf("Retain() = %v, attendu %v", err, tc.want)
			}
		})
	}
}

// TestDevisRejectRefusedOnDecided couvre le pendant du cas précédent sur le
// chemin du refus : sans lui, une transition permissive n'y serait vue par
// personne.
func TestDevisRejectRefusedOnDecided(t *testing.T) {
	t.Parallel()

	decision := time.Date(2026, time.March, 20, 8, 0, 0, 0, time.UTC)

	for _, statut := range []devis.Statut{devis.StatutRetenu, devis.StatutRefuse} {
		proposition := devis.Devis{Statut: statut}
		if _, err := proposition.Reject(acteur, decision); !errors.Is(err, devis.ErrForbiddenTransition) {
			t.Errorf("Reject() depuis %q = %v, attendu %v", statut, err, devis.ErrForbiddenTransition)
		}
	}
}

func TestDevisValidUntil(t *testing.T) {
	t.Parallel()

	recu := time.Date(2026, time.March, 12, 0, 0, 0, 0, time.UTC)

	cases := map[string]struct {
		validity time.Duration
		known    bool
		until    time.Time
	}{
		"trente jours": {
			validity: 30 * 24 * time.Hour,
			known:    true,
			until:    time.Date(2026, time.April, 11, 0, 0, 0, 0, time.UTC),
		},
		"non renseignée": {validity: 0, known: false},
		"négative":       {validity: -time.Hour, known: false},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			proposition := devis.Devis{ReceivedAt: recu, Validity: tc.validity}

			until, known := proposition.ValidUntil()
			if known != tc.known {
				t.Fatalf("ValidUntil() connue = %t, attendu %t", known, tc.known)
			}
			if known && !until.Equal(tc.until) {
				t.Errorf("ValidUntil() = %s, attendu %s", until, tc.until)
			}
		})
	}
}

// TestDevisExpired : un devis sans durée annoncée n'expire jamais. Inventer une
// échéance ferait écarter une offre encore valable.
func TestDevisExpired(t *testing.T) {
	t.Parallel()

	recu := time.Date(2026, time.March, 12, 0, 0, 0, 0, time.UTC)
	limite := recu.Add(30 * 24 * time.Hour)

	cases := map[string]struct {
		validity time.Duration
		now      time.Time
		want     bool
	}{
		"la veille de l'échéance": {validity: 30 * 24 * time.Hour, now: limite.Add(-24 * time.Hour), want: false},
		"le jour de l'échéance":   {validity: 30 * 24 * time.Hour, now: limite, want: false},
		"le lendemain":            {validity: 30 * 24 * time.Hour, now: limite.Add(time.Second), want: true},
		"sans durée annoncée":     {validity: 0, now: limite.Add(10 * 365 * 24 * time.Hour), want: false},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			proposition := devis.Devis{ReceivedAt: recu, Validity: tc.validity}
			if got := proposition.Expired(tc.now); got != tc.want {
				t.Errorf("Expired(%s) = %t, attendu %t", tc.now, got, tc.want)
			}
		})
	}
}

func TestNewIDIsUnique(t *testing.T) {
	t.Parallel()

	seen := make(map[devis.ID]struct{}, 64)
	for range 64 {
		id, err := devis.NewID()
		if err != nil {
			t.Fatalf("NewID() échoué : %v", err)
		}
		if len(id.String()) != 36 {
			t.Fatalf("NewID() = %q, un UUID canonique fait 36 caractères", id)
		}
		if _, duplicate := seen[id]; duplicate {
			t.Fatalf("NewID() a rendu deux fois %q", id)
		}
		seen[id] = struct{}{}
	}
}

func TestActeurIDString(t *testing.T) {
	t.Parallel()

	if got := acteur.String(); got != string(acteur) {
		t.Errorf("ActeurID.String() = %q, attendu %q", got, string(acteur))
	}
}
